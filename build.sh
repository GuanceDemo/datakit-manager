#!/usr/bin/env bash
# DataKit Manager 打包脚本：准备编译环境，并打包成桌面应用或 Web 容器镜像。
#
#   ./build.sh                  # 当前系统对应的桌面包（macOS 上 = mac）
#   ./build.sh mac              # macOS 通用包（Intel + Apple Silicon），只能在 macOS 上构建
#   ./build.sh windows          # Windows x64（Win11 / 已装 WebView2 的 Win10 直接运行）
#   ./build.sh windows-offline  # Windows x64 纯离线包：内含 WebView2 离线安装器，Win10 断网可用
#   ./build.sh windows-arm64    # Windows ARM64
#   ./build.sh web              # Web 版：linux/amd64 + linux/arm64 容器镜像 tar 和可执行文件
#   ./build.sh all              # mac + windows + windows-offline + web
#   ./build.sh clean            # 删除构建产物和本地工具链
#
# 环境变量：
#   VERSION=1.1.0              应用版本号（默认读取 wails.json）
#   CN_MIRROR=1|0              使用国内镜像下载 Go 模块 / npm 包 / 工具链（默认 1）
#   WEB_ARCH="amd64 arm64"     web 目标要构建的架构
#   WEB_IMAGE=datakit-manager  web 镜像名（标签为版本号）
#   WEBVIEW2_FIXED_DIR=<目录>  Windows 包内附带 WebView2 固定版本运行时（免安装，见 README）
#   SKIP_TESTS=1               跳过后端单元测试
#
# 系统里没有合适版本的 Go / Node.js 时，脚本会把它们下载到项目内的 .toolchain/ 目录，
# 不改动系统环境，也不需要 sudo。兼容 macOS 自带的 bash 3.2。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

TOOLS="$ROOT/.toolchain"
DIST="$ROOT/dist"
APP_NAME="DataKit Manager"
BIN_NAME="datakit-manager"
CN_MIRROR="${CN_MIRROR:-1}"
NODE_VERSION="${NODE_VERSION:-22.12.0}"   # 仅在系统没有 Node >= 18 时下载
MIN_NODE_MAJOR=18

c_info=$'\033[1;34m'; c_ok=$'\033[1;32m'; c_warn=$'\033[1;33m'; c_err=$'\033[1;31m'; c_off=$'\033[0m'
info() { printf '%s==>%s %s\n' "$c_info" "$c_off" "$*"; }
ok()   { printf '%s ✓ %s %s\n' "$c_ok" "$c_off" "$*"; }
warn() { printf '%s !  %s %s\n' "$c_warn" "$c_off" "$*" >&2; }
die()  { printf '%s ✗  %s %s\n' "$c_err" "$c_off" "$*" >&2; exit 1; }

# ---------------------------------------------------------------- host
case "$(uname -s)" in
  Darwin) HOST_OS=darwin ;;
  Linux)  HOST_OS=linux ;;
  *) die "不支持在 $(uname -s) 上运行本脚本（Windows 上请使用 WSL，或在 macOS / Linux 上交叉编译）" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  HOST_ARCH=amd64; NODE_ARCH=x64 ;;
  arm64|aarch64) HOST_ARCH=arm64; NODE_ARCH=arm64 ;;
  *) die "不支持的 CPU 架构：$(uname -m)" ;;
esac

# 版本号：go.mod 决定 Go 和 Wails 的版本，wails.json 决定应用版本
GO_VERSION="$(awk '$1=="go"{print $2; exit}' go.mod)"
WAILS_VERSION="$(awk '$1=="github.com/wailsapp/wails/v2"{print $2; exit}' go.mod)"
[ -n "$GO_VERSION" ] && [ -n "$WAILS_VERSION" ] || die "无法从 go.mod 读取 Go / Wails 版本"
case "$GO_VERSION" in *.*.*) ;; *) GO_VERSION="$GO_VERSION.0" ;; esac
VERSION="${VERSION:-$(sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' wails.json | head -1)}"
VERSION="${VERSION:-1.0.0}"

# ---------------------------------------------------------------- helpers
# ver_ge A B  => A >= B（按数字逐段比较）
ver_ge() {
  local a="$1" b="$2" i x y
  for i in 1 2 3; do
    x="$(printf '%s' "$a" | cut -d. -f"$i" | sed 's/[^0-9].*//')"; y="$(printf '%s' "$b" | cut -d. -f"$i" | sed 's/[^0-9].*//')"
    x="${x:-0}"; y="${y:-0}"
    if [ "$x" -gt "$y" ]; then return 0; fi
    if [ "$x" -lt "$y" ]; then return 1; fi
  done
  return 0
}

