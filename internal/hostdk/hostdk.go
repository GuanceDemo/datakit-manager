// Package hostdk manages DataKit installed directly on a Linux host, over SSH.
package hostdk

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"golang.org/x/crypto/ssh"

	"datakit-manager/internal/dk"
)

// DefaultInstallDir is where the official installer puts DataKit on Linux.
const DefaultInstallDir = "/usr/local/datakit"

// Target is how to reach a host.
type Target struct {
	Address    string
	Port       int
	User       string
	PrivateKey string
	Passphrase string
	// HostKey pins the server key (authorized_keys format). Empty = trust on first use:
	// the key seen is returned in Conn.HostKey for the caller to store.
	HostKey string
	// InstallDir overrides DefaultInstallDir (tests).
	InstallDir string
}

// Conn is an open SSH connection to a host.
type Conn struct {
	client     *ssh.Client
	HostKey    string // server key actually presented
	User       string
	Root       bool   // commands run as root (directly or through passwordless sudo)
	sudo       string // "" or "sudo -n "
	installDir string
}

// ParseKey checks a private key (and passphrase) before it is stored.
func ParseKey(privateKey, passphrase string) (ssh.Signer, error) {
	var signer ssh.Signer
	var err error
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(privateKey), []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(privateKey))
	}
	var missing *ssh.PassphraseMissingError
	switch {
	case errors.As(err, &missing):
		return nil, errors.New("这个私钥有口令保护，请填写口令")
	case err != nil && passphrase != "":
		return nil, fmt.Errorf("无法解析私钥（口令是否正确？）：%w", err)
	case err != nil:
		return nil, fmt.Errorf("无法解析私钥，需要 PEM / OpenSSH 格式的私钥文件（不是 .pub 公钥）：%w", err)
	}
	return signer, nil
}

// Dial connects and works out whether commands can run as root.
func Dial(ctx context.Context, t Target) (*Conn, error) {
	signer, err := ParseKey(t.PrivateKey, t.Passphrase)
	if err != nil {
		return nil, err
	}
	if t.Port == 0 {
		t.Port = 22
	}
	if t.User == "" {
		t.User = "root"
	}
	conn := &Conn{User: t.User, installDir: t.InstallDir}
	if conn.installDir == "" {
		conn.installDir = DefaultInstallDir
	}
	cfg := &ssh.ClientConfig{
		User:    t.User,
		Auth:    []ssh.AuthMethod{ssh.PublicKeys(signer)},
		Timeout: 15 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			conn.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
			if t.HostKey != "" && t.HostKey != conn.HostKey {
				return fmt.Errorf("主机密钥与导入时不一致（%s）。如果主机确实重装过，请移除后重新导入", ssh.FingerprintSHA256(key))
			}
			return nil
		},
	}
	addr := net.JoinHostPort(t.Address, strconv.Itoa(t.Port))
	d := net.Dialer{Timeout: 15 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", addr, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		_ = raw.Close()
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, fmt.Errorf("SSH 认证失败：用户 %s 不接受这把密钥", t.User)
		}
		return nil, fmt.Errorf("SSH 握手失败: %w", err)
	}
	conn.client = ssh.NewClient(c, chans, reqs)

	uid, err := conn.exec(ctx, "id -u", nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("无法在主机上执行命令: %w", err)
	}
	switch {
	case strings.TrimSpace(uid) == "0":
		conn.Root = true
	default:
		if _, err := conn.exec(ctx, "sudo -n true", nil); err == nil {
			conn.Root, conn.sudo = true, "sudo -n "
		}
	}
	return conn, nil
}

// Close ends the connection.
func (c *Conn) Close() {
	if c.client != nil {
		_ = c.client.Close()
	}
}

// q quotes a string for a POSIX shell.
func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// exec runs a command line as the login user.
func (c *Conn) exec(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	sess, err := c.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout, sess.Stderr, sess.Stdin = &stdout, &stderr, stdin
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		_ = sess.Close()
		return stdout.String(), fmt.Errorf("命令超时或被取消")
	case err := <-done:
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 2000 {
				msg = msg[len(msg)-2000:]
			}
			if msg != "" {
				return stdout.String(), fmt.Errorf("%s", msg)
			}
			return stdout.String(), err
		}
		return stdout.String(), nil
	}
}

