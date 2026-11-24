# PoundControl WebAssembly (`.wasm`) Plugin ABI Specification

PoundControl hosts single-file, sandboxed, hot-reloadable WebAssembly plugins using pure-Go [`wazero`](https://wazero.io/) across Windows, Linux, and macOS.

Any language targeting **WASI Preview 1 (`wasi_snapshot_preview1`)** as a **reactor module** (Go, TinyGo, Rust, Zig, C/C++) can implement this ABI.

For a complete working Go example, see [`examples/wasm-pomodoro/main.go`](../examples/wasm-pomodoro/main.go).

---

## 1. Runtime Model, Discovery & Sandbox Limits

### 1.1 Module Requirements (WASI Reactor vs. Command)

- Modules **must** be built as **WASI reactors** exporting `_initialize` (and must **not** export `_start` without `_initialize`).
  - With standard Go (`wasip1/wasm`):
    ```bash
    GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o myplugin.wasm .
    ```
  - Command modules exporting `_start` exit on return and are rejected at load time with an error card in **Settings → Plugins**.
- Enabled WASI host capabilities:
  - Wall clock (`walltime`), monotonic clock (`nanotime`), `nanosleep`
  - Cryptographically secure randomness (`rand.Reader`)
- Disabled capabilities (by design):
  - No host filesystem preopens, no raw TCP/UDP sockets, no environment variables, and no subprocess execution. All outbound network requests go through the sandboxed [`pc_http_request`](#46-pc_http_request) import and require explicit `permissions.allowedHosts` entries in `pc_manifest`.

### 1.2 Discovery & Hot Reload

PoundControl scans:
1. The per-user default `wasm/` folder next to `config.json` (displayed in **Settings → Plugins**)
2. Any user-configured files or directories in **Extra Wasm Plugin Paths** (`extraPaths`)

Within each configured directory, PoundControl discovers `.wasm` files at the root level and one subdirectory deep (so organizing `wasm/<plugin-name>/<plugin>.wasm` works cleanly). Files are loaded in sorted order; duplicate plugin `uuid`s are skipped. Click **Reload Plugins** in **Settings → Plugins** to cleanly stop active timers, tear down old runtimes, and load updated `.wasm` builds.

### 1.3 Resource & Timeout Limits

| Limit / Constant | Value | Notes |
| --- | --- | --- |
| Linear memory cap | **32 MiB** (`512` pages) | Enforced per Wasm runtime instance |
| Maximum payload size | **4 MiB** | Applies to `pc_manifest` and host/guest memory copies |
| Default guest call timeout | **5 seconds** | Applies to `_initialize`, `pc_manifest`, `pc_handle_event`, `pc_on_timer`, `pc_on_global_settings` |
| `pc_on_config_action` call timeout | **20 seconds** | `max(callTimeout, maxHTTPTimeout + 5s)` so full 15s HTTP timeouts finish before cancelling the call |
| Default HTTP request timeout | **10 seconds** | When `timeoutMs <= 0` in `pc_http_request` |
| Maximum HTTP request timeout | **15 seconds** | Larger `timeoutMs` values are clamped to `15000` |
| Maximum HTTP response body | **256 KiB** | Response body is truncated via `io.LimitReader` |
| Minimum host timer interval | **250 ms** | Smaller non-zero intervals are clamped to `250` |
| Maximum active timers per plugin | **16** | Across all `timerId` keys |
| Log ring buffer | **200 entries** | Viewed in Studio Logs |

### 1.4 Concurrency & Serialization

- All calls into a single plugin instance (`pc_manifest`, `pc_handle_event`, `pc_on_timer`, `pc_on_global_settings`, `pc_on_config_action`) are serialized by a per-instance host mutex.
- Host callbacks triggered *from inside* a guest call (such as `pc_update_visual` or `pc_save_global_settings`) execute synchronously within that guest call.

---

## 2. Memory & Calling Convention

All complex payloads cross the host/guest boundary as UTF-8 **JSON** buffers in the guest's linear memory (`memory` export).

### 2.1 Guest Allocator (`pc_alloc` & `pc_free`)

Before invoking guest functions that accept a payload (`pc_handle_event`, `pc_on_timer`, `pc_on_global_settings`, `pc_on_config_action`), the host allocates linear memory via `pc_alloc`, copies the JSON bytes in, invokes the export, and frees the buffer via `pc_free` (if exported):

- **Required:** `pc_alloc(size: uint32) -> ptr: uint32`
  - Must return a non-zero offset into guest linear memory with at least `size` writable bytes.
  - Returning `0` fails the call and transitions the plugin instance to `error` state.
- **Recommended:** `pc_free(ptr: uint32)`
  - Releases the allocation made by `pc_alloc`.

> **Go tip:** Keep a mutex-guarded map `map[uint32][]byte` pinning `pc_alloc` slices so Go's garbage collector cannot collect or relocate buffers between `pc_alloc` and `pc_free`.

### 2.2 Packed Pointer+Length Return (`pc_manifest`)

`pc_manifest` returns a single `uint64` packing `(ptr, len)`:

```text
result = (uint64(ptr) << 32) | uint64(len)
```

The host validates `ptr != 0 && len > 0 && len <= 4 MiB`, copies `len` bytes out of guest memory, and immediately calls `pc_free(ptr)` (if exported).

### 2.3 Caller-Buffer + Negative-Length Retry (`pc_get_global_settings`, `pc_http_request`)

When a host import returns JSON to the guest, the guest supplies an output buffer `(outPtr, outCap)`:

- If `len(jsonBytes) <= outCap`, the host writes the JSON bytes to `outPtr` and returns `int32(len(jsonBytes))` (`>= 0`).
- If `len(jsonBytes) > outCap`, the host writes **nothing** and returns `-int32(len(jsonBytes))`. Allocate a buffer of size `uint32(-rc)` and retry once.
- Returning `-1` indicates a serialization error or invalid guest pointer/bounds. (Because valid JSON objects such as `{}` are at least 2 bytes, a needed-size return is always `<= -2`.)

---

## 3. Guest Exports (Host → Plugin)

All string/JSON payloads passed to guest exports are UTF-8 encoded without NUL terminators.

| Export Name | Status | Signature (Wasm types) | Return Value |
| --- | --- | --- | --- |
| `pc_alloc` | **Required** | `(size: uint32) -> (ptr: uint32)` | Linear memory pointer (`0` = OOM/error) |
| `pc_free` | Recommended | `(ptr: uint32) -> ()` | None |
| `pc_manifest` | **Required** | `() -> (packedPtrLen: uint64)` | `(uint64(ptr) << 32) \| uint64(len)` |
| `pc_handle_event` | **Required** | `(ptr: uint32, len: uint32) -> (rc: int32)` | `0` OK, `< 0` error |
| `pc_on_timer` | Optional | `(ptr: uint32, len: uint32) -> (rc: int32)` | `0` OK, `< 0` error |
| `pc_on_global_settings` | Optional | `(ptr: uint32, len: uint32) -> (rc: int32)` | `0` OK, `< 0` error |
| `pc_on_config_action` | Optional | `(ptr: uint32, len: uint32) -> (rc: int32)` | `0` OK, `!= 0` surfaces error to UI without crashing module |

### 3.1 `pc_manifest() -> uint64`

Called once during plugin initialization. Returns a packed JSON [`GuestManifest`](#51-manifest-json-schema). Failures mark the plugin `error` in **Settings → Plugins**.

### 3.2 `pc_handle_event(ptr, len uint32) -> int32`

Dispatches deck lifecycle and user-input events as a JSON `ActionEvent`:

```json
{
  "event": "keyDown",
  "context": "550e8400-e29b-41d4-a716-446655440000",
  "action": "com.example.plugin.toggle",
  "controller": "Keypad",
  "coordinates": { "row": 0, "col": 2 },
  "dialIndex": 0,
  "settings": { "channel": "main" },
  "state": 0,
  "ticks": 0
}
```

Supported `"event"` values:
- `"willAppear"` — Slot or dial became visible on the active page/folder or profile (or plugin was re-enabled).
- `"willDisappear"` — Slot or dial left the visible view, was cleared, or its plugin was disabled.
- `"didReceiveSettings"` — User saved updated per-slot settings in the Action Inspector.
- `"keyDown"` — Keypad button pressed (desktop click, global hotkey, or LAN Web Deck tap).
- `"keyUp"` — Keypad button released.
- `"dialRotate"` — Encoder dial rotated; `"ticks"` contains the signed delta (positive clockwise, negative counter-clockwise).
- `"dialPress"` — Encoder dial pushed.

> **Fault isolation:** Returning a non-zero status code reports an error to callers without crashing the plugin. **Wasm traps or call timeouts** mark the instance `error` and cancel all of its timers until reloaded.

### 3.3 `pc_on_timer(ptr, len uint32) -> int32`

Called on each interval tick armed via [`pc_set_timer`](#47-pc_set_timer). Payload JSON:

```json
{
  "timerId": "poll-live",
  "context": "optional-slot-context"
}
```

If an invocation returns a Go/Wasm error (trap or 5s call timeout), that timer goroutine stops and the module transitions to `error`.

### 3.4 `pc_on_global_settings(ptr, len uint32) -> int32`

Called when the user clicks **Save** or **Reset Defaults** on the plugin's Settings tab (or any host-initiated `Engine.SaveGlobalSettings`). Payload is a JSON object matching the plugin's global settings **merged transparently with any `password` fields from the OS keyring**.

> **No feedback loop:** Calling [`pc_save_global_settings`](#44-pc_save_global_settings) from *inside* the plugin persists the data and live-refreshes the Studio UI, **without** re-entering `pc_on_global_settings`.

### 3.5 `pc_on_config_action(ptr, len uint32) -> int32`

Called when the user clicks an interactive `"button"` field in **Settings → `<Plugin>`**. Payload JSON (`GuestConfigActionEvent`):

```json
{
  "action": "authConnect",
  "settings": {
    "clientId": "abc123",
    "accessToken": "..."
  }
}
```

- `settings` merges persisted global settings, OS keyring `password` values, and any **unsaved edits** currently typed into the Settings form.
- Uses an extended **20-second** call timeout (`max(callTimeout, maxHTTPTimeout + 5s)`) so outbound OAuth/HTTP calls up to `15s` can finish cleanly.
- Returning any non-zero code (`!= 0`) reports an error in the Studio status bar **without** marking the module as `error` (so logical errors like `"Verify Connection failed"` or `"Missing Client ID"` keep the plugin running).

---

## 4. Host Imports (`poundcontrol` Module)

All imports reside in the WebAssembly import module **`"poundcontrol"`**.

| Import Name | Signature | Return |
| --- | --- | --- |
| `pc_update_visual` | `(ctxPtr, ctxLen, patchPtr, patchLen: uint32) -> int32` | `0` OK, `< 0` error |
| `pc_save_settings` | `(ctxPtr, ctxLen, jsonPtr, jsonLen: uint32) -> int32` | `0` OK, `< 0` error |
| `pc_get_global_settings` | `(outPtr, outCap: uint32) -> int32` | `>= 0` bytes written, `-neededLen` retry |
| `pc_save_global_settings` | `(jsonPtr, jsonLen: uint32) -> int32` | `0` OK, `< 0` error |
| `pc_log` | `(msgPtr, msgLen: uint32) -> ()` | None |
| `pc_http_request` | `(reqPtr, reqLen, respPtr, respCap: uint32) -> int32` | `>= 0` bytes written, `-neededLen` retry |
| `pc_set_timer` | `(idPtr, idLen, ctxPtr, ctxLen, intervalMs: uint32) -> int32` | `0` OK, `< 0` error |
| `pc_deck_nav` | `(cmdPtr, cmdLen: uint32) -> int32` | `0` OK, `< 0` error |

### 4.1 `pc_update_visual`

Updates live title, subtitle, colors, icon, active state, encoder feedback, or flash animation on the action instance identified by `ctxPtr/ctxLen`. Payload is a partial JSON `VisualPatch` — omitted/`null` fields leave the current property unchanged:

```json
{
  "title": "Optional Override",
  "subtitle": "LIVE 1.2K",
  "color": "#9146FF",
  "backgroundColor": "#5C1922",
  "image": "data:image/svg+xml;base64,...",
  "state": 1,
  "feedback": {
    "value": "75%",
    "indicator": { "value": 75 }
  },
  "flash": "ok"
}
```

- `backgroundColor`: A non-empty hex value overrides the slot's user-configured background color and temporarily suppresses any custom background image; passing `""` (empty string) clears the override and restores both.
- `image`: Accepts an inline `data:image/*` data URI, a safe relative file path inside the `.wasm` plugin's directory (no `..` escapes, symlinks escaping the plugin folder, absolute paths, `file://`, `http(s)://`, or non-image `data:` URIs), or `""` to clear an overridden image. Unsafe non-empty URIs are ignored without clearing existing slot artwork.
- `flash`: Supported values are `""`, `"ok"`, and `"alert"`.
- Returns `0` on success, `-1` on invalid memory bounds, `-2` on invalid JSON.

### 4.2 `pc_save_settings`

Persists updated per-instance settings JSON for the slot identified by `ctxPtr/ctxLen` back to `config.json`.
- Returns `0` on success, `-1` invalid memory read, `-2` invalid JSON, `-3` unbound host, `-4` save error (such as unknown slot context).

### 4.3 `pc_get_global_settings`

Reads this plugin's persisted plugin-wide global settings JSON object (namespace keyed by the plugin's `manifest.uuid`), **transparently merging any `"password"` schema fields stored in the OS keyring**.

- Uses the [negative-length retry convention](#23-caller-buffer--negative-length-retry-pc_get_global_settings-pc_http_request). Returns `{}` when no global settings exist yet.

### 4.4 `pc_save_global_settings`

Atomically persists the plugin's plugin-wide global settings JSON object:
- Fields whose `key` has `"type": "password"` in `globalSettingsSchema` are routed automatically to the OS keyring (`secrets.Store`) and stripped from `config.json`. Writing `""` for a password key deletes it from the keyring; omitting a password key preserves its existing keyring entry.
- Triggers live UI refresh on an open **Settings → `<Plugin>`** tab (`info`, `link`, `button`, and keyring password status indicators).
- Does **not** re-invoke `pc_on_global_settings`.
- Returns `0` on success, `-1` invalid memory read, `-2` invalid JSON, `-3` unbound host / missing UUID, `-4` save failure.

### 4.5 `pc_log`

Appends a diagnostic message `[pluginUUID] <message>` to PoundControl's in-app log buffer.

### 4.6 `pc_http_request`

Executes a sandboxed outbound HTTP/HTTPS request on the plugin's behalf.

Request JSON (`GuestHTTPRequest`):

```json
{
  "method": "POST",
  "url": "https://id.twitch.tv/oauth2/device",
  "headers": {
    "Content-Type": "application/x-www-form-urlencoded"
  },
  "body": "client_id=abc&scopes=chat:read",
  "timeoutMs": 8000
}
```

Response JSON (`GuestHTTPResponse`):

```json
{
  "status": 200,
  "headers": {
    "Content-Type": "application/json"
  },
  "body": "{...}",
  "error": ""
}
```

- Uses the [negative-length retry convention](#23-caller-buffer--negative-length-retry-pc_get_global_settings-pc_http_request) when the encoded `GuestHTTPResponse` exceeds `respCap`.
- **`permissions.allowedHosts` enforcement:** Requests to URLs outside the manifest's allowlist are rejected with `status: 403` and an `error` message.
  - Only `http://` and `https://` URLs with non-empty hosts and no embedded userinfo (`user:pass@host`) are allowed.
  - Allowed host patterns:
    - Exact hostname (case-insensitive): `"api.twitch.tv"`
    - Host and port: `"127.0.0.1:8080"`
    - Wildcard subdomain (`*.` matches subdomains only, not the apex domain): `"*.example.com"`
- `headers` in the response preserves the first value per header key.

### 4.7 `pc_set_timer`

Creates, updates, or cancels a named host interval timer (`timerId` must be non-empty):
- `intervalMs == 0`: Cancels the timer identified by `timerId`.
- `intervalMs > 0`: Schedules ticks invoking `pc_on_timer` every `max(intervalMs, 250)` ms, forwarding `timerId` and `context`.
- Returns `0` on success, `-1` invalid memory read, `-2` empty `timerId`, `-3` timer limit (`16`) exceeded.

### 4.8 `pc_deck_nav`

Requests deck navigation or folder/profile switching. Payload JSON (`GuestNavCommand`):

```json
{
  "action": "goToPage",
  "page": 2
}
```

Supported `"action"` values and parameters:
- `"nextPage"` — Advance one page (wraps at end)
- `"prevPage"` — Go back one page (wraps at start)
- `"goToPage"` — Jump to 1-based `"page"`
- `"openFolder"` — Push folder `"folderId"` (`"folderName"` optional display fallback)
- `"closeFolder"` — Pop the current folder
- `"switchProfile"` — Switch active profile by `"profileRef"` (ID or name)

Returns `0` OK, `-1` memory read error, `-2` invalid JSON, `-3` deck control unavailable, `-4` unknown action, `-5` navigation error.

---

## 5. Manifest & Declarative Settings Schema

### 5.1 Manifest JSON Schema

Returned as JSON from `pc_manifest`:

```json
{
  "uuid": "com.example.twitch",
  "name": "Twitch",
  "version": "1.0.0",
  "author": "Example Author",
  "description": "Twitch controls and Device Code sign-in",
  "icon": "data:image/svg+xml;base64,...",
  "categoryIcon": "data:image/svg+xml;base64,...",
  "permissions": {
    "allowedHosts": ["id.twitch.tv", "api.twitch.tv"]
  },
  "globalSettingsSchema": [
    {
      "key": "authStatus",
      "label": "Connection Status",
      "type": "info",
      "section": "General",
      "default": "Not signed in"
    },
    {
      "key": "authConnect",
      "label": "Sign In with Twitch",
      "type": "button",
      "section": "General",
      "default": { "label": "Sign In with Twitch", "disabled": false }
    },
    {
      "key": "clientId",
      "label": "Twitch Client ID",
      "type": "password",
      "section": "Advanced",
      "placeholder": "Using built-in Client ID — enter to override"
    },
    {
      "key": "accessToken",
      "label": "OAuth Access Token",
      "type": "password",
      "section": "Advanced"
    }
  ],
  "actions": [
    {
      "uuid": "com.example.twitch.marker",
      "name": "Create Stream Marker",
      "category": "Twitch",
      "tooltip": "Drop a VOD marker at the current timestamp",
      "icon": "data:image/svg+xml;base64,...",
      "defaultTitle": "Marker",
      "defaultColor": "#9146FF",
      "defaultImage": "data:image/svg+xml;base64,...",
      "hideTitle": false,
      "controllers": ["Keypad"],
      "settingsSchema": [
        {
          "key": "description",
          "label": "Marker Description",
          "type": "text",
          "section": "General",
          "placeholder": "Optional note (max 140 chars)"
        }
      ]
    }
  ]
}
```

- **`controllers`:** Array of `"Keypad"` and/or `"Encoder"`. Defaults to `["Keypad"]` when omitted.
- **Icon fallback & URI sanitization (`icon`, `categoryIcon`, `defaultImage`):**
  - If an action omits `icon` (or leaves it empty), Studio falls back to that action's `defaultImage` in the Action Catalog.
  - Category icons in the Action Catalog resolve in order: `categoryIcon` → `icon` → the first action's `icon` / `defaultImage`.
  - All Wasm image/icon fields (`icon`, `categoryIcon`, `defaultImage`, and `pc_update_visual.image`) accept inline `data:image/*` data URIs or safe relative paths strictly inside the `.wasm` plugin's own directory. Paths using `..` escapes, symlink escapes outside the plugin folder, absolute paths, `file://`, `http(s)://`, or non-image `data:` schemes are rejected and stripped to `""`.

### 5.2 Sections

Both slot `settingsSchema` and plugin-level `globalSettingsSchema` group fields by `"section"` (matched case-insensitively):
- Canonical names: `"General"` (also default when omitted), `"Appearance"`, `"Trigger"`, `"Advanced"` (**collapsed by default** in Settings tabs).
- Any other non-empty `"section"` creates a custom accordion group with that title.

### 5.3 Declarative `SettingField` Types

Each field object supports: `key`, `label`, `type`, `section`, `default`, `placeholder`, `help`, `options` (`[{"label","value"}]`), `optionsKey`, `fileExtensions`, and `required`.

| `type` | UI Control | JSON Value / Shape | Notes |
| --- | --- | --- | --- |
| `"text"` | Single-line text entry | `string` | Default fallback if unknown type |
| `"multiline"` | Multi-line text area (4+ rows) | `string` | Good for message templates, JSON, scripts |
| `"password"` | Masked secret input + Clear checkbox | `string` | Stored in **OS keyring** (not `config.json`) in `globalSettingsSchema`. Omitting keeps existing secret; `""` clears it |
| `"number"` | Numeric entry | `int` or `float64` | Parsed cleanly back to numeric JSON |
| `"bool"` | Checkbox | `bool` | Toggle flag |
| `"select"` | Dropdown (`options`) or editable combo (`optionsKey`) | `string` | Static choices via `"options": [{"label","value"}]` |
| `"color"` | Color swatch picker + `#RRGGBB` input | `string` (`"#RRGGBB"`) | Validated as `#RRGGBB` hex |
| `"image"` | Image file chooser + preview/import | `string` | Imports assets cleanly |
| `"file"` | File picker + path entry | `string` | Optional `"fileExtensions": [".wav", ".mp3"]` filter |
| `"hotkey"` | Shortcut display + Capture button | `string` | Canonical hotkey chord (e.g. `"Ctrl+Shift+M"`) |
| `"info"` | Read-only wrapped label (+ optional **Copy** button) | `string` or `{"text": "...", "copyable": true}` | **Display-only.** Hidden completely (label, value, help) when text is empty. `{"text": "...", "copyable": true}` works both as a static schema `"default"` (inherited automatically by later plain-string updates) and as a dynamic runtime value; `copyable: true` renders bold monospace text with a **Copy** clipboard button |
| `"link"` | Clickable browser hyperlink | `"https://..."` or `{"url": "...", "label": "..."}` | **Display-only.** Only `"http"`/`"https"` URLs with non-empty host are rendered; empty/invalid hides the row. Uses `placeholder` as fallback label |
| `"button"` | Interactive Fyne button | `{"label": "...", "disabled": bool}` or `string` | **Display-only / action trigger.** Clicks invoke `pc_on_config_action` asynchronously with a 20s timeout |

### 5.4 Interactive Config Tab Semantics (`globalSettingsSchema`)

1. **Display-only fields stay intact on Save:** Clicking **Save** excludes `"info"`, `"link"`, and `"button"` keys from form collection while preserving existing plugin-managed metadata keys (e.g., `userId`, token expiry, status text, button state objects) stored in `PluginSettings`.
2. **OS Keyring protection:** Untouched `"password"` inputs are omitted on **Save** so existing keyring credentials are preserved automatically. Unsaved text typed into a `"password"` box is preserved across background UI refreshes.
3. **Optional-override `"password"` fields:** For credentials or IDs that ship with a built-in default (such as a public OAuth `clientId`), declare `"type": "password"` **without** `"default"` and provide an explanatory `"placeholder"` (e.g., `"Using built-in Client ID — enter to override"`). Inside the plugin, persist `""` via `pc_save_global_settings` when falling back to the built-in value so the default is not saved into the OS keyring as a user override.
4. **Live updates:** Calling `pc_save_global_settings` (for example after an interactive `pc_on_config_action` button click or during a background `pc_on_timer` auth poll) immediately updates `"info"`, `"link"`, `"button"`, and keyring status indicators on **Settings → `<Plugin>`** without losing form focus.
5. **Reset Defaults:** Resets editable fields to their schema `default` values, clears keyring secrets (`""`), omits display-only keys so the plugin can recompute its status inside `pc_on_global_settings`, and rebuilds the form.

---

## 6. Lifecycle & Best Practices

1. **Expect `willDisappear` on Plugin Disable:** Users can toggle optional plugins (including `.wasm` plugins) off in **Settings → Plugins**. PoundControl dispatches `willDisappear` for every visible slot/dial using that plugin, blocks further `Dispatch` and `pc_on_config_action` calls, and preserves user slot configs until re-enabled. Stop background polling timers cleanly when no active slots or auth flows require them.
2. **Respect the 5s Timer Budget:** `pc_on_timer` runs under a 5-second timeout (`defaultCallTimeout`). Avoid chaining multiple sequential outbound HTTP requests inside a single `pc_on_timer` callback — defer heavy status polling to the next timer tick or pass tight per-request `timeoutMs` values in `GuestHTTPRequest`.
3. **Keep Guest State Defensive Against Restarts:** Because **Reload Plugins** tears down and re-instantiates the Wasm runtime, lazily hydrate persisted auth/state via `pc_get_global_settings` on first call rather than relying solely on in-memory globals.