# download URL... 依次尝试，成功即返回
download() {
  local out="$1"; shift
  local url
  for url in "$@"; do
    info "下载 $url"
    if curl -fL --retry 3 --connect-timeout 15 --progress-bar -o "$out.part" "$url"; then
      mv "$out.part" "$out"; return 0
    fi
    warn "下载失败，尝试下一个地址"
  done
  rm -f "$out.part"
  return 1
}

# ---------------------------------------------------------------- toolchain
setup_mirrors() {
  if [ "$CN_MIRROR" = "1" ]; then
    export GOPROXY="${GOPROXY:-https://goproxy.cn,https://proxy.golang.org,direct}"
    export GOSUMDB="${GOSUMDB:-sum.golang.google.cn}"
    export npm_config_registry="${npm_config_registry:-https://registry.npmmirror.com}"
  fi
  # 版本由脚本保证，不让 go 命令再自行下载别的工具链
  export GOTOOLCHAIN=local
}

ensure_go() {
  local have=""
  if command -v go >/dev/null 2>&1; then
    have="$(GOTOOLCHAIN=local go env GOVERSION 2>/dev/null | sed 's/^go//')"
  fi
  if [ -n "$have" ] && ver_ge "$have" "$GO_VERSION"; then
    ok "Go ${have}（系统）"; return
  fi
  if [ -x "$TOOLS/go/bin/go" ] && ver_ge "$("$TOOLS/go/bin/go" env GOVERSION | sed 's/^go//')" "$GO_VERSION"; then
    export PATH="$TOOLS/go/bin:$PATH"; export GOROOT="$TOOLS/go"
    ok "Go ${GO_VERSION}（.toolchain）"; return
  fi
  [ -n "$have" ] && warn "系统 Go $have 低于项目要求的 ${GO_VERSION}，改用项目内工具链"
  local file="go$GO_VERSION.$HOST_OS-$HOST_ARCH.tar.gz"
  mkdir -p "$TOOLS"
  if [ "$CN_MIRROR" = "1" ]; then
    download "$TOOLS/$file" "https://mirrors.aliyun.com/golang/$file" "https://golang.google.cn/dl/$file" "https://go.dev/dl/$file" || die "下载 Go 失败"
  else
    download "$TOOLS/$file" "https://go.dev/dl/$file" "https://golang.google.cn/dl/$file" || die "下载 Go 失败"
  fi
  rm -rf "$TOOLS/go"
  tar -xzf "$TOOLS/$file" -C "$TOOLS" && rm -f "$TOOLS/$file"
  export PATH="$TOOLS/go/bin:$PATH"; export GOROOT="$TOOLS/go"
  ok "Go $GO_VERSION 已安装到 .toolchain/go"
}

ensure_node() {
  local major=""
  if command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then
    major="$(node -v | sed 's/^v//' | cut -d. -f1)"
  fi
  if [ -n "$major" ] && [ "$major" -ge "$MIN_NODE_MAJOR" ]; then
    ok "Node.js $(node -v)（系统）"; return
  fi
  if [ -x "$TOOLS/node/bin/node" ]; then
    export PATH="$TOOLS/node/bin:$PATH"
    ok "Node.js $(node -v)（.toolchain）"; return
  fi
  [ -n "$major" ] && warn "系统 Node.js v$major 低于要求的 v${MIN_NODE_MAJOR}，改用项目内工具链"
  local name="node-v$NODE_VERSION-$HOST_OS-$NODE_ARCH"
  mkdir -p "$TOOLS"
  if [ "$CN_MIRROR" = "1" ]; then
    download "$TOOLS/$name.tar.gz" "https://npmmirror.com/mirrors/node/v$NODE_VERSION/$name.tar.gz" "https://nodejs.org/dist/v$NODE_VERSION/$name.tar.gz" || die "下载 Node.js 失败"
  else
    download "$TOOLS/$name.tar.gz" "https://nodejs.org/dist/v$NODE_VERSION/$name.tar.gz" || die "下载 Node.js 失败"
  fi
  rm -rf "$TOOLS/node" "$TOOLS/$name"
  tar -xzf "$TOOLS/$name.tar.gz" -C "$TOOLS" && rm -f "$TOOLS/$name.tar.gz"
  mv "$TOOLS/$name" "$TOOLS/node"
  export PATH="$TOOLS/node/bin:$PATH"
  ok "Node.js v$NODE_VERSION 已安装到 .toolchain/node"
}

ensure_wails() {
  export PATH="$TOOLS/bin:$PATH"
  local want="${WAILS_VERSION#v}" have=""
  if [ -x "$TOOLS/bin/wails" ]; then
    have="$("$TOOLS/bin/wails" version 2>/dev/null | head -1 | tr -d 'v[:space:]')"
  fi
  if [ "$have" = "$want" ]; then
    ok "Wails CLI v$have"; return
  fi
  info "安装 Wails CLI ${WAILS_VERSION}（与 go.mod 一致）"
  mkdir -p "$TOOLS/bin"
  GOBIN="$TOOLS/bin" GOFLAGS="" go install "github.com/wailsapp/wails/v2/cmd/wails@$WAILS_VERSION" || die "安装 Wails CLI 失败"
  ok "Wails CLI $WAILS_VERSION"
}

