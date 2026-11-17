package app

import (
	"encoding/json"
	"fmt"
	"os"

	"datakit-manager/internal/hostdk/sshtest"
	"path/filepath"
	"strings"
	"testing"

	"datakit-manager/internal/dk"
)

const kubeconfig = `apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: c1
  cluster: {server: "https://10.0.0.1:6443", insecure-skip-tls-verify: true}
users:
- name: u1
  user: {token: abc}
contexts:
- name: prod
  context: {cluster: c1, user: u1}
`

func raw(t *testing.T, vals ...any) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(vals))
	for i, v := range vals {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = b
	}
	return out
}

func TestWebFlow(t *testing.T) {
	s, err := New(t.TempDir(), "test", true)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := s.Call("Info", nil); info.(AppInfo).Mode != "web" {
		t.Fatalf("%+v", info)
	}
	// arbitrary server paths are refused in web mode
	if _, err := s.Call("ParseKubeconfig", raw(t, "/etc/passwd")); err == nil {
		t.Fatal("path outside uploads must be refused")
	}
	if _, err := s.Call("ParseKubeconfig", raw(t, filepath.Join(s.uploadsDir(), "..", "clusters.json"))); err == nil {
		t.Fatal("path traversal must be refused")
	}
	if _, err := s.Call("UploadKubeconfig", raw(t, "not a kubeconfig")); err == nil {
		t.Fatal("garbage must be refused")
	}
	h, err := s.Call("UploadKubeconfig", raw(t, kubeconfig))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Call("ParseKubeconfig", raw(t, h)); err != nil {
		t.Fatal(err)
	}
	// the uploaded kubeconfig is not readable on disk
	if disk, _ := os.ReadFile(h.(string)); strings.Contains(string(disk), "abc") || strings.Contains(string(disk), "10.0.0.1") {
		t.Fatal("upload must be encrypted at rest")
	}
	if _, err := s.Call("ImportClusters", raw(t, h, []string{"prod"}, "monitoring")); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListClusters()
	if len(list) != 1 || list[0].Namespace != "monitoring" {
		t.Fatalf("%+v", list)
	}

	y, err := s.Call("RenderDataKitYAML", raw(t, list[0].ID, dk.DataKitInstall{DataWay: "https://openway.guance.com?token=x", Namespace: "ignored"}))
	if err != nil || !strings.Contains(y.(string), "namespace: monitoring") {
		t.Fatalf("%v", err)
	}
	if _, err := s.Call("Nope", nil); err == nil {
		t.Fatal("unknown method")
	}
	if _, err := s.Call("Inspect", nil); err == nil {
		t.Fatal("arity must be checked")
	}
	if _, err := s.Call("Call", raw(t, "x", nil)); err == nil {
		t.Fatal("Call must not be reachable")
	}
}

func TestParseHostLine(t *testing.T) {
	cases := map[string]struct {
		user, addr, name string
		port             int
	}{
		"10.0.0.5":             {"root", "10.0.0.5", "10.0.0.5", 22},
		"ubuntu@10.0.0.6:2222": {"ubuntu", "10.0.0.6", "10.0.0.6", 2222},
		"ops@db1.example.com 生产 MySQL 主库": {"ops", "db1.example.com", "生产 MySQL 主库", 22},
	}
	for line, want := range cases {
		h, err := parseHostLine(line)
		if err != nil || h.User != want.user || h.Address != want.addr || h.Port != want.port || h.Name != want.name {
			t.Fatalf("%q -> %+v %v", line, h, err)
		}
	}
	for _, bad := range []string{"root@", "host:abc", "host:70000"} {
		if _, err := parseHostLine(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
	// importing with something that is not a private key fails before any connection
	s, _ := New(t.TempDir(), "test", true)
	if _, err := s.ImportHosts(HostImport{KeyContent: "ssh-ed25519 AAAA pub", Hosts: []string{"10.0.0.1"}}); err == nil {
		t.Fatal("public key must be rejected")
	}
	if _, err := s.ImportHosts(HostImport{KeyPath: "/root/.ssh/id_rsa", Hosts: []string{"10.0.0.1"}}); err == nil {
		t.Fatal("server-side key paths must be refused in web mode")
	}
}

// Hosts through the service: import over SSH, key encrypted on disk, pinned host key.
func TestHostsOverSSH(t *testing.T) {
	keyPEM, pub, _ := sshtest.GenKey(t)
	srv := sshtest.Start(t, pub)
	dir := t.TempDir()
	s, err := New(dir, "test", true)
	if err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("tester@%s:%d 测试机", srv.Addr, srv.Port)
	res, err := s.ImportHosts(HostImport{KeyContent: keyPEM, Hosts: []string{line + "\n# comment\ntester@127.0.0.1:1 unreachable"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 || res.Added[0].Name != "测试机" || res.Added[0].HostKey == "" || len(res.Errors) != 1 {
		t.Fatalf("%+v", res)
	}
	id := res.Added[0].ID
	// nothing readable about the key in the data directory
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && filepath.Base(p) != "secret.key" {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "OPENSSH PRIVATE KEY") {
				t.Fatalf("private key stored in plaintext in %s", p)
			}
		}
		return nil
	})
	// the stored host is reachable again with the stored (decrypted) key
	info, err := s.HostInspect(id)
	if err != nil || info.Hostname == "" || info.Arch == "" {
		t.Fatalf("%v %+v", err, info)
	}
	// a different machine answering on that address is refused
	srv2 := sshtest.Start(t, pub)
	hosts, _ := s.ListHosts()
	_ = hosts
	h, sec, _ := s.store.GetHost(id)
	if sec.PrivateKey != keyPEM {
		t.Fatal("key round trip")
	}
	_ = s.store.RemoveHost(id)
	h.Port = srv2.Port
	moved, _ := s.store.AddHost(h, sec)
	if _, err := s.HostInspect(moved.ID); err == nil || !strings.Contains(err.Error(), "主机密钥") {
		t.Fatalf("changed host key must be refused, got %v", err)
	}
}
