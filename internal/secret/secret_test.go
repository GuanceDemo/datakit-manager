package secret

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestBox(t *testing.T) {
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "secret.key"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", err, st)
	}
	plain := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n")
	p := filepath.Join(dir, "k.enc")
	if err := b.WriteFile(p, plain); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if bytes.Contains(raw, []byte("OPENSSH")) || !Sealed(raw) {
		t.Fatal("file must not contain plaintext")
	}
	// same key after reopening; two seals of the same data differ (random nonce)
	b2, _ := Open(dir)
	got, err := b2.ReadFile(p)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("%v %q", err, got)
	}
	s1, _ := b.Seal(plain)
	s2, _ := b.Seal(plain)
	if bytes.Equal(s1, s2) {
		t.Fatal("nonce reuse")
	}
	// tampering and a wrong key are detected
	raw[len(raw)-1] ^= 1
	if _, err := b.Unseal(raw); err == nil {
		t.Fatal("tampered data must fail")
	}
	t.Setenv("DKM_SECRET_KEY", "another")
	b3, _ := Open(dir)
	if _, err := b3.Unseal(s1); err == nil || b3.Source != "env" {
		t.Fatal("wrong key must fail")
	}
}