ensure_mac_sdk() {
  xcode-select -p >/dev/null 2>&1 || die "缺少 Xcode Command Line Tools，请先执行：xcode-select --install（装完后重新运行本脚本）"
  ok "Xcode Command Line Tools"
}

prepare() {
  info "准备编译环境（主机 $HOST_OS/${HOST_ARCH}，应用版本 ${VERSION}）"
  command -v curl >/dev/null 2>&1 || die "需要 curl"
  setup_mirrors
  ensure_go
  ensure_node
  ensure_wails
  if [ "${SKIP_TESTS:-0}" != "1" ]; then
    info "运行后端单元测试"
    go test ./internal/... || die "单元测试失败"
  fi
}

# ---------------------------------------------------------------- build
WAILS_COMMON=(-clean -trimpath -skipbindings -m)

build_mac() {
  [ "$HOST_OS" = "darwin" ] || die "macOS 包只能在 macOS 上构建（需要系统 SDK 和 WebKit 框架）"
  ensure_mac_sdk
  info "构建 macOS 通用包（Intel + Apple Silicon）"
  wails build "${WAILS_COMMON[@]}" -platform darwin/universal -ldflags "-X main.version=$VERSION"

  local app="$ROOT/build/bin/$APP_NAME.app"
  [ -d "$app" ] || die "没有生成 $app"
  # 本地（ad-hoc）签名：Apple Silicon 要求可执行文件必须有签名才能运行
  codesign --force --deep --sign - "$app" >/dev/null 2>&1 || warn "ad-hoc 签名失败，应用在 Apple Silicon 上可能无法启动"

  mkdir -p "$DIST"
  local zip="$DIST/DataKit-Manager-$VERSION-macos-universal.zip"
  rm -f "$zip"
  ditto -c -k --sequesterRsrc --keepParent "$app" "$zip"
  ok "macOS：$zip"
  echo "     解压后把“$APP_NAME.app”拖到“应用程序”。首次打开如被拦截：右键 → 打开，或执行"
  echo "     xattr -dr com.apple.quarantine \"/Applications/$APP_NAME.app\""
}

# 微软官方 Evergreen Standalone Installer（离线安装器）的固定下载链接
webview2_installer() {
  local arch="$1" name url
  case "$arch" in
    amd64) name="MicrosoftEdgeWebView2RuntimeInstallerX64.exe";   url="https://go.microsoft.com/fwlink/?linkid=2124701" ;;
    arm64) name="MicrosoftEdgeWebView2RuntimeInstallerARM64.exe"; url="https://go.microsoft.com/fwlink/?linkid=2099616" ;;
  esac
  mkdir -p "$TOOLS/cache"
  if [ ! -s "$TOOLS/cache/$name" ]; then
    download "$TOOLS/cache/$name" "$url" >&2 || die "下载 WebView2 离线安装器失败，可手动下载后放到 .toolchain/cache/$name"
  fi
  # 粗略校验：离线安装器约 200MB，小于 50MB 说明下到的不是它
  local size; size="$(wc -c < "$TOOLS/cache/$name" | tr -d ' ')"
  [ "$size" -gt 50000000 ] || { rm -f "$TOOLS/cache/$name"; die "WebView2 离线安装器大小异常（$size 字节），请重试"; }
  printf '%s' "$TOOLS/cache/$name"
}

