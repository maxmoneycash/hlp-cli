# Security policy

hlp can sign Hyperliquid actions, so we treat security reports as the highest priority.

## Reporting a vulnerability

Please **do not open a public issue**. Use GitHub's private vulnerability reporting on this
repository (Security tab, "Report a vulnerability"). Include a description, the affected version
(`hlp version`), and steps to reproduce. We aim to acknowledge reports within 72 hours.

## How hlp handles keys

- hlp signs only with a Hyperliquid **API (agent) wallet** key. Agent wallets can trade but cannot
  withdraw. hlp never asks for, stores or uses your master wallet key.
- The key comes from `HLP_AGENT_KEY` or the OS keychain (`hlp auth set` reads it from stdin). It is
  never read from argv or config files and never written to logs; `hlp auth status` shows only the
  public address it signs as.
- This build is **dry-run only**: it never sends to `/exchange`. Dry-run envelopes are signed with
  an `expiresAfter` already in the past and their signature is withheld from every output (JSON,
  MCP, the assistant), so a preview can never be replayed as a live order.
- Every order passes risk caps (notional, leverage, allowed coins, daily loss). The in-app
  assistant cannot pre-approve its own orders; MCP trading tools exist only when both
  `mcp.allow_trading` and `--allow-trading` are set.
- No telemetry. hlp talks only to Hyperliquid's public API, hlp.xyz's public API and, if you
  configure one, your LLM provider.

## Scope

In scope: anything that could leak a key, sign or send an action the user did not confirm, bypass
risk caps, or let untrusted data (vault names, model output, MCP input) act as instructions.