// run executes a shell script as root (sudo when needed). Extra args become $1, $2...
func (c *Conn) run(ctx context.Context, script string, stdin io.Reader, args ...string) (string, error) {
	cmd := c.sudo + "sh -c " + q(script) + " sh"
	for _, a := range args {
		cmd += " " + q(a)
	}
	return c.exec(ctx, cmd, stdin)
}

func (c *Conn) confDir() string  { return c.installDir + "/conf.d" }
func (c *Conn) mainConf() string { return c.confDir() + "/datakit.conf" }
func (c *Conn) binary() string   { return c.installDir + "/datakit" }

// ConfFile is one collector config on the host.
type ConfFile struct {
	Path    string   `json:"path"` // absolute
	Rel     string   `json:"rel"`  // relative to conf.d
	Content string   `json:"content"`
	Inputs  []string `json:"inputs"`
	Error   string   `json:"error"`
}

// Info is the state of a host and its DataKit.
type Info struct {
	Hostname      string     `json:"hostname"`
	OS            string     `json:"os"`
	Arch          string     `json:"arch"`
	Kernel        string     `json:"kernel"`
	User          string     `json:"user"`
	Root          bool       `json:"root"`
	Installed     bool       `json:"installed"`
	Version       string     `json:"version"`
	Running       bool       `json:"running"`
	Service       string     `json:"service"`
	InstallDir    string     `json:"installDir"`
	ConfDir       string     `json:"confDir"`
	MainConfPath  string     `json:"mainConfPath"`
	MainConf      string     `json:"mainConf"`
	HTTPListen    string     `json:"httpListen"`
	DefaultInputs []string   `json:"defaultInputs"`
	ConfFiles     []ConfFile `json:"confFiles"`
}

func marker() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "@@dkm-" + hex.EncodeToString(b)
}

const inspectScript = `M="$1"; D="$2"
echo "$M hostname"; hostname 2>/dev/null
echo "$M os"; (. /etc/os-release 2>/dev/null; echo "$PRETTY_NAME")
echo "$M arch"; uname -m
echo "$M kernel"; uname -r
echo "$M installed"; [ -x "$D/datakit" ] && echo yes || echo no
echo "$M version"; "$D/datakit" version 2>/dev/null | head -n 12
echo "$M service"; if command -v systemctl >/dev/null 2>&1; then systemctl is-active datakit 2>/dev/null || true; else echo unknown; fi
echo "$M proc"; if pgrep -x datakit >/dev/null 2>&1; then echo yes; else echo no; fi
echo "$M mainconf"; cat "$D/conf.d/datakit.conf" 2>/dev/null
echo "$M end"`

var versionRe = regexp.MustCompile(`(?m)^\s*Version:\s*(\S+)`)

func sections(out, m string) map[string]string {
	res := map[string]string{}
	parts := strings.Split("\n"+out, "\n"+m+" ")
	for _, p := range parts[1:] {
		nl := strings.IndexByte(p, '\n')
		if nl < 0 {
			res[strings.TrimSpace(p)] = ""
			continue
		}
		res[strings.TrimSpace(p[:nl])] = strings.TrimRight(p[nl+1:], "\n")
	}
	return res
}

