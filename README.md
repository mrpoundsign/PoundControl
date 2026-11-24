# PoundControl

**Cross-Platform Macro Deck, OBS Studio Controller & Extensible Plugin Studio (Go & Fyne)**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg?style=for-the-badge)](LICENSE)

**PoundControl** is an open-source, cross-platform desktop and LAN touch macro deck written in **Go** and **Fyne v2**. It pairs a clickable **Virtual Desktop Deck** (with pop-out pad and global hotkeys) and a **LAN Web / Mobile / Tablet Touch Deck** with a three-pillar plugin architecture:

- **Built-in Native Go Plugins**: Core navigation (pages, folders, profiles, multi-actions), **OBS Studio (WebSocket v5)** control, and **App & System Control** (app/script launcher, open URL, HTTP/WS webhooks, hotkeys/macros, media keys).
- **Elgato Stream Deck (`.sdPlugin`) Compatibility**: Load standard SDK v2/v3 `.sdPlugin` bundles (Node.js or native binaries, including TownCrier's plugin) with a WebView2 / browser Property Inspector bridge.
- **Dynamic WebAssembly (`wazero`) Plugins**: Run single-file, sandboxed, hot-reloadable `.wasm` plugins compiled from Go, TinyGo, Rust, Zig, or C/C++ across Windows, Linux, and macOS (see [docs/WASM_PLUGIN_ABI.md](docs/WASM_PLUGIN_ABI.md)).

---

## Quick Start & Build Commands

### Windows (PowerShell with Zig CGo)

```powershell
.\build.ps1                # Run lint, unit/UI tests, and build GUI -> build\bin\poundcontrol.exe
.\build.ps1 -Target gui    # Build GUI only -> build\bin\poundcontrol.exe
.\build.ps1 -Target test   # Run all unit & headless UI tests
.\build.ps1 -Target lint   # Run go fix -diff and golangci-lint-v2
.\build.ps1 -Target check  # Validate GoReleaser configuration
.\build.ps1 -Target clean  # Clean build artifacts
```

### Linux / macOS / WSL (Bash)

```bash
./build.sh                 # Run lint, tests, and build GUI -> build/bin/poundcontrol
./build.sh gui             # Build native GUI -> build/bin/poundcontrol
./build.sh gui-win         # Cross-compile Windows GUI via MinGW -> build/bin/poundcontrol.exe
./build.sh test            # Run all tests
./build.sh lint            # Run go fix -diff and golangci-lint-v2
./build.sh clean           # Clean build artifacts
```

---

## Documentation

- [WebAssembly Plugin ABI Specification](docs/WASM_PLUGIN_ABI.md) — Guest exports, `poundcontrol` host imports, sandbox limits, and declarative `info` / `link` / `button` / `password` settings schema.
- [Architecture & Design Overview](DESIGN.md) — High-level architecture and module layout.

---

## License

MIT — see [LICENSE](LICENSE).
