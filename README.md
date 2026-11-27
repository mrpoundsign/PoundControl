# PoundControl

**Cross-Platform Macro Deck, OBS Studio Controller & Extensible Plugin Studio**

[![Latest Release](https://img.shields.io/github/v/release/mrpoundsign/PoundControl?style=for-the-badge&color=38BDF8)](https://github.com/mrpoundsign/PoundControl/releases/latest)
[![Documentation](https://img.shields.io/badge/Docs-mrpoundsign.com-0284C7?style=for-the-badge)](https://mrpoundsign.com/PoundControl/)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg?style=for-the-badge)](LICENSE)

**PoundControl** is an open-source, local-first macro and stream deck for **Windows**, **macOS**, **Linux**, **LAN phones/tablets**, **Android**, and **Wear OS** watches. It pairs a clickable **Virtual Desktop Deck** (with pop-out pad and global hotkeys) and encrypted **LAN / Mobile / Watch Touch Decks** with built-in plugins (**Core**, **OBS Studio**, **Multi-Format Soundboard**, **Apps/Hotkeys/Webhooks**), **Elgato Stream Deck (`.sdPlugin`)** compatibility, and sandboxed **WebAssembly (`wazero`)** plugins.

---

## Download & Install

Grab the file for your device from the **[Latest GitHub Release](https://github.com/mrpoundsign/PoundControl/releases/latest)**:

### Windows (64-bit)
- **Guided Installer (Recommended)**: Download `PoundControl-v*-windows-amd64-setup.exe` and double-click to install with Start Menu / Desktop shortcuts, clean uninstall support, and automatic SHA-256 self-updates.
- **Portable Binary**: Download `PoundControl-v*-windows-amd64.exe` and run it directly from any folder — no installation needed.
- *First-launch note*: Because release binaries are unsigned with a commercial EV certificate, Windows SmartScreen may show *"Windows protected your PC"*. Click **More info → Run anyway** (you can also verify the file against `checksums.txt` or `gh attestation verify` below).

### macOS
1. Download `PoundControl-v*-darwin-arm64.zip` from the [Latest Release](https://github.com/mrpoundsign/PoundControl/releases/latest) and unzip it.
2. Drag `PoundControl.app` into your **Applications** folder.
3. On first launch, if macOS Gatekeeper warns about an unidentified developer, **Control-click (right-click) `PoundControl.app` → Open → Open**, or open **System Settings → Privacy & Security → Open Anyway**.

### Linux (`x86_64`)
1. Download `PoundControl-v*-linux-amd64.tar.gz` from the [Latest Release](https://github.com/mrpoundsign/PoundControl/releases/latest).
2. Extract and run:
   ```bash
   mkdir -p poundcontrol && tar -xzf PoundControl-v*-linux-amd64.tar.gz -C poundcontrol
   cd poundcontrol
   ./poundcontrol
   ```
   *(Optional)* Copy `poundcontrol` somewhere on your `PATH` such as `~/.local/bin/poundcontrol`.

### Phone & Tablet Touch Deck (Zero-Install Browser Mode)
Use any phone, iPad, or secondary device on the same Wi-Fi network without installing an app:
1. In **PoundControl Desktop**, open **Settings → Web Deck** and make sure **Enable LAN Web Touch Deck** is checked.
2. Click **Pair New Device** to display a temporary **6-digit PIN** and **QR code**.
3. Scan the QR code with your phone/tablet camera (or open the shown URL and enter the PIN), then approve the pairing prompt in Desktop Studio.

### Android Companion App
1. On your Android phone or tablet (Android 8.0+), download `PoundControl-v*-android.apk` from the [Latest Release](https://github.com/mrpoundsign/PoundControl/releases/latest).
2. Tap the downloaded APK and allow **Install unknown apps** when prompted by Android.
3. Launch **PoundControl**, open **Settings → Web Deck → Pair New Device** on your desktop, and scan the QR code to connect.

### Wear OS Smartwatch Deck
The Wear OS watch app communicates with your desktop through the **PoundControl Android companion app** over Bluetooth/Wearable Data Layer:
1. Install and pair the **Android companion app** (`PoundControl-v*-android.apk`) on the phone paired to your watch first.
2. Enable **Developer options → ADB debugging** (or **Wireless debugging**) on your Wear OS watch and install `PoundControl-v*-wear.apk`:
   ```bash
   adb install PoundControl-v*-wear.apk
   ```
   *(Or sideload using a Wear OS sideloader app such as Wear Installer.)*
3. Launch **PoundControl** on your watch to tap deck buttons, navigate pages/folders, adjust rotary dials, or add the **PoundControl** watch face complication.

### Verifying Downloads
Every release includes `checksums.txt`, `SHA256SUMS.txt`, and keyless **SLSA Build Provenance** attestations generated directly by GitHub Actions:
```bash
# Check SHA-256 checksum
sha256sum -c checksums.txt --ignore-missing

# Or verify GitHub SLSA Build Provenance with GitHub CLI
gh attestation verify <downloaded-file> --repo mrpoundsign/PoundControl
```

---

## Key Features

- **Virtual Desktop Deck & Pop-Out Pad**: Customizable grids and dials across profiles, pages, and nested folders with drag/move, auto-swap, cross-profile copy/paste, custom color palettes, word-wrapped titles, active accent glow/shimmer, and global keyboard hotkeys.
- **Visual Multi-Action Editor**: Build multi-step sequences with delays, inline step editing, mute toggles, reorder/duplicate controls, and live test runs.
- **Encrypted Multi-Device Touch Decks**: Mutual Ed25519 + SPAKE2 + HMAC pairing for LAN browsers, Android, and Wear OS with independent per-device viewports or Follow-Studio mode.
- **Built-In Plugins**:
  - **Core**: Page/folder/profile navigation and Multi-Actions.
  - **OBS Studio (WebSocket v5)**: Scene switching, source/filter toggles, audio mute/volume, and stream/record controls.
  - **Soundboard**: Polyphonic WAV, MP3, OGG Vorbis, and FLAC audio playback with per-slot modes, master volume, and output device selection.
  - **Apps & System Control**: Launch apps/scripts, open URLs, send HTTP/WebSocket webhooks, trigger keyboard macros/hotkeys, and send media keys.
- **Elgato Stream Deck (`.sdPlugin`) Compatibility**: Load standard SDK v2/v3 `.sdPlugin` bundles (with optional 1-click Node.js setup, WebView2/browser Property Inspector bridge, and **OpenAction Marketplace** browsing).
- **Sandboxed WebAssembly (`wazero`) Plugins**: Run cross-platform `.wasm` plugins compiled from Go, TinyGo, Rust, Zig, or C/C++ with a **Curated Catalog** and SHA-256 verified updates.

---

## Building from Source

### Windows (PowerShell with Zig CGo)

```powershell
.\build.ps1                # Run lint, unit/UI tests, and build GUI -> build\bin\poundcontrol.exe
.\build.ps1 -Target gui    # Build GUI only -> build\bin\poundcontrol.exe
.\build.ps1 -Target installer # Build Windows installer -> build\bin\PoundControl-Setup.exe
.\build.ps1 -Target android   # Test & build both Android and Wear OS APKs -> build\bin\
.\build.ps1 -Target test   # Run all unit & headless UI tests
.\build.ps1 -Target lint   # Run go fix -diff and golangci-lint-v2
.\build.ps1 -Target clean  # Clean build artifacts
```

### Linux / macOS / WSL (Bash)

```bash
./build.sh                 # Run lint, tests, and build GUI -> build/bin/poundcontrol
./build.sh gui             # Build native GUI -> build/bin/poundcontrol
./build.sh gui-win         # Cross-compile Windows GUI via MinGW -> build/bin/poundcontrol.exe
./build.sh installer       # Cross-compile Windows installer -> build/bin/PoundControl-Setup.exe
./build.sh test            # Run all Go tests
./build.sh lint            # Run go fix -diff and golangci-lint-v2
./build.sh clean           # Clean build artifacts
```

### Android & Wear OS Companion Apps

```bash
cd mobile/android
./gradlew :protocol:testDebugUnitTest :app:testDebugUnitTest :wear:testDebugUnitTest :app:assembleDebug :wear:assembleDebug
```

---

## Documentation

- **[Documentation Site](https://mrpoundsign.com/PoundControl/)** — Features overview, architecture, and security summary
- **[Download & Install Guide](https://mrpoundsign.com/PoundControl/install.html)** — Windows, macOS, Linux, LAN Web Deck, Android, Wear OS, checksums, and building from source
- **[WebAssembly Plugin ABI Reference](https://mrpoundsign.com/PoundControl/wasm-abi.html)** ([Markdown source](docs/WASM_PLUGIN_ABI.md)) — Guest exports, `poundcontrol` host imports, sandbox limits, and declarative `info` / `link` / `button` / `password` settings schema

---

## License

GNU Affero General Public License v3.0 (AGPL-3.0) — see [LICENSE](LICENSE).
