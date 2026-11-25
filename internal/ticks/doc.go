// Package ticks builds tick, volume and range bars from Hyperliquid's
// public trades stream and keeps a local, size-capped trade history so
// those bars have a past.
//
// Hyperliquid has no free trade history (recentTrades returns only the
// last few prints; the S3 archive is requester-pays), so history is what
// this machine recorded while hlp ran: seeded with recentTrades, then every
// print from the trades WebSocket. Trades are deduped by tid, never
// invented, and a gap (a reconnect or a restart that cannot prove it missed
// nothing) is marked on the next trade so no bar spans it.
//
// Bars restart at 00:00 UTC each day (a session anchor): a bar never spans
// two days, and a day's bars come out the same however much history is
// loaded or pruned.
package ticks
