package main

import (
	"context"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"datakit-manager/frontend"
	"datakit-manager/internal/app"
)

// version is set by build.sh through -ldflags.
var version = "dev"

// Desktop holds the few functions that only make sense in the desktop app.
// JS: window.go.main.Desktop.<Method>
type Desktop struct {
	ctx context.Context
	svc *app.Service
}

func (d *Desktop) startup(ctx context.Context) { d.ctx = ctx }

// PickKubeconfig opens a file dialog and returns the chosen path ("" if cancelled).
func (d *Desktop) PickKubeconfig() (string, error) {
	home, _ := os.UserHomeDir()
	return runtime.OpenFileDialog(d.ctx, runtime.OpenDialogOptions{
		Title:            "选择 kubeconfig 文件",
		DefaultDirectory: home,
		ShowHiddenFiles:  true,
	})
}

// PickSSHKey opens a file dialog on ~/.ssh and returns the chosen private key path.
func (d *Desktop) PickSSHKey() (string, error) {
	home, _ := os.UserHomeDir()
	dir := home
	if st, err := os.Stat(home + "/.ssh"); err == nil && st.IsDir() {
		dir = home + "/.ssh"
	}
	return runtime.OpenFileDialog(d.ctx, runtime.OpenDialogOptions{
		Title:            "选择 SSH 私钥文件",
		DefaultDirectory: dir,
		ShowHiddenFiles:  true,
	})
}

// OpenDataDir reveals the data directory in the system file browser.
func (d *Desktop) OpenDataDir() { runtime.BrowserOpenURL(d.ctx, "file://"+d.svc.DataDir()) }

func main() {
	svc, err := app.New("", version, false)
	if err != nil {
		log.Fatal(err)
	}
	desktop := &Desktop{svc: svc}

	// Windows only: make sure a WebView2 runtime is available (offline package support).
	browserPath := prepareWebView2()

	err = wails.Run(&options.App{
		Title:     "DataKit Manager",
		Width:     1400,
		Height:    880,
		MinWidth:  1100,
		MinHeight: 680,
		AssetServer: &assetserver.Options{
			Assets: frontend.Dist(),
		},
		BackgroundColour: &options.RGBA{R: 245, G: 246, B: 248, A: 1},
		OnStartup:        desktop.startup,
		// JS: window.go.app.Service.* and window.go.main.Desktop.*
		Bind: []interface{}{svc, desktop},
		Windows: &windows.Options{
			WebviewBrowserPath: browserPath,
		},
		Mac: &mac.Options{
			About: &mac.AboutInfo{
				Title:   "DataKit Manager",
				Message: "多集群 DataKit 配置管理工具\n版本 " + version,
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
