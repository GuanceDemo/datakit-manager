// Package sshtest is a minimal in-process SSH server for tests: public-key auth, and "exec"
// requests are run with the local /bin/sh. It lets the SSH client code be exercised end to
// end without a real sshd. Never use it outside tests.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os/exec"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Server is a running test server.
type Server struct {
	Addr    string
	Port    int
	HostKey ssh.Signer
}

// GenKey returns a fresh ed25519 key: OpenSSH PEM text, public key and signer.
func GenKey(t *testing.T) (pemText string, pub ssh.PublicKey, signer ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err = ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), signer.PublicKey(), signer
}

// Start runs a server on a random local port that accepts exactly one public key.
func Start(t *testing.T, allowed ssh.PublicKey) *Server {
	t.Helper()
	_, _, hostKey := GenKey(t)
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(allowed.Marshal()) {
				return nil, nil
			}
			return nil, io.EOF
		},
	}
	cfg.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(raw, cfg)
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return &Server{Addr: "127.0.0.1", Port: p, HostKey: hostKey}
}

func serve(raw net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(raw, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range creqs {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				_ = ssh.Unmarshal(req.Payload, &payload)
				_ = req.Reply(true, nil)
				cmd := exec.Command("/bin/sh", "-c", payload.Command)
				cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
				stdin, _ := cmd.StdinPipe()
				go func() { _, _ = io.Copy(stdin, ch); _ = stdin.Close() }()
				code := 0
				if err := cmd.Run(); err != nil {
					code = 1
					if ee, ok := err.(*exec.ExitError); ok {
						code = ee.ExitCode()
					}
				}
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
				return
			}
		}()
	}
}
