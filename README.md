# multi-codex-proxy

OpenAI compatible proxy backed by many Codex OAuth accounts. Port of the RikkaHub Agent Codex stack to a Go daemon with TUI.

## Why

Sign in N ChatGPT accounts once, rotate them round robin, track quota from upstream headers, serve `/v1/responses` to any OpenAI client.

## Quick start

1. Run `make run` in `multi-codex-proxy`. Static build, `CGO_ENABLED=0` always.
2. Press `a` to sign in. Browser opens to OpenAI, callback lands on `127.0.0.1:1455`.
3. Press `s` to serve on `127.0.0.1:18789`. Port busy shows a clear error with a `--port` hint.
4. Point a client at it:
   `curl http://127.0.0.1:18789/v1/models`
   `curl -X POST http://127.0.0.1:18789/v1/responses -H 'Content-Type: application/json' -d '{"model":"gpt-5-codex","input":[{"role":"user","content":"hi"}]}'`

Headless: `make serve` or `go run . --serve --port 18789`.

## Errors

Every failure shows what broke plus a hint. Port busy suggests `--port`.
Expired logins say which account to reauth with `a`. Upstream hiccups say
whether a retry is safe. Params the upstream rejects as unsupported are
dropped with an automatic retry instead of failing. API errors return
`error.message` plus `error.hint`.

## TUI layout

Adapts to terminal size. Narrow portrait stacks one account per two lines
with short keys. Wide landscape splits list left and quota detail right.
Resize sends `WindowSizeMsg` and the view reflows at once.

## Endpoints

- `GET /health`
- `GET /v1/models` — upstream Codex list filtered to `visibility == list`
- `POST /v1/responses` — full passthrough, SSE when `stream:true`
- `POST /v1/chat/completions` — full translation: text, images, function tools
  with `call_id` preserved, `tool_choice`, usage, plus SSE chunk streaming

Upstream is `https://chatgpt.com/backend-api/codex` with `originator: codex_cli_rs`, `OpenAI-Beta: responses=experimental`, UA `codex_cli_rs/0.144.5`.

## Config

Lives in `$XDG_CONFIG_HOME/multi-codex-proxy` or `~/.config/multi-codex-proxy`:

- `config.json` — host, port
- `accounts.json` — accounts plus `nextAccountIndex`, mode `0600`

Tokens never print to logs. Delete `accounts.json` to sign out all.

## TUI keys

`a` add, `r` refresh one, `R` refresh all, `e` enable toggle, `d` delete, `s` serve on/off, `q` quit.

## Reference

RikkaHub Agent files mirrored here: `CodexAccount.kt`, `CodexAccountRepository.kt`, `CodexCredentialStore.kt`, `CodexProvider.kt`, `CodexOAuthManager.kt`, `CodexJson.kt`, `CodexProviderConfigure.kt`, `DataSourceModule.kt`.
