# vscode-load-llama

Watches VS Code Copilot chat inputs via CDP (port 9222) and pre-loads the selected model on the local llama.cpp server.

## How it works
- Polls `http://127.0.0.1:9222/json/version` until VS Code (started with
  `--remote-debugging-port=9222`) is reachable, then rescans `/json/list`
  every 2s for workbench page targets.
- Per window: page-level CDP WebSocket + injected versioned MutationObserver
  (50ms debounce) pushes `{input, model, effort, mode}` via a runtime binding.
- On every non-empty input event: looks up the model in the VS Code user
  `settings.json` (`oaicopilot.models`, loaded once at startup) and sends
  `POST {server root}/models/load` with
  `{"model": "<id>"}` — the path is relative to the server ROOT
  (baseUrl `http://abc.com/v1` → `http://abc.com/models/load`).
- 30s per-model cooldown (timestamp refreshed when the request is sent).
- Load requests respect VS Code's `http.proxy` / `http.noProxy` settings
  (noProxy entries are host suffixes); with no proxy configured the
  client connects directly.
- Response handling: `400 "model is already running"` is treated as
  normal (model already loaded). Endpoints answering `404/405/401/403`
  are remembered as non-llama.cpp and skipped entirely afterwards, so
  remote API providers (openrouter, etc.) are only probed once.
- Survives VS Code fully exiting and restarting (state machine:
  waiting → monitoring → disconnected → ...).

## Build
| platform | command | output |
|---|---|---|
| Windows | `.\build.ps1` | `vscode-load-llama.exe` |
| Linux / macOS | `go build -trimpath -o vscode-load-llama .` | `vscode-load-llama` |

On Windows the exe is a console-free GUI process (`-H windowsgui`); on
Linux/macOS it's a normal binary you run in the background
(e.g. `nohup ./vscode-load-llama &`). `build.ps1` runs
`go build -trimpath -ldflags "-H windowsgui" -o vscode-load-llama.exe .`.

## Setup
Enable the CDP debugging port in VS Code (one-time):
1. Open the Command Palette (`Ctrl+Shift+P`) and run
   `Preferences: Configure Runtime Arguments` — this opens `argv.json`.
2. Add `"remote-debugging-port": "9222"` to the JSON object.
3. Restart VS Code.

(Alternatively, launch VS Code with `--remote-debugging-port=9222` on the CLI.)

## Flags
| flag | default | meaning |
|---|---|---|
| `-cdp` | `127.0.0.1:9222` | CDP HTTP address |
| `-settings` | VS Code user `settings.json` (see below) | settings file (loaded once) |
| `-cooldown` | `30s` | per-model load cooldown |
| `-log` | `<tempdir>/vscode-load-llama/app.log` | log file |
| `-verbose` | `false` | debug-level logging |

Default `settings.json` location per platform:

| platform | path |
|---|---|
| Windows | `%APPDATA%\Code\User\settings.json` |
| Linux | `~/.config/Code/User/settings.json` |
| macOS | `~/Library/Application Support/Code/User/settings.json` |


## Caveats
- **Single instance only.** The CDP binding is a page global; a second
  running instance overrides the first one's binding.
- VS Code must expose the CDP port (see **Setup**); without it the tool
  stays in the waiting state.
- `settings.json` is not hot-reloaded; restart the exe after editing it.
