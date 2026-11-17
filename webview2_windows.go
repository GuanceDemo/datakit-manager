//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

// prepareWebView2 makes the app usable on machines without internet access.
//
//  1. A "WebView2Runtime" folder next to the exe (Microsoft's Fixed Version runtime) is used
//     as-is: nothing gets installed. Its path is returned for Wails.
//  2. Otherwise, if no WebView2 runtime is installed and the offline package shipped
//     "WebView2\MicrosoftEdgeWebView2RuntimeInstaller*.exe" (Microsoft's Evergreen Standalone
//     Installer), it is run once, silently, before the window is created.
//  3. Otherwise Wails falls back to its embedded online bootstrapper.
func prepareWebView2() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)

	fixed := filepath.Join(dir, "WebView2Runtime")
	if st, err := os.Stat(filepath.Join(fixed, "msedgewebview2.exe")); err == nil && !st.IsDir() {
		return fixed
	}

	if v, err := webviewloader.GetAvailableCoreWebView2BrowserVersionString(""); err == nil && v != "" {
		return "" // already installed
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "WebView2", "MicrosoftEdgeWebView2RuntimeInstaller*.exe"))
	if len(matches) == 0 {
		return ""
	}

	msgBox("DataKit Manager", "首次运行需要安装 Microsoft Edge WebView2 运行时（已包含在安装包内，无需联网）。\n\n点击“确定”开始安装，大约需要 1～2 分钟，完成后程序会自动打开。", windows.MB_OK|windows.MB_ICONINFORMATION)
	cmd := exec.Command(matches[0], "/silent", "/install")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		msgBox("DataKit Manager", "WebView2 运行时安装失败："+err.Error()+"\n\n请手动运行 WebView2 目录下的安装程序后重新打开。", windows.MB_OK|windows.MB_ICONERROR)
		os.Exit(1)
	}
	return ""
}

func msgBox(title, text string, flags uint32) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	_, _ = windows.MessageBox(0, m, t, flags)
}
