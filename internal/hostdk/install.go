package hostdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultBaseURL is the official download location of DataKit 2.x.
const DefaultBaseURL = "https://static.guance.com/datakit-v2"

// InstallOptions are the knobs of a host install.
type InstallOptions struct {
	DataWay        string `json:"dataway"`
	DefInputs      string `json:"defInputs"`      // DK_DEF_INPUTS, optional
	GlobalHostTags string `json:"globalHostTags"` // DK_GLOBAL_HOST_TAGS, optional
	// Offline: the host has no internet access. The packages are downloaded by this
	// program (into CacheDir) and uploaded over SSH.
	Offline bool   `json:"offline"`
	Version string `json:"version"` // offline only; "" = latest
	Upgrade bool   `json:"upgrade"` // DataKit is already installed: upgrade it
	BaseURL string `json:"baseUrl"`
}

// LatestVersion asks the download site for the newest DataKit version.
func LatestVersion(ctx context.Context, baseURL string) (string, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/version", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil || v.Version == "" {
		return "", errors.New("版本信息格式不正确")
	}
	return v.Version, nil
}

func goArch(uname string) (string, error) {
	switch strings.TrimSpace(uname) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	case "i386", "i686":
		return "386", nil
	case "armv7l", "armv6l", "arm":
		return "arm", nil
	}
	return "", fmt.Errorf("不支持的 CPU 架构: %s", uname)
}

// offlineFiles are the packages an offline install needs, per the official docs.
func offlineFiles(arch, version string) []string {
	return []string{
		"installer-linux-" + arch,
		fmt.Sprintf("datakit-linux-%s-%s.tar.gz", arch, version),
		fmt.Sprintf("dk_upgrader-linux-%s-%s.tar.gz", arch, version),
		"data.tar.gz",
	}
}

// fetch downloads url into the cache unless a complete copy is already there.
func fetch(ctx context.Context, url, dest string) error {
	if st, err := os.Stat(dest); err == nil && st.Size() > 0 {
		return nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("下载 %s 失败: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 失败: HTTP %d", url, resp.StatusCode)
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	_ = f.Close()
	if err != nil || (resp.ContentLength > 0 && n != resp.ContentLength) {
		_ = os.Remove(tmp)
		return fmt.Errorf("下载 %s 不完整", url)
	}
	return os.Rename(tmp, dest)
}

func (o InstallOptions) env() string {
	var b strings.Builder
	set := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "export %s=%s\n", k, q(v))
		}
	}
	set("DK_DATAWAY", strings.TrimSpace(o.DataWay))
	set("DK_DEF_INPUTS", strings.TrimSpace(o.DefInputs))
	set("DK_GLOBAL_HOST_TAGS", strings.TrimSpace(o.GlobalHostTags))
	if o.Upgrade {
		set("DK_UPGRADE", "1")
	}
	return b.String()
}

// Install installs (or upgrades) DataKit with the official installer and returns its output.
// cacheDir holds the packages downloaded for offline installs.
func (c *Conn) Install(ctx context.Context, o InstallOptions, cacheDir string) (string, error) {
	if !c.Root {
		return "", fmt.Errorf("用户 %s 不是 root，也没有免密 sudo，无法安装 DataKit", c.User)
	}
	if !o.Upgrade && (strings.TrimSpace(o.DataWay) == "" || !strings.Contains(o.DataWay, "://")) {
		return "", errors.New("请填写 DataWay 地址（以 http:// 或 https:// 开头，包含工作空间 token）")
	}
	base := strings.TrimRight(o.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}

	if !o.Offline {
		script := o.env() + `command -v bash >/dev/null 2>&1 || { echo "主机上没有 bash" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "主机上没有 curl，请改用“离线上传”方式安装" >&2; exit 1; }
script="$(curl -fsSL --connect-timeout 15 "$1/install.sh")" || { echo "主机无法访问 $1 ，请改用“离线上传”方式安装" >&2; exit 1; }
bash -c "$script" 2>&1`
		out, err := c.run(ctx, script, nil, base)
		if err != nil {
			return out, fmt.Errorf("安装失败: %w", tailOr(out, err))
		}
		return out, nil
	}

	// offline: download here, upload over SSH, run the installer in offline mode
	uname, err := c.exec(ctx, "uname -m", nil)
	if err != nil {
		return "", err
	}
	arch, err := goArch(uname)
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(o.Version)
	if version == "" {
		if version, err = LatestVersion(ctx, base); err != nil {
			return "", fmt.Errorf("获取最新版本号失败（本机需要能访问 %s）：%w", base, err)
		}
	}
	dir := filepath.Join(cacheDir, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	files := offlineFiles(arch, version)
	for _, name := range files {
		if err := fetch(ctx, base+"/"+name, filepath.Join(dir, name)); err != nil {
			return "", fmt.Errorf("%w（也可以手动下载后放到 %s）", err, dir)
		}
	}
	remote := "/tmp/dkm-install-" + strings.TrimPrefix(marker(), "@@dkm-")
	if _, err := c.run(ctx, `mkdir -p "$1" && chmod 700 "$1"`, nil, remote); err != nil {
		return "", err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = c.run(cctx, `rm -rf -- "$1"`, nil, remote)
	}()
	for _, name := range files {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}
		_, err = c.run(ctx, `cat > "$1"`, f, remote+"/"+name)
		_ = f.Close()
		if err != nil {
			return "", fmt.Errorf("上传 %s 失败: %w", name, err)
		}
	}
	flags := "--offline"
	if o.Upgrade {
		flags += " --upgrade"
	} else {
		flags += " --dataway " + q(strings.TrimSpace(o.DataWay))
	}
	script := o.env() + `cd "$1" && chmod +x "$2" && ./"$2" ` + flags + ` --srcs "$3" 2>&1`
	out, err := c.run(ctx, script, nil, remote, files[0], strings.Join(files[1:], ","))
	if err != nil {
		return out, fmt.Errorf("安装失败: %w", tailOr(out, err))
	}
	return fmt.Sprintf("（离线安装 DataKit %s，%s）\n%s", version, arch, out), nil
}

// tailOr prefers the end of the installer output over a bare exit status.
func tailOr(out string, err error) error {
	out = strings.TrimSpace(out)
	if out == "" {
		return err
	}
	if len(out) > 1500 {
		out = "…" + out[len(out)-1500:]
	}
	return errors.New(out)
}
