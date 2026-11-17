package hostdk

import (
	"context"
	"datakit-manager/internal/hostdk/sshtest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealInstall installs the real DataKit on THIS machine through the SSH code path.
// It changes the system (writes /usr/local/datakit, registers a service), so it only runs
// when asked for, on a disposable Linux machine, as root, with internet access:
//
//	DKM_E2E=online  go test ./internal/hostdk -run RealInstall -v -timeout 20m
//	DKM_E2E=offline go test ./internal/hostdk -run RealInstall -v -timeout 20m
func TestRealInstall(t *testing.T) {
	mode := os.Getenv("DKM_E2E")
	if mode == "" {
		t.Skip("set DKM_E2E=online or offline to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	keyPEM, pub, _ := sshtest.GenKey(t)
	srv := sshtest.Start(t, pub)
	c, err := Dial(ctx, Target{Address: srv.Addr, Port: srv.Port, User: "root", PrivateKey: keyPEM})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Root {
		t.Skip("needs root")
	}
	out, err := c.Install(ctx, InstallOptions{DataWay: "https://openway.guance.com?token=tkn_e2e_placeholder", Offline: mode == "offline", DefInputs: "cpu,mem"}, t.TempDir())
	if len(out) > 1500 {
		out = out[len(out)-1500:]
	}
	t.Logf("installer output (tail):\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	info, err := c.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("installed=%v version=%s service=%s running=%v listen=%s defaults=%v confs=%d", info.Installed, info.Version, info.Service, info.Running, info.HTTPListen, info.DefaultInputs, len(info.ConfFiles))
	if !info.Installed || info.Version == "" || !strings.Contains(info.MainConf, "tkn_e2e_placeholder") {
		t.Fatalf("install not detected: %+v", info)
	}
}
