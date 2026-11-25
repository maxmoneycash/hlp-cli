package ws

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
)

// Typed helpers built on Stream. They decode frames into domain types. Each
// returns a channel that closes when cancel is called, ctx ends, the stream
// closes, or the server rejects the subscription.

// pipe subscribes and converts events. Market data is latest-wins on the
// output too; lossless outputs block until read or cancelled.
func pipe[T any](ctx context.Context, s Stream, sub Subscription, lossless bool, conv func(Event) (T, bool)) (<-chan T, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	events, cancel, err := s.Subscribe(ctx, sub)
	if err != nil {
		return nil, func() {}, err
	}
	out := make(chan T, 16)
	stop := make(chan struct{})
	cancelAll := sync.OnceFunc(func() {
		close(stop)
		cancel()
	})
	go func() {
		defer close(out)
		for ev := range events {
			if ev.Err != nil {
				return
			}
			v, ok := conv(ev)
			if !ok {
				continue
			}
			if lossless {
				select {
				case out <- v:
				case <-stop:
					return
				case <-ctx.Done():
					return
				}
				continue
			}
			for {
				select {
				case out <- v:
				default:
					select { // drop the oldest pending value
					case <-out:
					default:
					}
					continue
				}
				break
			}
		}
	}()
	return out, cancelAll, nil
}

// Candles streams the open bar for coin/interval (replace if T equal, append if newer).
func Candles(ctx context.Context, s Stream, coin string, iv domain.Interval) (<-chan domain.Candle, func(), error) {
	if !iv.Valid() {
		return nil, func() {}, fmt.Errorf("ws: invalid interval %q", iv)
	}
	return pipe(ctx, s, Subscription{Type: "candle", Coin: coin, Interval: string(iv)}, false, func(ev Event) (domain.Candle, bool) {
		var w hyperliquid.WireCandle
		if json.Unmarshal(ev.Data, &w) != nil || w.OpenTime == 0 {
			return domain.Candle{}, false
		}
		return hyperliquid.ToCandle(w), true
	})
}

// Books streams l2Book with Fast:true (5 levels, ~0.5 s). nSigFigs 0 =
// full precision.
func Books(ctx context.Context, s Stream, coin string, nSigFigs int) (<-chan domain.Book, func(), error) {
	return BooksDepth(ctx, s, coin, nSigFigs, 5)
}

// BooksDepth uses the full-depth feed for more than five levels. Fast l2Book
// always contains only five; it must not replace a deeper snapshot.
func BooksDepth(ctx context.Context, s Stream, coin string, nSigFigs, depth int) (<-chan domain.Book, func(), error) {
	return pipe(ctx, s, Subscription{Type: "l2Book", Coin: coin, NSigFigs: nSigFigs, Fast: depth <= 5}, false, func(ev Event) (domain.Book, bool) {
		var b hyperliquid.L2Book
		if json.Unmarshal(ev.Data, &b) != nil {
			return domain.Book{}, false
		}
		return hyperliquid.ToBook(b), true
	})
}

// BBO streams best bid/offer (~9/s) as a one-level Book (a side may be empty).
func BBO(ctx context.Context, s Stream, coin string) (<-chan domain.Book, func(), error) {
	return pipe(ctx, s, Subscription{Type: "bbo", Coin: coin}, false, func(ev Event) (domain.Book, bool) {
		var w struct {
			Coin string                    `json:"coin"`
			Time int64                     `json:"time"`
			BBO  [2]*hyperliquid.WireLevel `json:"bbo"`
		}
		if json.Unmarshal(ev.Data, &w) != nil {
			return domain.Book{}, false
		}
		b := domain.Book{Coin: w.Coin, Time: w.Time, Bids: []domain.Level{}, Asks: []domain.Level{}}
		if w.BBO[0] != nil {
			b.Bids = hyperliquid.ToLevels([]hyperliquid.WireLevel{*w.BBO[0]})
		}
		if w.BBO[1] != nil {
			b.Asks = hyperliquid.ToLevels([]hyperliquid.WireLevel{*w.BBO[1]})
		}
		return b, true
	})
}

