#!/usr/bin/env bash
set -euo pipefail

# PoundDeck Build Script (Bash)
# Usage:
#   ./build.sh           # Run lint, tests, and build Desktop Studio GUI
#   ./build.sh gui       # Build native Desktop Studio GUI into build/bin/pounddeck
#   ./build.sh gui-win   # Cross-compile Windows Desktop Studio GUI via MinGW into build/bin/pounddeck.exe
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
    DIFF="$(go fix -diff ./...)"
    if [ -n "$DIFF" ]; then
        echo "$DIFF"
        echo "Error: Unfixed code modernization diffs found. Run 'go fix ./...' to apply them."
        exit 1
    fi
    echo "✓ Code modernizations verified!"

    echo "==> Running golangci-lint-v2..."
    if command -v golangci-lint-v2 &>/dev/null; then
        golangci-lint-v2 run ./...
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
    go tool go-winres simply \
        --arch amd64 \
        --out "$OUT_PREFIX" \
        --icon "$ICON_PATH" \
        --manifest gui \
        --product-name "$PRODUCT_NAME" \
        --file-description "$DESCRIPTION" \
        --product-version "$VER" \
        --file-version "$VER" \
        --original-filename "$ORIG_FILENAME"
}

build_gui() {
    echo "==> Building pounddeck Desktop Studio GUI..."
    local VER_LDFLAG=""
    if [ -n "${VERSION:-}" ]; then
        VER_LDFLAG=" -X github.com/mrpoundsign/PoundDeck/internal/version.Version=${VERSION#v}"
    fi
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w${VER_LDFLAG}" -o "$BIN_DIR/pounddeck" ./cmd/pounddeck
    echo "✓ Successfully built $BIN_DIR/pounddeck"
}

build_gui_win() {
    echo "==> Building pounddeck Desktop Studio GUI for Windows (via MinGW)..."
    local SYSO_PREFIX="$ROOT_DIR/cmd/pounddeck/rsrc"
    trap 'rm -f "${SYSO_PREFIX}_windows_amd64.syso"' RETURN
    generate_winres_syso "$SYSO_PREFIX" "PoundDeck" "PoundDeck Macro & Stream Deck Studio" "pounddeck.exe"
    local VER_LDFLAG=""
    if [ -n "${VERSION:-}" ]; then
        VER_LDFLAG=" -X github.com/mrpoundsign/PoundDeck/internal/version.Version=${VERSION#v}"
    fi
    CC=x86_64-w64-mingw32-gcc CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
        go build -trimpath -ldflags="-H windowsgui -s -w${VER_LDFLAG}" -o "$BIN_DIR/pounddeck.exe" ./cmd/pounddeck
    echo "✓ Successfully built $BIN_DIR/pounddeck.exe"
}

build_wasm() {
    echo "==> Building example WebAssembly plugins (WASI reactor)..."
    GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -buildmode=c-shared -o "$BIN_DIR/pomodoro.wasm" ./examples/wasm-pomodoro
    echo "✓ Successfully built $BIN_DIR/pomodoro.wasm"
}

run_tests() {
    echo "==> Running unit and headless UI tests..."
    go test -count=1 -v ./cmd/... ./internal/... ./pkg/...
    echo "✓ All tests passed!"
}

clean_artifacts() {
    echo "==> Cleaning build artifacts..."
    rm -rf "$BUILD_DIR" pounddeck pounddeck.exe dist/
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
        echo "Unknown target: $TARGET. Available: all, gui, gui-win, wasm, test, lint, clean"
        exit 1
        ;;
esac
