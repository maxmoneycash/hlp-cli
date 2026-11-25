# Contributing to hlp

Thanks for helping. hlp is a Go 1.27 program built on Charm's Bubble Tea v2, Lip Gloss v2 and Fang.
Good first contributions: a Pine built-in that `hlp pine check` reports as unsupported, a new example
strategy or thesis, a theme, a translation of the README, or a bug report with the command and its
`--json` output.

## Build and test

```sh
make build        # bin/hlp (uses /usr/local/opt/go/bin/go when present; override with make GO=go)
make check        # gofmt + go vet + unit tests; must stay green
make golden       # regenerate chart/card/theme goldens after an intentional rendering change
make test-live    # opt-in read-only smoke tests against public endpoints (HLP_LIVE=1)
make chart-preview  # write ANSI chart frames to eyeball: cat the file it prints
```

## Rules every change follows

- **Unit tests never touch the network.** Use the recorded fixtures in
  `testdata/fixtures/` and serve them with `httptest.Server`. Live tests are named `TestLive*`
  and skip unless `HLP_LIVE=1`; keep them few and read-only.
- **Nothing posts to `/exchange`.** This build is dry-run only (`liveSend` in
  `internal/hyperliquid/exchange` and `liveTrading` in `internal/core` are `false`). Never sign with a
  real key in tests: the only key allowed is the Hyperliquid Python SDK's public test key `0x0123…0123`.
- **Keys never go in argv, config files or logs.** Use `config.Secret`, which redacts itself.
- **No paid API calls in tests.** LLM code is tested with `httptest` fakes.
- **Polling:** WebSocket first; HTTP polling at most every 5 s and only while visible. hlp.xyz is
  rate-limited on the client (60 requests a minute) and cached; keep it that way.
- Doc comments on exported identifiers, and a test for every exported function.

## Adding a tool

Every capability is one `core.Tool` in `internal/core/catalog.go`. A tool declared once becomes a
CLI command, a slash command in the shell, an MCP tool and an assistant tool:

1. Add an input struct with `json` and `jsonschema` tags (the schema and the CLI flags come from it).
2. Add the `Tool` entry: `Name` (snake_case MCP name), `Command`, `Slash`, `Positional`, `Group`,
   an agent-quality `Description` and a one-line `Summary`.
3. Register its output type and card in `internal/core/tools_meta.go`.
4. Mutating tools set `Mutating: true` and go through `authorize` and `post` in
   `internal/core/tools_trade.go`: guard, confirm, gate. Never bypass them.
5. Add fixture-backed tests in `internal/core/tools_test.go`.

## Examples

Example strategies live in `examples/` and are described in `examples/README.md`. They must be
original (MIT, written for hlp, not copied from TradingView or elsewhere), must pass
`hlp pine check`, and must not promise returns in comments. Add a line to `examples/README.md` for
each new file.

## Docs and demo GIFs

- `README.md` only claims what `make build && ./bin/hlp …` does today. If a command in the README
  changes, update the README in the same PR. Run every README command you touched before opening it.
- Demo GIFs are recorded with [VHS](https://github.com/charmbracelet/vhs) from `docs/tapes/*.tape`
  by a maintainer (`make gifs`). The shared look is in `docs/tapes/settings.tape`. Record on
  real mainnet public data, never with a key in the environment or an address that is not already
  public, then `gifsicle -O3` and keep each GIF under 2 MB.
- Do not commit rendered frame dumps or MP4 masters; only the final `docs/gifs/*.gif|png` and the
  screenshots in `docs/images/`.

## Pull requests

Keep PRs focused, describe the user-visible change, and include before/after output for rendering
changes (`make golden` diffs are reviewed like code). Security issues go to [SECURITY.md](SECURITY.md),
not public issues.
