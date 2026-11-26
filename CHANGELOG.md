# Changelog

All notable changes to hlp. The format follows [Keep a Changelog](https://keepachangelog.com/),
and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.5] - 2026-11-26

### Added
- `/dash markets|vaults|hlp|strategies|research [coin]`: full-screen grids of live tiles. `1`-`9` opens a tile, `tab` moves to the next dashboard.

### Fixed
- Backtest price panels pick the widest candles that fit, so they span the panel.

## [0.1.4] - 2026-11-25

### Fixed
- Backtest price panels span the full width (two columns per candle).
- The arena race chart fills a full-screen pane.

## [0.1.3] - 2026-11-25

### Changed
- The accent is Hyperliquid mint (#97fce4) instead of orange; the paper theme uses dark teal.

### Fixed
- Quant strategy cards no longer print colour codes inside market names.

## [0.1.2] - 2026-11-24

### Fixed
- Release uploads point at the renamed repository (v0.1.1 has no binaries).

## [0.1.1] - 2026-11-24

### Changed
- The repository and Go module moved to `github.com/maxmoneycash/hlp-cli`.

## [0.1.0] - 2026-11-24

### Added
- Chat-style shell with inline cards, slash commands, command palette and full-screen panes.
- Candlestick charts (eighth-block bodies, braille MAs, volume, crosshair) for perps, spot and HIP-3.
- Vault screener, tearsheets with checklist, compare, and HLP positioning.
- Thesis and Pine backtests (`hlp backtest`, `hlp pine check|run`), paper trading and the arena.
- JSON contract, `hlp schema`, `hlp mcp`, and `hlp auth` for API-wallet keys.
- Dry-run trading with risk caps and confirmation (live sending is gated off in this build).