// Trades streams public prints.
func Trades(ctx context.Context, s Stream, coin string) (<-chan []domain.Trade, func(), error) {
	return pipe(ctx, s, Subscription{Type: "trades", Coin: coin}, false, func(ev Event) ([]domain.Trade, bool) {
		var wt []hyperliquid.WireTrade
		if json.Unmarshal(ev.Data, &wt) != nil || len(wt) == 0 {
			return nil, false
		}
		out := make([]domain.Trade, len(wt))
		for i, t := range wt {
			out[i] = hyperliquid.ToTrade(t)
		}
		return out, true
	})
}

// TradeBatch is one trades frame: the prints in arrival order, the socket
// generation they came on (see Event.Gen) and the server's snapshot flag.
type TradeBatch struct {
	Trades   []domain.Trade
	Gen      int64
	Snapshot bool
	State    string // set instead of Trades on opt-in lifecycle events
}

// TradeBatches streams every trades frame of coin without dropping any
// (unlike Trades, which keeps the latest for a tape). A slow reader makes
// the queue grow instead of losing prints; Gen changes after a reconnect.
func TradeBatches(ctx context.Context, s Stream, coin string) (<-chan TradeBatch, func(), error) {
	return tradeBatches(ctx, s, coin, false)
}

// TradeBatchesWithStatus includes acknowledgments and disconnects in the same queue as prints.
func TradeBatchesWithStatus(ctx context.Context, s Stream, coin string) (<-chan TradeBatch, func(), error) {
	return tradeBatches(ctx, s, coin, true)
}

func tradeBatches(ctx context.Context, s Stream, coin string, status bool) (<-chan TradeBatch, func(), error) {
	return pipe(ctx, s, Subscription{Type: "trades", Coin: coin, Lossless: true, Status: status}, true, func(ev Event) (TradeBatch, bool) {
		if ev.State != "" {
			return TradeBatch{Gen: ev.Gen, State: ev.State}, true
		}
		var wt []hyperliquid.WireTrade
		if json.Unmarshal(ev.Data, &wt) != nil || len(wt) == 0 {
			return TradeBatch{}, false
		}
		out := make([]domain.Trade, len(wt))
		for i, t := range wt {
			out[i] = hyperliquid.ToTrade(t)
		}
		return TradeBatch{Trades: out, Gen: ev.Gen, Snapshot: ev.IsSnapshot}, true
	})
}

// AssetContexts streams a coin's mark, oracle, funding and day reference.
// Spot uses the same subscription, returned on activeSpotAssetCtx.
func AssetContexts(ctx context.Context, s Stream, coin string) (<-chan hyperliquid.AssetCtx, func(), error) {
	return pipe(ctx, s, Subscription{Type: "activeAssetCtx", Coin: coin}, false, func(ev Event) (hyperliquid.AssetCtx, bool) {
		var frame struct {
			Coin string               `json:"coin"`
			Ctx  hyperliquid.AssetCtx `json:"ctx"`
		}
		if json.Unmarshal(ev.Data, &frame) != nil || frame.Coin != coin || frame.Ctx.MarkPx.Float() <= 0 {
			return hyperliquid.AssetCtx{}, false
		}
		return frame.Ctx, true
	})
}

// FastCtx is one coin's entry in a fastAssetCtxs frame.
type FastCtx struct {
	MarkPx hyperliquid.Dec `json:"markPx"`
	MidPx  hyperliquid.Dec `json:"midPx"`
}

