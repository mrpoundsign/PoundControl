#!/usr/bin/env bash
set -euo pipefail

# PoundControl Build Script (Bash)
# Usage:
#   ./build.sh           # Run lint, tests, and build Desktop Studio GUI
#   ./build.sh gui       # Build native Desktop Studio GUI into build/bin/poundcontrol
#   ./build.sh gui-win   # Cross-compile Windows Desktop Studio GUI via MinGW into build/bin/poundcontrol.exe
#   ./build.sh installer # Cross-compile Windows Guided Setup Installer via MinGW into build/bin/PoundControl-Setup.exe
#   ./build.sh wasm      # Build example WebAssembly plugins (WASI reactor) into build/bin/*.wasm
#   ./build.sh test      # Run unit & headless UI tests
#   ./build.sh lint      # Run go fix -diff & golangci-lint-v2
#   ./build.sh clean     # Clean build outputs

TARGET="${1:-all}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_DIR="$ROOT_DIR/build"
BIN_DIR="$BUILD_DIR/bin"
mkdir -p "$BUILD_DIR" "$BIN_DIR"

if ! command -v go &>/dev/null; then
    if [ -x "/usr/local/go/bin/go" ]; then
        export PATH="/usr/local/go/bin:$PATH"
    elif [ -x "$HOME/go/bin/go" ]; then
        export PATH="$HOME/go/bin:$PATH"
    fi
fi
if [ -d "$HOME/go/bin" ]; then
    export PATH="$PATH:$HOME/go/bin"
fi

run_lint() {
    echo "==> Verifying code modernizations (go fix -diff)..."
    local DIFF
    DIFF="$(go fix -diff ./cmd/... ./internal/... ./pkg/...)"
    if [ -n "$DIFF" ]; then
        echo "$DIFF"
        echo "Error: Unfixed code modernization diffs found. Run 'go fix ./cmd/... ./internal/... ./pkg/...' to apply them."
        exit 1
    fi
    echo "✓ Code modernizations verified!"

    echo "==> Running golangci-lint-v2..."
    if command -v golangci-lint-v2 &>/dev/null; then
        golangci-lint-v2 run ./cmd/... ./internal/... ./pkg/...
    else
        echo "Error: golangci-lint-v2 not found in PATH"
        exit 1
    fi
    echo "✓ Lint passed!"
}

generate_winres_syso() {
    local OUT_PREFIX="$1"
    local PRODUCT_NAME="$2"
    local DESCRIPTION="$3"
    local ORIG_FILENAME="$4"
    local ICON_PATH="$ROOT_DIR/assets/icon.png"
    if [ ! -f "$ICON_PATH" ]; then
        return 0
    fi
    local VER="${VERSION:-0.1.0}"
    VER="${VER#v}"
    local FILE_VER="${VER%%-*}"
    go tool go-winres simply \
        --arch amd64 \
        --out "$OUT_PREFIX" \
        --icon "$ICON_PATH" \
        --manifest gui \
        --product-name "$PRODUCT_NAME" \
        --file-description "$DESCRIPTION" \
        --product-version "$VER" \
        --file-version "$FILE_VER" \
        --original-filename "$ORIG_FILENAME"
}

build_gui() {
    echo "==> Building poundcontrol Desktop Studio GUI..."
    local VER_LDFLAG=""
    if [ -n "${VERSION:-}" ]; then
        VER_LDFLAG=" -X github.com/mrpoundsign/PoundControl/internal/version.Version=${VERSION#v}"
    fi
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w${VER_LDFLAG}" -o "$BIN_DIR/poundcontrol" ./cmd/poundcontrol
    echo "✓ Successfully built $BIN_DIR/poundcontrol"
}

build_gui_win() {
    echo "==> Building poundcontrol Desktop Studio GUI for Windows (via MinGW)..."
    local SYSO_PREFIX="$ROOT_DIR/cmd/poundcontrol/rsrc"
    trap 'rm -f "${SYSO_PREFIX}_windows_amd64.syso"' RETURN
    generate_winres_syso "$SYSO_PREFIX" "PoundControl" "PoundControl Macro & Stream Deck Studio" "poundcontrol.exe"
    local VER_LDFLAG=""
    if [ -n "${VERSION:-}" ]; then
        VER_LDFLAG=" -X github.com/mrpoundsign/PoundControl/internal/version.Version=${VERSION#v}"
    fi
    CC=x86_64-w64-mingw32-gcc CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
        go build -trimpath -ldflags="-H windowsgui -s -w${VER_LDFLAG} -extldflags '-static-libgcc'" -o "$BIN_DIR/poundcontrol.exe" ./cmd/poundcontrol
    echo "✓ Successfully built $BIN_DIR/poundcontrol.exe"

    if command -v x86_64-w64-mingw32-objdump &>/dev/null; then
        echo "==> Verifying poundcontrol.exe DLL Dependencies..."
        x86_64-w64-mingw32-objdump -p "$BIN_DIR/poundcontrol.exe" | grep "DLL Name"
    fi
}

