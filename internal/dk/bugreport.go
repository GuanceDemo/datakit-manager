package dk

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Binary is the DataKit executable inside the container.
const Binary = "/usr/local/datakit/datakit"

// BugReportOptions tunes `datakit debug --bug-report`.
type BugReportOptions struct {
	DisableProfile bool `json:"disableProfile"` // skip pprof collection (lighter on a busy DataKit)
	NMetrics       int  `json:"nmetrics"`       // how many times /metrics is sampled; 0 = DataKit's default (3)
}

// Flags renders the options as command-line flags (only fixed words and a number, safe unquoted).
func (o BugReportOptions) Flags() string {
	var f []string
	if o.DisableProfile {
		f = append(f, "--disable-profile")
	}
	if o.NMetrics > 0 {
		if o.NMetrics > 60 {
			o.NMetrics = 60
		}
		f = append(f, "--nmetrics", strconv.Itoa(o.NMetrics))
	}
	return strings.Join(f, " ")
}

// BugReportDirPrefix is where the report is staged on the DataKit side before it is fetched.
const BugReportDirPrefix = "/tmp/dkm-br."

// BugReportScript runs the collection in a private temp dir ($1 = datakit binary, $2 = flags).
// DataKit writes info-<ms>.zip into the current directory. First output line: the zip's path;
// the rest: DataKit's own output.
const BugReportScript = `d="` + BugReportDirPrefix + `$$"; rm -rf "$d"; mkdir -p "$d" && chmod 700 "$d" && cd "$d" || exit 1
"$1" debug --bug-report $2 > out.log 2>&1; rc=$?
f="$(ls info-*.zip 2>/dev/null | head -n 1)"
if [ -z "$f" ]; then cat out.log; cd /; rm -rf "$d"; echo "没有生成 bug report 文件（退出码 $rc）" >&2; exit 1; fi
echo "$d/$f"; cat out.log`

// ParseBugReportOutput splits the script's stdout into the remote zip path and DataKit's output.
func ParseBugReportOutput(out string) (remote, log string, err error) {
	remote, log, _ = strings.Cut(out, "\n")
	remote = strings.TrimSpace(remote)
	if !strings.HasPrefix(remote, BugReportDirPrefix) || !strings.HasSuffix(remote, ".zip") || strings.ContainsAny(remote, " \t'\"\\$`;&|") ||
		strings.Contains(remote, "..") || strings.Count(remote, "/") != 3 {
		return "", out, errors.New("没有得到 bug report 文件路径")
	}
	return remote, strings.TrimSpace(log), nil
}

// CheckZip rejects anything that is not a zip archive.
func CheckZip(data []byte) error {
	if len(data) < 4 || string(data[:2]) != "PK" {
		return fmt.Errorf("取回的 bug report 不是有效的 zip 文件（%d 字节）", len(data))
	}
	return nil
}

// BugReport runs `datakit debug --bug-report` inside one pod and fetches the resulting zip.
// It returns the file name DataKit chose, the archive and DataKit's output.
func (c *Client) BugReport(ctx context.Context, pod, container string, o BugReportOptions) (name string, data []byte, output string, err error) {
	if pod == "" {
		return "", nil, "", errors.New("请选择要导出的 Pod")
	}
	out, err := c.execPod(ctx, pod, container, []string{"sh", "-c", BugReportScript, "sh", Binary, o.Flags()}, nil)
	if err != nil {
		if msg := strings.TrimSpace(out); msg != "" {
			return "", nil, msg, fmt.Errorf("%w\n%s", err, tailText(msg, 1500))
		}
		return "", nil, "", err
	}
	remote, output, err := ParseBugReportOutput(out)
	if err != nil {
		return "", nil, output, err
	}
	defer func() { // the staged copy inside the pod is ours to remove
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20e9)
		defer cancel()
		_, _ = c.execPod(cctx, pod, container, []string{"rm", "-rf", path.Dir(remote)}, nil)
	}()
	raw, err := c.execPod(ctx, pod, container, []string{"cat", remote}, nil)
	if err != nil {
		return "", nil, output, fmt.Errorf("取回 bug report 失败：%w", err)
	}
	data = []byte(raw)
	if err := CheckZip(data); err != nil {
		return "", nil, output, err
	}
	return path.Base(remote), data, output, nil
}

func tailText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
