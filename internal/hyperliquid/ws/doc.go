// Package ws is the Hyperliquid WebSocket client: one multiplexed socket per
// purpose, typed subscriptions, ping/backoff/resubscribe, and fastAssetCtxs
// inflation.
//
// Must-haves (all LIVE-verified gotchas):
//   - Validate coins BEFORE subscribing: an unknown coin closes the socket
//     with 1006 and every other subscription dies (use a Validator).
//   - Ping {"method":"ping"} every 30-50 s when idle (server closes at 60 s).
//   - Match acks on the server-normalized subscription (it fills defaults
//     like nSigFigs:null, fast:false, dex:"").
//   - Reconnect with jittered backoff 0.5 s -> 15 s, <= 30 new conns/min,
//     resubscribe everything; dedupe user fills by tid after the snapshot.
//   - webData2 is rejected by the server; never subscribe to it.
//   - l2Book default pushes every ~10 s; panes use Fast:true (5 levels, ~0.5 s) + bbo.
//   - fastAssetCtxs frames are base64 raw-DEFLATE JSON; Go: flate.NewReader.
//   - Ignore frames without "channel"; the candle channel sends ONE object.
//   - At most 10 unique users across user subscriptions per connection set.
package ws
