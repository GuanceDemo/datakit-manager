package hostdk

import (
	"context"
	"datakit-manager/internal/dk"
	"datakit-manager/internal/hostdk/sshtest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeInstall lays out an installed DataKit under dir: a `datakit` stub that reports a
// version and records restarts, a main config and two collector configs.
func fakeInstall(t *testing.T, dir, listen string) {
	t.Helper()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "conf.d", "db"), 0o755))
	must(os.MkdirAll(filepath.Join(dir, "conf.d", "host"), 0o755))
	stub := "#!/bin/sh\nif [ \"$1\" = version ]; then echo; echo '       Version: 2.19.0'; echo '        Commit: abc'; exit 0; fi\n" +
		"if [ \"$1\" = service ]; then echo restarted >> \"$(dirname \"$0\")/restarts\"; exit 0; fi\n" +
		"if [ \"$1\" = debug ]; then echo \"$*\" > \"$(dirname \"$0\")/br-args\"; printf 'PK\\003\\004\\000\\377\\r\\n\\200bin' > info-1700000000000.zip; echo 'bug report saved to local file info-1700000000000.zip'; exit 0; fi\n"
	must(os.WriteFile(filepath.Join(dir, "datakit"), []byte(stub), 0o755))
	must(os.WriteFile(filepath.Join(dir, "conf.d", "datakit.conf"), []byte("default_enabled_inputs = [\"cpu\", \"mem\"]\n[http_api]\n  listen = \""+listen+"\"\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "conf.d", "db", "mysql.conf"), []byte("# prod\n[[inputs.mysql]]\n  host = \"db1\"\n"), 0o640))
	must(os.WriteFile(filepath.Join(dir, "conf.d", "host", "cpu.conf"), []byte("[[inputs.cpu]]\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "conf.d", "db", "redis.conf.sample"), []byte("[[inputs.redis]]\n"), 0o644))
}

func TestHostEndToEnd(t *testing.T) {
	ctx := context.Background()
	keyPEM, pub, _ := sshtest.GenKey(t)
	srv := sshtest.Start(t, pub)

	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# TYPE datakit_uptime_seconds gauge\ndatakit_uptime_seconds{hostname=\"h1\",version=\"2.19.0\"} 120\n" +
			"# TYPE datakit_io_feed_total counter\ndatakit_io_feed_total{category=\"metric\",name=\"mysql\"} 9\n"))
	}))
	defer metrics.Close()
	listen := strings.TrimPrefix(metrics.URL, "http://")

	dir := t.TempDir()
	fakeInstall(t, dir, listen)
	target := Target{Address: srv.Addr, Port: srv.Port, User: "tester", PrivateKey: keyPEM, InstallDir: dir}

	// first contact: the host key is reported so it can be pinned
	c, err := Dial(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(srv.HostKey.PublicKey())))
	if c.HostKey != want {
		t.Fatalf("host key %q", c.HostKey)
	}
	// a different pinned key (machine replaced / MITM) is refused; so is a key the host does not accept
	bad := target
	bad.HostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOtherKeyOtherKeyOtherKeyOtherKeyOtherKeyOtherKey"
	if _, err := Dial(ctx, bad); err == nil || !strings.Contains(err.Error(), "主机密钥") {
		t.Fatalf("pinned key mismatch must fail, got %v", err)
	}
	otherPEM, _, _ := sshtest.GenKey(t)
	other := target
	other.PrivateKey = otherPEM
	if _, err := Dial(ctx, other); err == nil || !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("unknown key must fail, got %v", err)
	}
	if _, err := ParseKey("ssh-ed25519 AAAA public-key", ""); err == nil {
		t.Fatal("a public key is not a private key")
	}

	c.Root = true // the test user owns the temp install dir; no sudo needed
	info, err := c.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Installed || info.Version != "2.19.0" || info.HTTPListen != listen || len(info.DefaultInputs) != 2 || info.Arch == "" {
		t.Fatalf("%+v", info)
	}
	// collector configs are listed with their content; samples and the main config are not
	if len(info.ConfFiles) != 2 || info.ConfFiles[0].Rel != "db/mysql.conf" || !strings.Contains(info.ConfFiles[0].Content, "# prod") || info.ConfFiles[0].Inputs[0] != "mysql" {
		t.Fatalf("%+v", info.ConfFiles)
	}

	// path and syntax validation
	for _, p := range []string{"/etc/cron.d/x.conf", dir + "/conf.d/../evil.conf", dir + "/conf.d/a b.conf", dir + "/conf.d/x.txt"} {
		if _, _, err := c.Plan(Changes{Upserts: []FileChange{{Path: p, Content: "[[inputs.cpu]]\n"}}}); err == nil {
			t.Fatalf("path %q must be refused", p)
		}
	}
	if _, _, err := c.Plan(Changes{Upserts: []FileChange{{Path: dir + "/conf.d/x.conf", Content: "[[inputs.cpu]\n"}}}); err == nil {
		t.Fatal("invalid TOML must be refused")
	}
	if _, _, err := c.Plan(Changes{Deletes: []string{c.mainConf()}}); err == nil {
		t.Fatal("main config must not be deletable")
	}

	// edit an existing file, add a new one, change the main config: written + service restarted
	edited := "# prod\n[[inputs.mysql]]\n  host = \"db2\"\n"
	newFile := dir + "/conf.d/redis.conf"
	_, _, prev, err := c.Apply(ctx, Changes{Upserts: []FileChange{
		{Path: info.ConfFiles[0].Path, Content: edited},
		{Path: newFile, Content: "[[inputs.redis]]\n  host = \"it's\"\n"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prev[info.ConfFiles[0].Path], "db1") {
		t.Fatalf("previous content not returned: %v", prev)
	}
	if b, _ := os.ReadFile(info.ConfFiles[0].Path); string(b) != edited {
		t.Fatalf("file content %q", b)
	}
	if st, _ := os.Stat(info.ConfFiles[0].Path); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode of an existing file must be kept, got %v", st.Mode().Perm())
	}
	if b, _ := os.ReadFile(newFile); string(b) != "[[inputs.redis]]\n  host = \"it's\"\n" {
		t.Fatalf("new file %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "restarts")); strings.Count(string(b), "restarted") != 1 {
		t.Fatalf("restart count: %q", b)
	}
	if _, _, _, err := c.Apply(ctx, Changes{Deletes: []string{newFile}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Fatal("file not deleted")
	}

	pm := c.Metrics(ctx, info.HTTPListen, info.Hostname)
	if !pm.OK || pm.Version != "2.19.0" || len(pm.Feeds) != 1 || pm.Feeds[0].Name != "mysql" {
		t.Fatalf("%+v", pm)
	}

	// bug report: the archive comes back byte for byte and the staging dir is removed
	name, data, output, err := c.BugReport(ctx, dk.BugReportOptions{DisableProfile: true, NMetrics: 5})
	if err != nil {
		t.Fatal(err)
	}
	if name != "info-1700000000000.zip" || string(data) != "PK\x03\x04\x00\xff\r\n\x80bin" || !strings.Contains(output, "saved to local file") {
		t.Fatalf("%q %q %q", name, data, output)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "br-args")); strings.TrimSpace(string(b)) != "debug --bug-report --disable-profile --nmetrics 5" {
		t.Fatalf("args %q", b)
	}
	if left, _ := filepath.Glob(dk.BugReportDirPrefix + "*"); len(left) != 0 {
		t.Fatalf("staging dir left behind: %v", left)
	}

	// a host without DataKit
	empty := target
	empty.InstallDir = t.TempDir()
	c2, err := Dial(ctx, empty)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if info, err := c2.Inspect(ctx); err != nil || info.Installed || len(info.ConfFiles) != 0 {
		t.Fatalf("%v %+v", err, info)
	}
}