// Inspect gathers host facts, DataKit state and every collector config.
func (c *Conn) Inspect(ctx context.Context) (*Info, error) {
	m := marker()
	out, err := c.run(ctx, inspectScript, nil, m, c.installDir)
	if err != nil {
		return nil, err
	}
	s := sections(out, m)
	info := &Info{
		Hostname: strings.TrimSpace(s["hostname"]), OS: strings.TrimSpace(s["os"]), Arch: strings.TrimSpace(s["arch"]), Kernel: strings.TrimSpace(s["kernel"]),
		User: c.User, Root: c.Root, Installed: strings.TrimSpace(s["installed"]) == "yes",
		Service: strings.TrimSpace(s["service"]), Running: strings.TrimSpace(s["proc"]) == "yes",
		InstallDir: c.installDir, ConfDir: c.confDir(), MainConfPath: c.mainConf(), MainConf: s["mainconf"],
		HTTPListen: "localhost:9529", DefaultInputs: []string{}, ConfFiles: []ConfFile{},
	}
	if v := versionRe.FindStringSubmatch(s["version"]); v != nil {
		info.Version = v[1]
	}
	if info.Service == "active" {
		info.Running = true
	}
	if !info.Installed {
		return info, nil
	}
	if info.MainConf != "" {
		var mc struct {
			DefaultEnabledInputs []string `toml:"default_enabled_inputs"`
			HTTP                 struct {
				Listen string `toml:"listen"`
			} `toml:"http_api"`
		}
		if _, err := toml.Decode(info.MainConf, &mc); err == nil {
			if mc.HTTP.Listen != "" {
				info.HTTPListen = mc.HTTP.Listen
			}
			if mc.DefaultEnabledInputs != nil {
				info.DefaultInputs = mc.DefaultEnabledInputs
			}
		}
	}

	out, err = c.run(ctx, dk.ListScript, nil, c.confDir(), m)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", c.confDir(), err)
	}
	files := dk.ParseListing(out, m, c.confDir())
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		f := ConfFile{Path: p, Rel: strings.TrimPrefix(p, c.confDir()+"/"), Content: files[p], Inputs: []string{}}
		if ins, err := dk.ParseInputs(f.Content); err != nil {
			f.Error = err.Error()
		} else if ins != nil {
			f.Inputs = ins
		}
		info.ConfFiles = append(info.ConfFiles, f)
	}
	return info, nil
}

// FileChange writes one file under conf.d (Path absolute).
type FileChange struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Changes is one edit of a host's DataKit configuration.
type Changes struct {
	Upserts []FileChange `json:"upserts"`
	Deletes []string     `json:"deletes"`
}

var nameRe = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

func (c *Conn) checkPath(p string) error {
	if path.Clean(p) != p || !strings.HasPrefix(p, c.confDir()+"/") {
		return fmt.Errorf("%s 不在 %s 目录内", p, c.confDir())
	}
	for _, seg := range strings.Split(strings.TrimPrefix(p, c.confDir()+"/"), "/") {
		if !nameRe.MatchString(seg) || seg == ".." {
			return fmt.Errorf("路径 %s 含有不支持的字符（只能用字母、数字、- _ .）", p)
		}
	}
	if !strings.HasSuffix(p, ".conf") {
		return fmt.Errorf("%s 必须以 .conf 结尾，否则 DataKit 不会加载", p)
	}
	return nil
}

// Plan validates the changes and describes what Apply will do.
func (c *Conn) Plan(ch Changes) (actions, warnings []string, err error) {
	actions, warnings = []string{}, []string{}
	if !c.Root {
		return nil, nil, fmt.Errorf("用户 %s 不是 root，也没有免密 sudo，无法修改 DataKit 配置", c.User)
	}
	for _, u := range ch.Upserts {
		if err := c.checkPath(u.Path); err != nil {
			return nil, nil, err
		}
		var probe map[string]any
		if _, err := toml.Decode(u.Content, &probe); err != nil {
			return nil, nil, fmt.Errorf("%s 不是合法的 TOML: %w", path.Base(u.Path), err)
		}
		if u.Path != c.mainConf() {
			if ins, _ := dk.ParseInputs(u.Content); len(ins) == 0 {
				warnings = append(warnings, fmt.Sprintf("%s 中没有 [[inputs.xxx]] 配置段，DataKit 加载后不会启动任何采集器", path.Base(u.Path)))
			}
		}
		actions = append(actions, "写入 "+u.Path)
	}
	for _, d := range ch.Deletes {
		if err := c.checkPath(d); err != nil {
			return nil, nil, err
		}
		if d == c.mainConf() {
			return nil, nil, errors.New("不能删除主配置 datakit.conf")
		}
		actions = append(actions, "删除 "+d)
	}
	actions = append(actions, "重启 datakit 服务")
	return actions, warnings, nil
}

