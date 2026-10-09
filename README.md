# hive — slopconductor TUI + OpenCode Go probe

Bubble Tea TUI that exercises **every** slopconductor daemon capability and
**every** OpenCode Go wire shape. Daemon-owned execution only: closing hive
never cancels work; `x` in Turns (or send-then-cancel) is the explicit
cancellation path, matching `slop turn cancel`.

## Run

```sh
go run .                 # TUI (uses SLOP_DAEMON_URL / SLOP_TOKEN_FILE or loopback defaults)
go run . --smoke-daemon  # headless daemon read-surface check (needs daemon up)
go run . --probe-go      # headless direct provider check (needs OPENCODE_GO_API_KEY)
```

Go toolchain: `/usr/local/go/bin/go` (add to PATH if missing).

Provider key resolution: `OPENCODE_GO_API_KEY` env, else
`~/projects/slopconductor/.env`. Never logged; `set-key` input uses a
password field and is only sent via `PUT /v1/providers/{id}/api-key`.

## Tabs (1–7)

| Tab | Covers |
| --- | ------ |
| Status | `GET /v1/health` (anon, version check), `GET /v1/node`, `GET /v1/models`, `GET /v1/capabilities`, `GET /v1/providers` |
| Sessions | `GET /v1/sessions`, `GET /v1/sessions/{id}`, `POST /v1/sessions` (new command ID per mutation), history preview |
| Chat | `POST /v1/sessions/{id}/messages` + `GET /v1/sessions/{id}/events?after=&follow=` NDJSON (durable/delta/heartbeat), canonical history reconcile |
| Turns | `GET /v1/turns/{id}`, `POST /v1/turns/{id}/cancel`, `GET /v1/turns/{id}/requests`, `GET /v1/turns/{id}/tools`, `GET /v1/messages/{id}`, `GET /v1/tools/{id}` |
| Artifacts | `GET /v1/artifacts/{id}`, `GET /v1/artifacts/{id}/content` with size+SHA-256 verification before publish |
| Providers | `GET /v1/providers`, `PUT /v1/providers/{p}/api-key`, `POST /v1/providers/codex/login`, `GET /v1/providers/codex/login/{id}` |
| GoDirect | direct `GET /models` + `POST /chat/completions`, `/responses`, `/messages` (stream + non-stream) across the 32-model catalog |

## Keys

`1-7` tabs · `tab` cycle · `r` refresh · `enter` run/show/send · `c` new
session · `e` edit · `f` follow · `x` cancel · `d` download artifact ·
`l`/`L` codex login/status · `g` live models · `s` stream toggle ·
`[`/`]` focus cycle · `F1`/`F2`/`F3` select an input · `esc` blur ·
`pgup`/`pgdown` scroll · `home`/`end` first/last page · `?` help ·
`q`/`ctrl+c` quit.

`tab` and `shift+tab` switch tabs even while editing. Letter and number
shortcuts apply after `esc` blurs the input; `e` resumes editing. In Chat,
`F1` selects the session and `F2` selects the message. In Providers, `F1`,
`F2`, and `F3` select the provider, password, and login ID. Enter in the
login field checks login status. `alt+enter` inserts a newline in a prompt.

Failed sends retain the draft and any newly created session. Event observers
close when changing sessions or quitting, while daemon work continues.
Committed snapshots reconcile the chat history, including paginated history;
`f` reconnects a closed observer. Direct provider responses require an explicit
terminal outcome; incomplete or malformed streams never report completion.

## Check

```sh
go test -race ./...
go vet ./...
go build ./...
```

Regression tests use local HTTP fixtures and do not require provider credentials.
