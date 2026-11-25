// Package hyperliquid is hlp's own thin client for Hyperliquid's public read
// surface: POST /info, the stats-data S3 vault list, and the asset registry.
// Subpackages: ws (WebSocket streams) and exchange (signing + /exchange).
//
// Decisions:
//   - No SDK dependency at runtime. sonirico/go-hyperliquid may be used only
//     as a test oracle behind the `oracle` build tag.
//   - Wire types keep Hyperliquid's decimal strings (type Dec). Conversion
//     to domain.* float64 types happens in reader.go for display/analytics.
//   - Every request goes through a weight-aware token bucket (1200/min/IP).
//   - Unknown coins are rejected locally (HTTP 500 "null" upstream; on the
//     WebSocket they kill the whole socket).
//   - Candle wire structs declare BOTH "t" and "T" (encoding/json is
//     case-insensitive, so one field would swallow both).
package hyperliquid
