package store

import (
	"datakit-manager/internal/secret"
	"os"
	"path/filepath"
	"testing"
)

const kubeconfig = `apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: c1
  cluster: {server: "https://10.0.0.1:6443", certificate-authority: ca.crt}
- name: c2
  cluster: {server: "https://10.0.0.2:6443", insecure-skip-tls-verify: true}
users:
- name: u1
  user: {token: abc}
contexts:
- name: prod
  context: {cluster: c1, user: u1}
- name: test
  context: {cluster: c2, user: u1}
`

func TestImport(t *testing.T) {
	src := t.TempDir()
	path := filepath.Join(src, "config")
	_ = os.WriteFile(path, []byte(kubeconfig), 0o600)
	_ = os.WriteFile(filepath.Join(src, "ca.crt"), []byte("CERT"), 0o600)

	ctxs, err := ParseKubeconfig(path)
	if err != nil || len(ctxs) != 2 || !ctxs[0].Current || ctxs[0].Server != "https://10.0.0.1:6443" {
		t.Fatalf("%v %+v", err, ctxs)
	}

	s, _ := NewAt(t.TempDir())
	added, err := s.Import(path, nil, "")
	if err != nil || len(added) != 2 || added[0].Namespace != DefaultNamespace {
		t.Fatalf("%v %+v", err, added)
	}
	// the copy is self-contained: relative cert file inlined, single context
	b, err := s.Kubeconfig(added[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	one, err := ParseKubeconfigBytes(b)
	if err != nil || len(one) != 1 || one[0].Name != "prod" {
		t.Fatalf("%v %+v", err, one)
	}
	if !contains(string(b), "certificate-authority-data") {
		t.Fatalf("cert not inlined:\n%s", b)
	}
	// on disk the kubeconfig is encrypted: no token, no server address
	disk, _ := os.ReadFile(s.kubeconfigFile(added[0].ID))
	if contains(string(disk), "abc") || contains(string(disk), "10.0.0.1") || !secret.Sealed(disk) {
		t.Fatal("kubeconfig must be encrypted at rest")
	}

	again, _ := s.Import(path, []string{"prod"}, "monitoring")
	if again[0].Name != "prod (2)" || again[0].Namespace != "monitoring" {
		t.Fatalf("%+v", again)
	}
	if _, err := s.Update(again[0].ID, "生产", "dk"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(added[1].ID); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if len(list) != 2 {
		t.Fatalf("%+v", list)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestMigrationAndHosts(t *testing.T) {
	dir := t.TempDir()
	// a plaintext kubeconfig left by an older version gets encrypted on open
	_ = os.MkdirAll(filepath.Join(dir, "kubeconfigs"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "kubeconfigs", "old1.yaml"), []byte(kubeconfig), 0o600)
	s, err := NewAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kubeconfigs", "old1.yaml")); !os.IsNotExist(err) {
		t.Fatal("plaintext copy must be removed")
	}
	if b, err := s.Kubeconfig("old1"); err != nil || string(b) != kubeconfig {
		t.Fatalf("%v", err)
	}

	h, err := s.AddHost(Host{Name: "web-1", Address: "10.0.0.5", Port: 22, User: "root", HostKey: "ssh-ed25519 AAAA"}, HostSecret{PrivateKey: "PRIVATE-KEY-BODY", Passphrase: "pp"})
	if err != nil || h.ID == "" {
		t.Fatal(err)
	}
	h2, _ := s.AddHost(Host{Name: "web-1", Address: "10.0.0.6", Port: 22, User: "root"}, HostSecret{PrivateKey: "K2"})
	if h2.Name != "web-1 (2)" {
		t.Fatalf("%+v", h2)
	}
	disk, _ := os.ReadFile(s.hostKeyFile(h.ID))
	if contains(string(disk), "PRIVATE-KEY-BODY") || contains(string(disk), "pp\"") {
		t.Fatal("ssh key must be encrypted at rest")
	}
	meta, _ := os.ReadFile(filepath.Join(dir, "hosts.json"))
	if contains(string(meta), "PRIVATE") {
		t.Fatal("hosts.json must not hold the key")
	}
	// reopening (same secret.key) still decrypts
	s2, _ := NewAt(dir)
	got, sec, err := s2.GetHost(h.ID)
	if err != nil || got.Address != "10.0.0.5" || sec.PrivateKey != "PRIVATE-KEY-BODY" || sec.Passphrase != "pp" {
		t.Fatalf("%v %+v", err, got)
	}
	if err := s2.RemoveHost(h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s2.hostKeyFile(h.ID)); !os.IsNotExist(err) {
		t.Fatal("key file must be removed with the host")
	}
	list, _ := s2.ListHosts()
	if len(list) != 1 {
		t.Fatalf("%+v", list)
	}
}