// DecodeFastAssetCtxs decodes a fastAssetCtxs payload: a JSON string of
// base64 raw-DEFLATE JSON {coin: {markPx?, midPx?}}. A plain JSON object is
// accepted too.
func DecodeFastAssetCtxs(data json.RawMessage) (map[string]FastCtx, error) {
	out := map[string]FastCtx{}
	d := bytes.TrimSpace(data)
	if len(d) > 0 && d[0] == '{' {
		err := json.Unmarshal(d, &out)
		return out, err
	}
	var s string
	if err := json.Unmarshal(d, &s); err != nil {
		return nil, fmt.Errorf("fastAssetCtxs: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("fastAssetCtxs base64: %w", err)
	}
	fr := flate.NewReader(bytes.NewReader(raw))
	defer fr.Close()
	js, err := io.ReadAll(io.LimitReader(fr, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("fastAssetCtxs inflate: %w", err)
	}
	if err := json.Unmarshal(js, &out); err != nil {
		return nil, fmt.Errorf("fastAssetCtxs json: %w", err)
	}
	return out, nil
}

// EncodeFastAssetCtxs is the inverse of DecodeFastAssetCtxs (tests, replay).
func EncodeFastAssetCtxs(m map[string]FastCtx) (json.RawMessage, error) {
	js, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	fw, _ := flate.NewWriter(&buf, flate.BestSpeed)
	if _, err := fw.Write(js); err != nil {
		return nil, err
	}
	if err := fw.Close(); err != nil {
		return nil, err
	}
	return json.Marshal(base64.StdEncoding.EncodeToString(buf.Bytes()))
}

// Mids streams fastAssetCtxs merged into a full coin -> mark map (all dexes).
// Each value is a fresh copy safe to keep. The mark falls back to the mid
// when a delta carries only midPx for a coin never seen with a mark.
func Mids(ctx context.Context, s Stream) (<-chan map[string]float64, func(), error) {
	state := map[string]float64{}
	return pipe(ctx, s, Subscription{Type: "fastAssetCtxs"}, false, func(ev Event) (map[string]float64, bool) {
		m, err := DecodeFastAssetCtxs(ev.Data)
		if err != nil || len(m) == 0 {
			return nil, false
		}
		for coin, x := range m {
			if x.MarkPx != "" {
				state[coin] = x.MarkPx.Float()
			} else if _, ok := state[coin]; !ok && x.MidPx != "" {
				state[coin] = x.MidPx.Float()
			}
		}
		out := make(map[string]float64, len(state))
		for k, v := range state {
			out[k] = v
		}
		return out, true
	})
}

// fillDedupeWindow bounds the remembered tids.
const fillDedupeWindow = 20000

// UserFills streams an address's fills (snapshot first, deduped by tid).
// After a reconnect the server resends a snapshot; only unseen fills pass.
func UserFills(ctx context.Context, s Stream, user string) (<-chan []domain.Fill, func(), error) {
	seen := map[uint64]bool{}
	var order []uint64
	return pipe(ctx, s, Subscription{Type: "userFills", User: strings.ToLower(user)}, true, func(ev Event) ([]domain.Fill, bool) {
		var w struct {
			Fills []hyperliquid.WireFill `json:"fills"`
		}
		if json.Unmarshal(ev.Data, &w) != nil {
			return nil, false
		}
		var out []domain.Fill
		for _, f := range w.Fills {
			if f.TID != 0 {
				if seen[f.TID] {
					continue
				}
				seen[f.TID] = true
				order = append(order, f.TID)
				if len(order) > fillDedupeWindow {
					delete(seen, order[0])
					order = order[1:]
				}
			}
			out = append(out, hyperliquid.ToFill(f))
		}
		return out, len(out) > 0
	})
}

// Positions streams allDexsClearinghouseState mapped to domain.Account
// (perp part only; spot/Earn are refreshed by REST).
func Positions(ctx context.Context, s Stream, user string) (<-chan domain.Account, func(), error) {
	addr := strings.ToLower(user)
	return pipe(ctx, s, Subscription{Type: "allDexsClearinghouseState", User: addr}, false, func(ev Event) (domain.Account, bool) {
		var w struct {
			User   string               `json:"user"`
			States [][2]json.RawMessage `json:"clearinghouseStates"`
		}
		if json.Unmarshal(ev.Data, &w) != nil {
			return domain.Account{}, false
		}
		states := make([]hyperliquid.DexState, 0, len(w.States))
		for _, pair := range w.States {
			var ds hyperliquid.DexState
			if json.Unmarshal(pair[0], &ds.Dex) != nil || json.Unmarshal(pair[1], &ds.State) != nil || ds.State.AssetPositions == nil {
				return domain.Account{}, false
			}
			// Invalid quantities must not become apparent closes in the mapper.
			for _, leg := range ds.State.AssetPositions {
				size, err := strconv.ParseFloat(string(leg.Position.Szi), 64)
				if leg.Position.Coin == "" || err != nil || math.IsNaN(size) || math.IsInf(size, 0) {
					return domain.Account{}, false
				}
			}
			states = append(states, ds)
		}
		if len(states) == 0 || (w.User != "" && !strings.EqualFold(w.User, addr)) {
			return domain.Account{}, false
		}
		account := hyperliquid.PerpAccount(addr, states)
		account.StreamGeneration = ev.Gen
		return account, true
	})
}
