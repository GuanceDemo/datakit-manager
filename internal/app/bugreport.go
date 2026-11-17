package app

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"datakit-manager/internal/dk"
)

// BugReportFile describes an exported DataKit bug report kept in <data dir>/bugreports.
type BugReportFile struct {
	File   string `json:"file"`   // file name inside the bugreports directory
	Path   string `json:"path"`   // absolute path on the machine running the backend
	Size   int64  `json:"size"`   // bytes
	Output string `json:"output"` // what `datakit debug --bug-report` printed
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)

func safeName(s string) string {
	s = strings.Trim(unsafeName.ReplaceAllString(s, "-"), "-.")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

func (s *Service) bugReportsDir() string { return filepath.Join(s.store.Dir(), "bugreports") }

// saveBugReport stores the archive and drops reports older than a week.
func (s *Service) saveBugReport(parts []string, data []byte, output string) (*BugReportFile, error) {
	dir := s.bugReportsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 7*24*time.Hour {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	name := "bugreport"
	for _, p := range parts {
		if p = safeName(p); p != "" {
			name += "-" + p
		}
	}
	name += "-" + time.Now().Format("20060102-150405") + ".zip"
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, data, 0o600); err != nil {
		return nil, err
	}
	return &BugReportFile{File: name, Path: full, Size: int64(len(data)), Output: output}, nil
}

// BugReport collects a bug report from one DataKit pod (`datakit debug --bug-report` inside the
// pod) and stores the zip in the data directory. node is only used to name the file.
func (s *Service) BugReport(id, pod, node, container string, o dk.BugReportOptions) (*BugReportFile, error) {
	c, err := s.client(id)
	if err != nil {
		return nil, err
	}
	cl, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(10 * time.Minute)
	defer cancel()
	_, data, output, err := c.BugReport(ctx, pod, container, o)
	if err != nil {
		return nil, err
	}
	return s.saveBugReport([]string{cl.Name, node, pod}, data, output)
}

// HostBugReport collects a bug report from the DataKit on a host.
func (s *Service) HostBugReport(id string, o dk.BugReportOptions) (*BugReportFile, error) {
	conn, h, ctx, done, err := s.dial(id, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	defer done()
	_, data, output, err := conn.BugReport(ctx, o)
	if err != nil {
		return nil, err
	}
	return s.saveBugReport([]string{h.Name, h.Address}, data, output)
}

// BugReportPath resolves a file name returned by BugReport to its absolute path.
func (s *Service) BugReportPath(file string) (string, error) {
	if file == "" || file != filepath.Base(file) || !strings.HasPrefix(file, "bugreport-") || !strings.HasSuffix(file, ".zip") {
		return "", errors.New("无效的文件名")
	}
	full := filepath.Join(s.bugReportsDir(), file)
	if st, err := os.Stat(full); err != nil || st.IsDir() {
		return "", errors.New("文件不存在，可能已被清理，请重新导出")
	}
	return full, nil
}
