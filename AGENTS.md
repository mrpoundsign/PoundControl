# PoundDeck — Agent & Project Knowledge Base (AGENTS.md)

## 1. User Preferences & Working Rules

> [!IMPORTANT]
> Always adhere strictly to these core working preferences:
> 1. **True Pair Programming Assistant (No "Vibe Coding")**: The user is an experienced, knowledgeable developer and lead. Never act like an autonomous black box. When investigating an issue or unexpected behavior, always communicate technical findings, underlying root causes, and mechanics to the user *first*. Discuss and align before creating GitHub issues or making code changes.
> 2. **Feedback & Explicit Approval on Decisions**: Always present clear options, findings, and proposed designs, then wait for user approval before moving forward. Never jump ahead to file tickets, create branches, or write code based on assumptions. Once a specific plan is approved, executing that agreed plan autonomously is expected, but any new findings, deviations, or decisions must pause for communication and approval.
> 3. **One ticket at a time**: We work on exactly one issue/ticket at a time unless explicitly directed otherwise. Exactly **1 commit per issue** (aspirational goal; multiple commits or tickets on a branch only when aligned).
> 4. **Pre-Commit Quality Gate**: Before every single commit, ALWAYS run:
>    - `go fix -diff ./...` (must report 0 diff)
>    - `golangci-lint-v2 run ./...` (must report 0 issues)
>    - `go test -count=1 -v ./cmd/... ./internal/... ./pkg/...` (all unit & UI tests must pass)
> 5. **User tests GUI before committing**: Always stop and let the user manually test the GUI binary (`build/bin/pounddeck.exe` or `build/bin/pounddeck`) before any changes are committed to git. Never launch GUI binaries interactively in agent subshells.
> 6. **Prioritize Built-in Agent Tools Over Shelling Out**: Always use built-in tools (`read_file`, `write_file`, `edit_file`) for inspecting and editing files. Only shell out via `run_command` when strictly necessary (e.g., git operations, building/running tests, linting, or `gh` CLI).
> 7. **In-Place Build Outputs Only (`build/bin/`)**: Keep compiled binaries, installers, and runtime artifacts strictly inside `build/bin/` (gitignored). Never copy build outputs outside the repository.
> 8. **Planning Repository Independence**: `planning/` is its own local-only git repository (`main`, no remote) ignored by the root `.gitignore`. Planning commits happen independently inside `planning/` referencing ticket/milestone IDs.
> 9. **Always Link PRs, Issues, and CI Runs**: Whenever creating or referencing GitHub Pull Requests, Issues, or Actions CI runs, always include clickable Markdown links.
> 10. **GitHub CLI Issue & PR Bodies via Files**: PowerShell mangles markdown backticks and escapes when passing strings inline to `gh`. Always write multiline issue or PR bodies to a temporary file in `build/` and pass `--body-file`, then clean up the temporary file.
> 11. **Strict Test Configuration & Directory Isolation**: NEVER write tests that read or write real user configuration locations (`%APPDATA%`, `~/.config`, etc.). Tests MUST always use `t.TempDir()` or in-memory stores.
> 12. **Explicit Approval for Pull Requests**: Never open a Pull Request without explicit user approval. Release merges from `dev` to `main` occur via GitHub PR listing `Closes #<id>` for completed issues.

---

## 2. Project Overview & Mission

**PoundDeck** is a cross-platform (Windows, Linux, macOS), open-source programmable macro & stream deck application written in **Go** using **Fyne v2**. It pairs a **Fyne Virtual Desktop Deck** (with global hotkeys and a pop-out pad) and a **LAN Web / Mobile / Tablet Touch Deck** with a unified three-pillar plugin architecture:

- **Primary Repository**: `github.com/mrpoundsign/PoundDeck`
- **License**: MIT License
- **Visual Identity**: Obsidian Slate (`#0A0A0C`), Chrome White (`#FFFFFF`), and Electric Sky Blue (`#38BDF8`).
- **Core Stack**:
  - **Go**: 1.27+
  - **GUI Toolkit**: [Fyne v2](https://fyne.io/) (v2.8.2)
  - **Built-in Plugins**: `core` (pages/folders/profiles/multi-actions), `obs` (OBS WebSocket v5), `apps` (app/script launch, URLs, HTTP/WS webhooks, hotkeys/macros, media controls)
  - **External Plugins**: Elgato Stream Deck SDK v2/v3 `.sdPlugin` WebSocket host (with WebView2 / browser Property Inspector bridge) and pure-Go `wazero` WebAssembly plugins (supporting Go, TinyGo, Rust, Zig, C/C++)

---

## 3. Architecture & Codebase Map

```text
PoundDeck/
├── assets/               # Application icon (SVG/PNG) & embedded Go resources
├── build/
│   └── bin/              # Local compiled binaries only (gitignored)
├── cmd/
│   ├── pounddeck/        # Main Fyne Desktop Studio & Virtual Deck entry point
│   └── installer/        # Guided setup installer (appinstaller, Milestone 6)
├── internal/
│   ├── app/              # App controller, atomic config store, wiring
│   ├── ui/               # Fyne Studio GUI, Obsidian theme, pop-out pad, inspectors
│   ├── version/          # App metadata and build version constants
│   └── webdeck/          # LAN HTTP/WS touch deck & PI HTTP bridge
├── pkg/
│   ├── deck/             # Core domain types (GridSize, Coordinates, Profile, Page, Engine)
│   ├── plugin/           # Unified Plugin/Provider/Action abstractions & registry
│   ├── sdplugin/         # Elgato .sdPlugin manifest loader, supervisor & WS host
│   ├── wasmplugin/       # Pure-Go wazero WebAssembly dynamic plugin provider
│   ├── tile/             # Button image/SVG/title/state compositor
│   └── builtin/          # Built-in native Go plugins (core/, obs/, apps/)
├── planning/             # Local-only git repo (PRDs & task lists per milestone/ticket)
├── build.ps1             # Windows PowerShell build/lint/test/check script (Zig CGo)
├── build.sh              # Linux/macOS/WSL build/lint/test/cross-compile script
├── .golangci.yml         # golangci-lint v2 configuration
├── .goreleaser.yaml      # GoReleaser v2 release configuration
└── FyneApp.toml          # Fyne application metadata
```

---

## 4. Testing, Fyne Thread Safety & Cross-Platform Build Rules

1. **Fyne GUI Thread Safety**:
   - All UI mutations triggered from background goroutines, tickers, WebSocket callbacks, or async loaders **must** be dispatched onto the main Fyne thread via `dispatchUI(...)` (`fyne.Do(...)`). Never mutate widget state directly off-thread.
2. **Cross-Platform Builds**:
   - **Windows PowerShell**:
     - `.\build.ps1` (lint + test + GUI build to `build\bin\pounddeck.exe`)
     - `.\build.ps1 -Target lint` (`go fix -diff ./...` + `golangci-lint-v2 run ./...`)
     - `.\build.ps1 -Target test` (`go test -count=1 -v ./cmd/... ./internal/... ./pkg/...`)
     - `.\build.ps1 -Target gui` (Zig CGo + `go-winres` -> `build\bin\pounddeck.exe`)
     - `.\build.ps1 -Target check` (`goreleaser check`)
   - **Linux / macOS / WSL Bash**:
     - `./build.sh` (lint + test + native GUI build to `build/bin/pounddeck`)
     - `./build.sh gui-win` (MinGW cross-compile -> `build/bin/pounddeck.exe`)
3. **Config & Test Isolation**:
   - Any persistent configuration or plugin data store must accept an explicit base directory or store interface so unit tests strictly use `t.TempDir()` or memory stores.