clean_installer_payload() {
    local PAYLOAD_DIR="$ROOT_DIR/cmd/installer/payload"
    mkdir -p "$PAYLOAD_DIR"
    find "$PAYLOAD_DIR" -mindepth 1 ! -name 'placeholder.txt' -exec rm -rf {} + 2>/dev/null || true
    touch "$PAYLOAD_DIR/placeholder.txt"
    rm -rf "$BUILD_DIR/installer_payload_stage_win"
    rm -f "$ROOT_DIR/cmd/installer/rsrc_windows_amd64.syso"
}

build_installer_win() {
    if [ ! -f "$BIN_DIR/poundcontrol.exe" ]; then
        build_gui_win
    fi

    echo "==> Packing compressed payload for PoundControl-Setup.exe..."
    local PAYLOAD_DIR="$ROOT_DIR/cmd/installer/payload"
    local STAGE_DIR="$BUILD_DIR/installer_payload_stage_win"
    clean_installer_payload
    trap 'clean_installer_payload' RETURN

    mkdir -p "$STAGE_DIR"
    cp -f "$BIN_DIR/poundcontrol.exe" "$STAGE_DIR/poundcontrol.exe"
    [ -f "$ROOT_DIR/LICENSE" ] && cp -f "$ROOT_DIR/LICENSE" "$STAGE_DIR/LICENSE"
    [ -f "$ROOT_DIR/README.md" ] && cp -f "$ROOT_DIR/README.md" "$STAGE_DIR/README.md"
    go run github.com/mrpoundsign/appinstaller/cmd/packpayload \
        -src "$STAGE_DIR" \
        -out "$PAYLOAD_DIR/payload.tar.zst"
    rm -rf "$STAGE_DIR"

    local VER_LDFLAG=""
    if [ -n "${VERSION:-}" ]; then
        VER_LDFLAG=" -X main.AppVersion=${VERSION#v}"
    fi

    generate_winres_syso "$ROOT_DIR/cmd/installer/rsrc" "PoundControl Setup" "PoundControl Guided Setup Installer" "PoundControl-Setup.exe"

    echo "==> Cross-compiling PoundControl-Setup.exe (via MinGW)..."
    CC=x86_64-w64-mingw32-gcc CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
        go build -trimpath -ldflags="-H windowsgui -s -w${VER_LDFLAG} -extldflags '-static-libgcc'" -o "$BIN_DIR/PoundControl-Setup.exe" ./cmd/installer
    echo "✓ Successfully built $BIN_DIR/PoundControl-Setup.exe"

    if command -v x86_64-w64-mingw32-objdump &>/dev/null; then
        echo "==> Verifying PoundControl-Setup.exe DLL Dependencies..."
        x86_64-w64-mingw32-objdump -p "$BIN_DIR/PoundControl-Setup.exe" | grep "DLL Name"
    fi
}

build_wasm() {
    echo "==> Building example WebAssembly plugins (WASI reactor)..."
    GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -buildmode=c-shared -o "$BIN_DIR/pomodoro.wasm" ./examples/wasm-pomodoro
    echo "✓ Successfully built $BIN_DIR/pomodoro.wasm"
}

run_tests() {
    echo "==> Running unit and headless UI tests..."
    go test -count=1 ./cmd/... ./internal/... ./pkg/...
    echo "==> Running race detector on concurrent/network packages..."
    go test -race -count=1 ./cmd/... ./internal/app/... ./internal/hotkey/... ./internal/webdeck/... ./pkg/builtin/... ./pkg/deck/... ./pkg/plugin/... ./pkg/pluginmgr/... ./pkg/sdplugin/... ./pkg/secrets/... ./pkg/tile/...
    echo "✓ All tests passed!"
}

clean_artifacts() {
    echo "==> Cleaning build artifacts..."
    rm -rf "$BUILD_DIR" poundcontrol poundcontrol.exe dist/
    mkdir -p "$BIN_DIR"
    echo "✓ Cleaned."
}

case "$TARGET" in
    all)
        run_lint
        run_tests
        build_gui
        ;;
    gui)
        build_gui
        ;;
    gui-win)
        build_gui_win
        ;;
    installer)
        build_installer_win
        ;;
    wasm)
        build_wasm
        ;;
    test)
        run_tests
        ;;
    lint)
        run_lint
        ;;
    clean)
        clean_artifacts
        ;;
    *)
        echo "Unknown target: $TARGET. Available: all, gui, gui-win, installer, wasm, test, lint, clean"
        exit 1
        ;;
esac
