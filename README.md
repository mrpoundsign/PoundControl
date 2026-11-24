# PoundControl

**Cross-Platform Macro Deck, OBS Studio Controller & Extensible Plugin Studio (Go & Fyne)**

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg?style=for-the-badge)](LICENSE)

**PoundControl** is an open-source, cross-platform desktop, mobile, and watch macro deck written in **Go**, **Fyne v2**, and **Kotlin**. It pairs a clickable **Virtual Desktop Deck** (with pop-out pad and global hotkeys), a **LAN Web / Mobile / Tablet Touch Deck**, and native **Android & Wear OS** companion apps with a three-pillar plugin architecture:

- **Built-in Native Go Plugins**: Core navigation (pages, folders, profiles, multi-actions), **OBS Studio (WebSocket v5)** control, **Multi-Format Audio Soundboard** (WAV, MP3, OGG Vorbis, FLAC), and **App & System Control** (app/script launcher, open URL, HTTP/WS webhooks, hotkeys/macros, media keys).
- **Elgato Stream Deck (`.sdPlugin`) Compatibility**: Load standard SDK v2/v3 `.sdPlugin` bundles (Node.js or native binaries, including TownCrier's plugin) with a WebView2 / browser Property Inspector bridge and OpenAction Marketplace integration.
- **Dynamic WebAssembly (`wazero`) Plugins**: Run single-file, sandboxed, hot-reloadable `.wasm` plugins compiled from Go, TinyGo, Rust, Zig, or C/C++ across Windows, Linux, and macOS (see [docs/WASM_PLUGIN_ABI.md](docs/WASM_PLUGIN_ABI.md)) with Curated Catalog browsing and SHA-256 verified updates.

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

GNU Affero General Public License v3.0 (AGPL-3.0) — see [LICENSE](LICENSE).
