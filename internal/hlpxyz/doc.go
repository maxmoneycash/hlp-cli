// Package hlpxyz is the client for hlp.xyz's public API (www.hlp.xyz/api/*)
// and the mapper from its responses to domain vault types.
//
// Test fixtures: testdata/fixtures/hlpxyz/.
//
// Rules:
//   - Public GET endpoints only. Never call /api/auth, /api/user?action=record,
//     idxInv*, manage*, or anything needing a Bearer token.
//   - Stay under 120 req/window: own token bucket at 90/min, honor 429 and
//     x-ratelimit-reset, disk cache (60 s list, 5 min detail).
//   - Polling (watch mode) no faster than every 5 s and only while visible
//     (owner rule); default 15 s.
//   - Always label the share-price model (domain.SPv6 vs domain.SPLive).
//   - HLP: take headline numbers from /live and v6status, never the stale
//     summary row (lastUpdated 2026-04-28).
//   - hlp.xyz is optional: config hlpxyz.disabled=true must leave every
//     command working from Hyperliquid data alone.
//
// Layout: client.go (Options, the API methods), transport.go (token bucket,
// 429/x-ratelimit-reset, retries, gzip, memory + disk cache with stale
// fallback, the forbidden-endpoint guard), list.go (VaultList and the pure
// MapVaultList join), detail.go (ToDetail, FetchDetail), t1.go (DecodeT1),
// index.go (IndexCurves, IndexNAV, IndexMembers).
//
// Freshness: every cached body is served for its TTL; WithoutCache skips
// the read for poll loops. If the network fails, a cached copy up to 24 h
// old is served instead (VaultList reports it as Meta.Stale).
package hlpxyz
