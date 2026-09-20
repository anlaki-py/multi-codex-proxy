# AGENTS.md

Purpose: Go proxy that pools Codex OAuth accounts and serves OpenAI routes.

## Dir map

- `main.go` — flags, config load, TUI or headless serve
- `internal/config` — home `.config` load, fail fast checks
- `internal/codex` — constants, account types, JWT identity, usage parsers, reasoning map
- `internal/accounts` — 0600 JSON store plus mutex repository with round robin
- `internal/auth` — PKCE browser login on 1455/1457
- `internal/proxy` — `/v1/models`, `/v1/responses`, `/v1/chat/completions` shim
- `internal/tui` — Bubble Tea UI, no business logic beyond calls

## Setup

`make run` for TUI. `make serve` for headless. All targets force
`CGO_ENABLED=0` for static binaries. Never call `go run` or `go build`
directly, use make.

## Test

`make build`
`make vet`
`make test``

## Limits

- No image gen upstream, text only in chat shim.
- Chat stream returns a marker, use `/v1/responses` stream for SSE.
- Token file is 0600 JSON, not Keystore. Do not commit it.
- Body cap is 10 MiB per request.

## Done means

Build plus vet plus tests pass, TUI opens with zero accounts and `a` starts login, `/health` and `/v1/models` answer when serving.