build_windows() {
  local arch="$1" offline="${2:-}"
  info "构建 Windows $arch${offline:+（纯离线包）}"
  wails build "${WAILS_COMMON[@]}" -platform "windows/$arch" -webview2 embed -ldflags "-X main.version=$VERSION"

  local exe="$ROOT/build/bin/$BIN_NAME.exe"
  [ -f "$exe" ] || die "没有生成 $exe"
  local label="x64"; [ "$arch" = "arm64" ] && label="arm64"
  [ -n "$offline" ] && label="$label-offline"
  local stage="$DIST/DataKit-Manager-$VERSION-windows-$label"
  rm -rf "$stage" "$stage.zip"
  mkdir -p "$stage"
  cp "$exe" "$stage/DataKit Manager.exe"

  if [ -n "${WEBVIEW2_FIXED_DIR:-}" ]; then
    [ -f "$WEBVIEW2_FIXED_DIR/msedgewebview2.exe" ] || die "WEBVIEW2_FIXED_DIR 目录下没有 msedgewebview2.exe：$WEBVIEW2_FIXED_DIR"
    info "附带 WebView2 固定版本运行时"
    cp -R "$WEBVIEW2_FIXED_DIR" "$stage/WebView2Runtime"
  fi

  if [ -n "$offline" ] && [ -z "${WEBVIEW2_FIXED_DIR:-}" ]; then
    local installer; installer="$(webview2_installer "$arch")"
    mkdir -p "$stage/WebView2"
    cp "$installer" "$stage/WebView2/"
    # UTF-8 BOM，Windows 记事本才能正确显示中文
    printf '\357\273\277' > "$stage/README.txt"
    printf '%s\r\n' \
      "DataKit Manager 离线包" "" \
      "直接运行 DataKit Manager.exe 即可，全程不需要联网。" "" \
      "如果这台电脑没有 Microsoft Edge WebView2 运行时（部分 Windows 10），首次启动时程序会" \
      "自动运行 WebView2 目录里的离线安装器（约 1～2 分钟，可能弹出一次管理员确认），" \
      "装好后自动打开。也可以先手动双击 WebView2 目录下的安装程序。" "" \
      "请保持 WebView2 目录与 exe 在同一目录下。" >> "$stage/README.txt"
  fi

  if command -v zip >/dev/null 2>&1; then
    (cd "$DIST" && zip -qr "$(basename "$stage").zip" "$(basename "$stage")")
  elif [ "$HOST_OS" = "darwin" ]; then
    ditto -c -k --keepParent "$stage" "$stage.zip"
  else
    warn "没有 zip 命令，跳过压缩（目录已生成）"
  fi
  [ -f "$stage.zip" ] && ok "Windows：$stage.zip" || ok "Windows：$stage/"
  if [ -z "$offline" ] && [ -z "${WEBVIEW2_FIXED_DIR:-}" ]; then
    echo "     Windows 11 及已安装 Edge WebView2 的 Windows 10 可直接运行；缺少 WebView2 时程序会"
    echo "     联网安装。断网的 Windows 10 请用 ./build.sh windows-offline 打纯离线包。"
  fi
}

build_frontend() {
  info "构建前端"
  (cd "$ROOT/frontend" && npm install --no-audit --no-fund --loglevel=error && npm run build) || die "前端构建失败"
}

# Web 版：静态链接的 Linux 可执行文件 + 容器镜像 tar（不依赖 Docker 生成）
build_web() {
  build_frontend
  local arch out image="${WEB_IMAGE:-datakit-manager}"
  mkdir -p "$DIST"
  for arch in ${WEB_ARCH:-amd64 arm64}; do
    info "构建 Web 版 linux/$arch"
    out="$ROOT/build/bin/web/linux-$arch/datakit-manager-server"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$out" ./cmd/server || die "编译失败"
    local tarball="$DIST/datakit-manager-web-$VERSION-linux-$arch.tar"
    go run ./tools/mkimage -binary "$out" -arch "$arch" -tag "$image:$VERSION" -out "$tarball" || die "生成镜像失败"
    cp "$out" "$DIST/datakit-manager-server-$VERSION-linux-$arch"
    ok "镜像：$tarball"
  done
  cp "$ROOT/deploy/datakit-manager-web.yaml" "$DIST/datakit-manager-web.yaml"
  echo "     导入镜像：docker load -i <tar>   或   ctr -n k8s.io images import <tar>"
  echo "     运行：    docker run -d -p 8080:8080 -e DKM_PASSWORD=<密码> -v dkm-data:/data $image:$VERSION"
  echo "     K8s 部署：修改 dist/datakit-manager-web.yaml 里的镜像地址和密码后 kubectl apply -f"
}

clean() {
  info "清理构建产物"
  rm -rf "$ROOT/build/bin" "$DIST" "$ROOT/frontend/dist" "$ROOT/frontend/node_modules" "$ROOT/frontend/wailsjs" "$TOOLS"
  ok "已清理"
}

# ---------------------------------------------------------------- main
TARGET="${1:-}"
if [ -z "$TARGET" ]; then
  if [ "$HOST_OS" = "darwin" ]; then TARGET=mac; else TARGET=windows; fi
fi

case "$TARGET" in
  mac|macos|darwin) prepare; build_mac ;;
  windows|win)      prepare; build_windows amd64 ;;
  windows-offline)  prepare; build_windows amd64 offline ;;
  windows-arm64)    prepare; build_windows arm64 ;;
  windows-arm64-offline) prepare; build_windows arm64 offline ;;
  web)              prepare; build_web ;;
  all)              prepare; build_web; build_windows amd64; build_windows amd64 offline; build_mac ;;
  clean)            clean ;;
  -h|--help|help)   sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//' ;;
  *) die "未知目标：${TARGET}（可用：mac / windows / windows-offline / windows-arm64 / web / all / clean）" ;;
esac
