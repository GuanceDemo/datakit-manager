package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBugReportFiles(t *testing.T) {
	s, err := New(t.TempDir(), "test", true)
	if err != nil {
		t.Fatal(err)
	}
	// a stale report is cleaned up when the next one is saved
	old := filepath.Join(s.bugReportsDir(), "bugreport-old.zip")
	_ = os.MkdirAll(s.bugReportsDir(), 0o700)
	_ = os.WriteFile(old, []byte("PK"), 0o600)
	_ = os.Chtimes(old, time.Now().Add(-8*24*time.Hour), time.Now().Add(-8*24*time.Hour))

	f, err := s.saveBugReport([]string{"生产/上海 prod", "node-1", "../datakit-x"}, []byte("PK\x03\x04"), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.File, "bugreport-生产-上海-prod-node-1-datakit-x-") || !strings.HasSuffix(f.File, ".zip") || f.Size != 4 || strings.ContainsAny(f.File, "/ ") {
		t.Fatalf("%+v", f)
	}
	if p, err := s.BugReportPath(f.File); err != nil || p != f.Path {
		t.Fatalf("%v %s", err, p)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("stale report not removed")
	}
	for _, bad := range []string{"", "../secret.key", "bugreport-../../secret.key", "secret.key", "bugreport-missing.zip", "/etc/passwd"} {
		if _, err := s.BugReportPath(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}
