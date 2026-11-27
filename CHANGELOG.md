# Changelog

## v0.1.3 — 2026-11-26

### Fixed
- **OBS Studio Audio Volume & Mute Hydration Race**:
  - Guarded asynchronous `warmSlotState` and `ensureInputVolumePct` RPC reads in the built-in OBS Studio provider (`pkg/builtin/obs`) with monotonic per-input mutation counters and connection epoch tracking so late `GetInputVolume` / `GetInputMute` responses cannot overwrite newer dial rotations, keypad volume changes, or live OBS events.
- **Release Publishing Reliability**:
  - Added `GH_REPO` to the `Publish GitHub Release` step in `.github/workflows/release.yml` so GitHub Actions release creation succeeds cleanly when artifacts are published from a checkoutless release job.

### Changed
- **Documentation (`mrpoundsign.com/PoundControl`)**:
  - Expanded GitHub Pages guides with step-by-step configuration instructions for OBS WebSocket v5 0–100% cubic faders, Curated WebAssembly plugins, and dynamic **TownCrier & Plugins** actions, and synced the HTML Wasm ABI reference with `pc_on_start`, `pc_register_actions`, borderless SVG tiles, and Wear OS feedback fields.

---

## v0.1.2 — 2026-11-26

### Added
- **Curated TownCrier & Plugins Integration (`com.github.mrpoundsign.towncrier.wasm`)**:
  - Added **TownCrier & Plugins** to the curated Wasm Plugin Catalog with SHA-256 verified installs from `TownCrier` GitHub Releases.
  - Added runtime dynamic action registration to the WebAssembly plugin ABI (`pc_on_start` export and `pc_register_actions` host import) so plugins can discover and expose live actions at startup and push real-time tile updates (`hideTitle: true` borderless 144×144 SVG tiles and Wear OS watch `feedback.label` / `feedback.sublabel` fields).
- **Round Wear OS Watch Deck UI**:
  - Updated the Wear OS companion deck with round-screen layout refinements and rotary bezel / crown dial input support.

### Changed
- **OBS 0–100% Cubic Perceptual Volume Fader**:
  - Switched the built-in OBS Studio Audio Volume action from raw decibels to OBS Studio's 0–100% cubic perceptual fader scale (`0.5%` step precision, dial push mute toggle or reset-to-100%, clean `-inf dB` / `null` handling, and automatic fallback compatibility for legacy `stepDb` / `targetDb` slot configs).
- **LAN & Mobile Reconnect Viewport Persistence**:
  - Preserved each browser/Android/Wear touch deck's active page/folder viewport cleanly across reconnects.
- **Guided Installer & Release Pipeline**:
  - Upgraded `cmd/installer` to `appinstaller@v0.1.0` for component-preserving silent self-updates and clean uninstalls, and standardized artifact filenames and SLSA OIDC attestation in GitHub Actions.

---

## v0.1.1 — 2026-11-25

### Added
- **Wear OS Companion App & Bluetooth Relay**:
  - Added the Wear OS smartwatch deck (`mobile/android/wear`) communicating with Desktop Studio via the Android companion phone relay over Wearable Data Layer, complete with a watch face complication.
- **Documentation Site (`mrpoundsign.com/PoundControl`)**:
  - Added GitHub Pages documentation covering features, installation across all desktop/mobile targets, SHA-256 + SLSA verification, and the full WebAssembly Plugin ABI specification.

### Changed
- **Licensing & Provenance**:
  - Switched repository license to GNU Affero General Public License v3.0 (AGPL-3.0) and added Wear OS APK builds and keyless SLSA Build Provenance attestations to releases.

---

## v0.1.0 — 2026-11-24

### Added
- **Initial Release of PoundControl**:
  - Cross-platform open-source macro deck and studio built with Go and Fyne for Windows, macOS, and Linux, plus an Android companion app and zero-install encrypted LAN Web Touch Deck (Ed25519 + SPAKE2 + HMAC pairing).
  - Multi-action sequences with delays/reordering, floating Virtual Desktop Deck pop-out, and global hotkeys.
  - Built-in **Core**, **OBS Studio (WebSocket v5)**, **Soundboard**, and **Apps & System Control** plugins alongside an **Elgato `.sdPlugin`** host and pure-Go **`wazero` WebAssembly** plugin sandbox with OS Keyring secret storage.
