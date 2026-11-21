# PoundDeck — Design Document
**Cross-Platform Macro Deck, OBS Controller & Extensible Plugin Studio (Go & Fyne)**

---

## 1. Executive Summary

**PoundDeck** is an open-source, cross-platform (Windows, Linux, macOS) programmable macro and stream deck written in **Go** with **Fyne v2**. It combines a **Fyne Virtual Desktop Deck** (with a distraction-free pop-out pad and global hotkeys) and a **LAN Web / Mobile / Tablet Touch Deck** over a unified three-pillar plugin architecture:

1. **Built-in Native Go Plugins**: Zero-latency native actions for **Core Navigation** (pages, folders, profiles, multi-actions), **OBS Studio** (WebSocket v5), and **App & System Control** (app/script launcher, open URL, HTTP/WS webhooks, hotkeys/macros, media keys).
2. **Elgato Stream Deck SDK v2/v3 (`.sdPlugin`) Host**: Runs standard external Stream Deck plugins (Node.js and compiled binaries, including Go plugins and TownCrier's `.sdPlugin`) with a WebView2 / browser Property Inspector bridge.
3. **`wazero` WebAssembly Plugins**: Pure-Go, sandboxed, hot-reloadable dynamic plugins compiled from Go, TinyGo, Rust, Zig, or C/C++ with a single OS-agnostic `.wasm` artifact and declarative Fyne inspector schemas.

---

## 2. Visual Identity & Color System

PoundDeck shares the **Obsidian, Chrome Slate & Electric Sky Blue** visual identity established across InkAnim and TownCrier.

| Token | Hex / RGBA | Role / Usage |
| :--- | :--- | :--- |
| **Obsidian Deep (Base 1)** | `#020203` | Deep canvas shadows, pressed key wells |
| **Obsidian Slate (Base 2)** | `#0A0A0C` | Root window & deck studio background |
| **Dark Charcoal (Surface 1)** | `#141416` | Panels, inactive key tiles, modal dialogs |
| **Elevated Slate (Surface 2)** | `#1E293B` | Hover/active buttons, inputs, secondary cards |
| **Slate Border / Grid** | `#475569` | Key borders, grid dividers, dial rings |
| **Muted Text / Guides** | `#64748B` | Coordinates, muted captions, empty hints |
| **Secondary Text / Slate** | `#94A3B8` | Subtitles, metadata, inactive badges |
| **Primary Text / Chrome** | `#FFFFFF` | Headings, active key titles |
| **Electric Sky Blue (Primary)** | `#38BDF8` | Selected tile border, primary CTA, active indicators |
| **Deep Sky (Primary Dark)** | `#0284C7` | Active button press accent |
| **Selection Tint** | `rgba(56, 189, 248, 0.25)` | Selected slot highlight |

---

## 3. Surfaces & Architecture

### 3.1 Shared Deck Engine & Surfaces
Every surface (Desktop Virtual Pad, Pop-Out Pad, LAN Web/Tablet Touch Deck) connects to the same `deck.Engine`:
- **Profile → Pages → Slots (`Row, Col`) & Dials (`Encoder`)**
- **Inbound events**: `keyDown`, `keyUp`, `dialRotate`, `dialPress`, `willAppear`, `willDisappear`, `didReceiveSettings`
- **Outbound visual updates**: `setTitle`, `setImage`, `setState`, `setFeedback`, `showAlert`, `showOk`

### 3.2 Three-Pillar Plugin Architecture (`pkg/plugin.Provider`)

```text
+-----------------------------------------------------------------------------------+
|                                PoundDeck Application                              |
|                                                                                   |
|   +--------------------------+     +------------------------------------------+   |
|   |   Surfaces (Input/UI)    |     |        Core Deck Engine (pkg/deck)       |   |
|   | - Fyne Studio & Grid     | <-> | - Profiles, Pages, Folders, Multi-Action |   |
|   | - Fyne Pop-Out Pad       |     | - Slot & Encoder State Store             |   |
|   | - Global Hotkeys         |     | - Tile Compositor (pkg/tile)             |   |
|   | - LAN Web/Touch Deck     |     +------------------------------------------+   |
|   +--------------------------+                         ^                          |
|                                                        |                          |
|                                    +------------------------------------------+   |
|                                    |    Unified Registry (pkg/plugin)         |   |
|                                    +------------------------------------------+   |
|                                      /                 |                   \      |
|                                     v                  v                    v     |
|                    +-------------------+   +--------------------+   +-----------+ |
|                    | Built-in Go       |   | .sdPlugin Host     |   | wazero    | |
|                    | (pkg/builtin)     |   | (pkg/sdplugin)     |   | Wasm Host | |
|                    | - core (pages)    |   | - manifest.json    |   | - .wasm   | |
|                    | - obs (WS v5)     |   | - Node / Binary    |   | - Go/Rust | |
|                    | - apps (exec/HTTP |   | - Elgato WS Proto  |   |   Zig/C++ | |
|                    |   hotkeys/media)  |   | - WebView2/Browser |   | - Sandbox | |
|                    +-------------------+   +--------------------+   +-----------+ |
+-----------------------------------------------------------------------------------+
```

### 3.3 Studio 3-Column UI Layout

```text
+---------------------------------------------------------------------------------------------------+
|  PoundDeck v0.1.0   [ Profile: Default v ]  [ Page 1 / 1 ]          Web Deck: Off | Plugins: 0    |
+------------------------+-----------------------------------------------+--------------------------+
| Action Catalog         | Virtual Deck (5x3)                            | Action Inspector         |
|                        |                                               |                          |
| > Core Navigation      |  +--------+ +--------+ +--------+ +--------+  | Slot: Row 0, Col 0       |
|   - Folder / Page      |  | (0, 0) | | (0, 1) | | (0, 2) | | (0, 3) |  | Type: Keypad             |
|   - Multi-Action       |  +--------+ +--------+ +--------+ +--------+  |                          |
|                        |  +--------+ +--------+ +--------+ +--------+  | [ Action configuration   |
| > OBS Studio           |  | (1, 0) | | (1, 1) | | (1, 2) | | (1, 3) |  |   and native/HTML PI   |
|   - Scene / Source     |  +--------+ +--------+ +--------+ +--------+  |   appears here ]         |
|   - Stream / Record    |  ...                                          |                          |
|                        |                                               |                          |
| > App Control          |  --- Virtual Dials (Encoders) --------------  |                          |
| > Stream Deck Plugins  |  ( Dial 1 )  ( Dial 2 )  ( Dial 3 ) ( Dial 4) |                          |
| > Wasm Plugins         |                                               |                          |
+------------------------+-----------------------------------------------+--------------------------+
| Ready | Selected: (0, 0) | Grid: 5x3 | Plugins: 0 | Web Deck: Stopped                             |
+---------------------------------------------------------------------------------------------------+
```

---

## 4. Repository Structure

```text
PoundDeck/
├── assets/               # Application icon (icon.svg, icon.png) and embedded Go resources
├── build/bin/            # Compiled binaries only (gitignored)
├── cmd/
│   ├── pounddeck/        # Main Fyne Desktop Studio entry point
│   └── installer/        # Guided Setup Installer (Milestone 6)
├── internal/
│   ├── app/              # Application controller, atomic config store, wiring
│   ├── ui/               # Fyne Studio UI, Obsidian theme, custom widgets
│   ├── version/          # Version and application ID metadata
│   └── webdeck/          # LAN touch deck server and PI HTTP bridge
├── pkg/
│   ├── deck/             # Profiles, pages, grids, slots, surface interfaces & engine
│   ├── plugin/           # Unified Provider/Action interfaces & manifest schema
│   ├── sdplugin/         # Elgato .sdPlugin loader, process supervisor & WS host
│   ├── wasmplugin/       # Pure-Go wazero WebAssembly plugin runtime
│   ├── tile/             # Button image/SVG/title/state compositor
│   └── builtin/          # Native plugins: core/, obs/, apps/
├── planning/             # Local-only git repo for PRDs and task breakdowns
├── build.ps1             # Windows PowerShell build/lint/test/check script
├── build.sh              # Linux/macOS/WSL build/lint/test/cross-compile script
├── .golangci.yml         # Linter rules
├── .goreleaser.yaml      # Release packaging config
└── FyneApp.toml          # Fyne package metadata
```