// writeScript writes stdin to $1 atomically. An existing file is copied first (cp -p) so the
// temp file inherits its mode and owner; this works with GNU, BSD and busybox userlands alike.
const writeScript = `set -e; d="$(dirname "$1")"; mkdir -p "$d"
if [ -f "$1" ]; then cp -p "$1" "$1.dkm-tmp"; else (umask 077; : > "$1.dkm-tmp"); fi
cat > "$1.dkm-tmp"
mv -f "$1.dkm-tmp" "$1"`

// Apply writes / deletes the files and restarts DataKit. It returns the previous content
// of every touched file (path -> content) so the caller can keep a backup.
func (c *Conn) Apply(ctx context.Context, ch Changes) (actions, warnings []string, previous map[string]string, err error) {
	actions, warnings, err = c.Plan(ch)
	if err != nil {
		return nil, nil, nil, err
	}
	previous = map[string]string{}
	touched := append([]string{}, ch.Deletes...)
	for _, u := range ch.Upserts {
		touched = append(touched, u.Path)
	}
	for _, p := range touched {
		if old, err := c.run(ctx, `cat "$1" 2>/dev/null || true`, nil, p); err == nil && old != "" {
			previous[p] = old
		}
	}
	for _, u := range ch.Upserts {
		if _, err := c.run(ctx, writeScript, strings.NewReader(u.Content), u.Path); err != nil {
			return nil, nil, previous, fmt.Errorf("写入 %s 失败: %w", u.Path, err)
		}
	}
	for _, d := range ch.Deletes {
		if _, err := c.run(ctx, `rm -f -- "$1"`, nil, d); err != nil {
			return nil, nil, previous, fmt.Errorf("删除 %s 失败: %w", d, err)
		}
	}
	if err := c.Restart(ctx); err != nil {
		return actions, warnings, previous, fmt.Errorf("配置已写入，但重启 datakit 失败: %w", err)
	}
	return actions, warnings, previous, nil
}

// Restart restarts the DataKit service.
func (c *Conn) Restart(ctx context.Context) error {
	_, err := c.run(ctx, `if "$1" service -R >/dev/null 2>&1; then exit 0; fi
if command -v systemctl >/dev/null 2>&1; then systemctl restart datakit; else "$1" service -R; fi`, nil, c.binary())
	return err
}

// Metrics scrapes DataKit's own /metrics on the host.
func (c *Conn) Metrics(ctx context.Context, listen, hostname string) dk.PodMetrics {
	pm := dk.PodMetrics{Pod: hostname, Node: hostname, Inputs: []dk.InputInstance{}, Feeds: []dk.FeedStat{}, Uploads: []dk.UploadStat{}, Errors: []dk.LastError{}}
	if listen == "" {
		listen = "localhost:9529"
	}
	if strings.HasPrefix(listen, "0.0.0.0:") || strings.HasPrefix(listen, ":") || strings.HasPrefix(listen, "[::]:") {
		listen = "127.0.0.1:" + listen[strings.LastIndexByte(listen, ':')+1:]
	}
	out, err := c.run(ctx, `u="http://$1/metrics"; curl -s --max-time 8 "$u" 2>/dev/null || wget -qO- -T 8 "$u"`, nil, listen)
	if err != nil || strings.TrimSpace(out) == "" {
		pm.Error = "无法读取 DataKit 指标（" + listen + "）"
		if err != nil {
			pm.Error += "：" + err.Error()
		}
		return pm
	}
	parsed, err := dk.ParseMetrics([]byte(out))
	if err != nil {
		pm.Error = "解析指标失败: " + err.Error()
		return pm
	}
	parsed.Pod, parsed.Node, parsed.Via = hostname, hostname, "ssh"
	return *parsed
}

// Logs returns the tail of DataKit's log file.
func (c *Conn) Logs(ctx context.Context, lines int) (string, error) {
	if lines <= 0 {
		lines = 300
	}
	return c.run(ctx, `tail -n "$1" /var/log/datakit/log 2>/dev/null || journalctl -u datakit -n "$1" --no-pager 2>/dev/null || echo "没有找到 DataKit 日志"`, nil, strconv.Itoa(lines))
}
