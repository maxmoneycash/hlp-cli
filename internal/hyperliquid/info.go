package hyperliquid

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// Typed /info calls. Each is a thin wrapper over Client.Info with the exact
// request body Hyperliquid expects. dex "" = native dex.

// infoReq is the generic request body; empty fields are omitted.
type infoReq struct {
	Type            string `json:"type"`
	Dex             string `json:"dex,omitempty"`
	Coin            string `json:"coin,omitempty"`
	User            string `json:"user,omitempty"`
	VaultAddress    string `json:"vaultAddress,omitempty"`
	StartTime       *int64 `json:"startTime,omitempty"`
	EndTime         *int64 `json:"endTime,omitempty"`
	NSigFigs        *int   `json:"nSigFigs,omitempty"`
	Mantissa        *int   `json:"mantissa,omitempty"`
	AggregateByTime bool   `json:"aggregateByTime,omitempty"`
	Req             any    `json:"req,omitempty"`
}

func i64(v int64) *int64 { return &v }

// Meta returns the perp universe of one dex.
func (c *Client) Meta(ctx context.Context, dex string) (Meta, error) {
	var m Meta
	err := c.Info(ctx, infoReq{Type: "meta", Dex: dex}, &m)
	return m, err
}

// metaCtxs is one memoized metaAndAssetCtxs answer.
type metaCtxs struct {
	meta Meta
	ctxs []AssetCtx
}

// MetaAndAssetCtxs returns meta and ctxs aligned by index. Concurrent and
// repeated calls within AssetCtxTTL share one request. Treat the result as
// read-only (the ctx slice is a copy; Meta's slices are shared).
func (c *Client) MetaAndAssetCtxs(ctx context.Context, dex string) (Meta, []AssetCtx, error) {
	v, err := c.memoDo(ctx, "metaAndAssetCtxs:"+dex, AssetCtxTTL, func() (any, error) {
		m, cx, err := c.metaAndAssetCtxs(ctx, dex)
		return metaCtxs{m, cx}, err
	})
	if err != nil {
		return Meta{}, nil, err
	}
	mc := v.(metaCtxs)
	return mc.meta, append([]AssetCtx(nil), mc.ctxs...), nil
}

func (c *Client) metaAndAssetCtxs(ctx context.Context, dex string) (Meta, []AssetCtx, error) {
	var raw [2]json.RawMessage
	if err := c.Info(ctx, infoReq{Type: "metaAndAssetCtxs", Dex: dex}, &raw); err != nil {
		return Meta{}, nil, err
	}
	var m Meta
	var ctxs []AssetCtx
	if err := json.Unmarshal(raw[0], &m); err != nil {
		return Meta{}, nil, &APIError{Status: 200, Code: "DECODE", Body: err.Error()}
	}
	if err := json.Unmarshal(raw[1], &ctxs); err != nil {
		return Meta{}, nil, &APIError{Status: 200, Code: "DECODE", Body: err.Error()}
	}
	return m, ctxs, nil
}

type spotCtxs struct {
	meta SpotMeta
	ctxs []SpotAssetCtx
}

// SpotMetaAndAssetCtxs returns spotMeta and the per-pair contexts aligned
// with spotMeta.universe (memoized like MetaAndAssetCtxs).
func (c *Client) SpotMetaAndAssetCtxs(ctx context.Context) (SpotMeta, []SpotAssetCtx, error) {
	v, err := c.memoDo(ctx, "spotMetaAndAssetCtxs", AssetCtxTTL, func() (any, error) {
		m, cx, err := c.spotMetaAndAssetCtxs(ctx)
		return spotCtxs{m, cx}, err
	})
	if err != nil {
		return SpotMeta{}, nil, err
	}
	sc := v.(spotCtxs)
	return sc.meta, append([]SpotAssetCtx(nil), sc.ctxs...), nil
}

func (c *Client) spotMetaAndAssetCtxs(ctx context.Context) (SpotMeta, []SpotAssetCtx, error) {
	var raw [2]json.RawMessage
	if err := c.Info(ctx, infoReq{Type: "spotMetaAndAssetCtxs"}, &raw); err != nil {
		return SpotMeta{}, nil, err
	}
	var m SpotMeta
	var ctxs []SpotAssetCtx
	if err := json.Unmarshal(raw[0], &m); err != nil {
		return SpotMeta{}, nil, &APIError{Status: 200, Code: "DECODE", Body: err.Error()}
	}
	if err := json.Unmarshal(raw[1], &ctxs); err != nil {
		return SpotMeta{}, nil, &APIError{Status: 200, Code: "DECODE", Body: err.Error()}
	}
	return m, ctxs, nil
}

// AllPerpMetas returns every dex's meta in perpDexs order (index 0 = native).
func (c *Client) AllPerpMetas(ctx context.Context) ([]Meta, error) {
	var out []Meta
	err := c.Info(ctx, infoReq{Type: "allPerpMetas"}, &out)
	return out, err
}

// PerpDexs returns the dex list; index 0 is nil (native dex).
func (c *Client) PerpDexs(ctx context.Context) ([]*PerpDex, error) {
	var out []*PerpDex
	err := c.Info(ctx, infoReq{Type: "perpDexs"}, &out)
	return out, err
}

// SpotMeta returns spot tokens and pairs.
func (c *Client) SpotMeta(ctx context.Context) (SpotMeta, error) {
	var m SpotMeta
	err := c.Info(ctx, infoReq{Type: "spotMeta"}, &m)
	return m, err
}

// PerpCategories returns [coin, category] pairs (stocks, crypto, ...).
func (c *Client) PerpCategories(ctx context.Context) ([]PerpCategory, error) {
	var out []PerpCategory
	err := c.Info(ctx, infoReq{Type: "perpCategories"}, &out)
	return out, err
}

// PerpAnnotation returns the description and keywords for one perp coin.
func (c *Client) PerpAnnotation(ctx context.Context, coin string) (PerpAnnotation, error) {
	var out PerpAnnotation
	err := c.Info(ctx, infoReq{Type: "perpAnnotation", Coin: coin}, &out)
	return out, err
}

// OutcomeMeta returns HIP-4 outcome markets.
func (c *Client) OutcomeMeta(ctx context.Context) (OutcomeMeta, error) {
	var out OutcomeMeta
	err := c.Info(ctx, infoReq{Type: "outcomeMeta"}, &out)
	return out, err
}

// AllMids returns coin -> mid. HIP-3 mids need dex="xyz" etc. Calls
// within AllMidsTTL share one request; the map returned is the caller's.
func (c *Client) AllMids(ctx context.Context, dex string) (map[string]Dec, error) {
	v, err := c.memoDo(ctx, "allMids:"+dex, AllMidsTTL, func() (any, error) {
		out := map[string]Dec{}
		err := c.Info(ctx, infoReq{Type: "allMids", Dex: dex}, &out)
		return out, err
	})
	if err != nil {
		return map[string]Dec{}, err
	}
	src := v.(map[string]Dec)
	out := make(map[string]Dec, len(src))
	for k, d := range src {
		out[k] = d
	}
	return out, nil
}

// BookOpts selects l2Book aggregation. NSigFigs 0 = full precision;
// Mantissa (1,2,5) is valid only with NSigFigs 5.
type BookOpts struct {
	NSigFigs int
	Mantissa int
}

// Validate checks the aggregation against the server's accepted values.
func (o BookOpts) Validate() error {
	switch o.NSigFigs {
	case 0, 2, 3, 4, 5:
	default:
		return &APIError{Code: "BAD_REQUEST", Body: fmt.Sprintf("nSigFigs must be 2-5 or unset, got %d", o.NSigFigs)}
	}
	if o.Mantissa != 0 {
		if o.NSigFigs != 5 {
			return &APIError{Code: "BAD_REQUEST", Body: "mantissa is only valid with nSigFigs 5"}
		}
		if o.Mantissa != 1 && o.Mantissa != 2 && o.Mantissa != 5 {
			return &APIError{Code: "BAD_REQUEST", Body: fmt.Sprintf("mantissa must be 1, 2 or 5, got %d", o.Mantissa)}
		}
	}
	return nil
}

// L2Book returns an order book snapshot (<= 20 levels per side).
func (c *Client) L2Book(ctx context.Context, coin string, o BookOpts) (L2Book, error) {
	if err := o.Validate(); err != nil {
		return L2Book{}, err
	}
	r := infoReq{Type: "l2Book", Coin: coin}
	if o.NSigFigs != 0 {
		r.NSigFigs = &o.NSigFigs
	}
	if o.Mantissa != 0 {
		r.Mantissa = &o.Mantissa
	}
	var b L2Book
	err := c.Info(ctx, r, &b)
	return b, err
}

type candleReq struct {
	Coin      string `json:"coin"`
	Interval  string `json:"interval"`
	StartTime int64  `json:"startTime"`
	EndTime   *int64 `json:"endTime,omitempty"`
}

// CandleSnapshot fetches candles in [start, end] ms (end 0 = now). Only the
// most recent ~5000 candles per interval exist. The open candle is included.
func (c *Client) CandleSnapshot(ctx context.Context, coin string, iv domain.Interval, start, end int64) ([]WireCandle, error) {
	if !iv.Valid() {
		return nil, &APIError{Code: "BAD_REQUEST", Body: fmt.Sprintf("invalid interval %q", iv)}
	}
	cr := candleReq{Coin: coin, Interval: string(iv), StartTime: start}
	if end > 0 {
		cr.EndTime = &end
	}
	var out []WireCandle
	err := c.Info(ctx, infoReq{Type: "candleSnapshot", Req: cr}, &out)
	return out, err
}

// RecentTrades returns the last 10 trades.
func (c *Client) RecentTrades(ctx context.Context, coin string) ([]WireTrade, error) {
	var out []WireTrade
	err := c.Info(ctx, infoReq{Type: "recentTrades", Coin: coin}, &out)
	return out, err
}

// fundingPage is the server's page size for fundingHistory.
const fundingPage = 500

// maxPages bounds every paginated loop.
const maxPages = 200

// fundingWindow is the span of one cached funding page: 500 hourly prints.
const fundingWindow = int64(fundingPage) * 3600_000

// FundingHistory returns funding prints in [start, end] (end 0 = now),
// oldest first. Requests are aligned to fixed 500-hour windows so that
// windows entirely in the past are immutable and cached on disk under
// Options.CacheDir: a repeated backtest over the same months costs no
// weight (each window costs ~45 weight live, so a year of one coin is
// ~800 weight, most of a minute's budget).
func (c *Client) FundingHistory(ctx context.Context, coin string, start, end int64) ([]WireFunding, error) {
	now := c.nowMs()
	if end <= 0 || end > now {
		end = now
	}
	if start > end {
		return nil, nil
	}
	var all []WireFunding
	for w := start - mod(start, fundingWindow); w <= end; w += fundingWindow {
		wEnd := w + fundingWindow - 1
		page, err := c.fundingWindow(ctx, coin, w, wEnd, wEnd < now-3600_000)
		if err != nil {
			return dedupeFunding(filterFunding(all, start, end)), err
		}
		all = append(all, page...)
	}
	return dedupeFunding(filterFunding(all, start, end)), nil
}

// fundingWindow fetches [from, to] (paging inside the window if the server
// returns a full page early) and caches it when final.
func (c *Client) fundingWindow(ctx context.Context, coin string, from, to int64, final bool) ([]WireFunding, error) {
	path := c.fundingCachePath(coin, from)
	if final && path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var cached []WireFunding
			if json.Unmarshal(b, &cached) == nil {
				return cached, nil
			}
		}
	}
	var all []WireFunding
	cursor := from
	for page := 0; page < maxPages; page++ {
		var out []WireFunding
		if err := c.Info(ctx, infoReq{Type: "fundingHistory", Coin: coin, StartTime: i64(cursor), EndTime: i64(to)}, &out); err != nil {
			return nil, err
		}
		all = append(all, out...)
		if len(out) < fundingPage {
			break
		}
		last := out[len(out)-1].Time
		// A full page of hourly prints normally covers the whole window.
		if last < cursor || last >= to-3600_000 {
			break
		}
		cursor = last + 1
	}
	if final && path != "" {
		if b, err := json.Marshal(all); err == nil {
			_ = writeFileAtomic(path, b)
		}
	}
	return all, nil
}

func (c *Client) fundingCachePath(coin string, from int64) string {
	if c.opts.CacheDir == "" {
		return ""
	}
	safe := strings.NewReplacer("/", "_", ":", "_", "@", "_", "\\", "_").Replace(coin)
	return filepath.Join(c.opts.CacheDir, "funding", safe, strconv.FormatInt(from, 10)+".json")
}

func filterFunding(in []WireFunding, start, end int64) []WireFunding {
	out := in[:0]
	for _, f := range in {
		if f.Time >= start && f.Time <= end {
			out = append(out, f)
		}
	}
	return out
}

func mod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

func dedupeFunding(in []WireFunding) []WireFunding {
	sort.SliceStable(in, func(i, j int) bool { return in[i].Time < in[j].Time })
	out := in[:0]
	var last int64 = -1
	for _, f := range in {
		if f.Time == last {
			continue
		}
		last = f.Time
		out = append(out, f)
	}
	return out
}

// ClearinghouseState returns one dex's perp account state.
func (c *Client) ClearinghouseState(ctx context.Context, user, dex string) (ClearinghouseState, error) {
	var s ClearinghouseState
	err := c.Info(ctx, infoReq{Type: "clearinghouseState", User: user, Dex: dex}, &s)
	return s, err
}

// SpotClearinghouseState returns spot balances.
func (c *Client) SpotClearinghouseState(ctx context.Context, user string) ([]SpotBalance, error) {
	var s struct {
		Balances []SpotBalance `json:"balances"`
	}
	err := c.Info(ctx, infoReq{Type: "spotClearinghouseState", User: user}, &s)
	return s.Balances, err
}

// BorrowLendUserState returns Earn supply/borrow per token.
func (c *Client) BorrowLendUserState(ctx context.Context, user string) (BorrowLendUserState, error) {
	var s BorrowLendUserState
	err := c.Info(ctx, infoReq{Type: "borrowLendUserState", User: user}, &s)
	return s, err
}

// FrontendOpenOrders returns resting orders with trigger metadata.
func (c *Client) FrontendOpenOrders(ctx context.Context, user, dex string) ([]FrontendOrder, error) {
	var out []FrontendOrder
	err := c.Info(ctx, infoReq{Type: "frontendOpenOrders", User: user, Dex: dex}, &out)
	return out, err
}

// UserFills returns at most the 2000 most recent fills.
func (c *Client) UserFills(ctx context.Context, user string) ([]WireFill, error) {
	var out []WireFill
	err := c.Info(ctx, infoReq{Type: "userFills", User: user}, &out)
	return out, err
}

// fillsPage is the server's page size for userFillsByTime.
const fillsPage = 2000

// UserFillsByTime pages through [start,end]; only the 10k most recent are reachable.
func (c *Client) UserFillsByTime(ctx context.Context, user string, start, end int64) ([]WireFill, error) {
	var all []WireFill
	seen := map[uint64]bool{}
	cursor := start
	for page := 0; page < 10; page++ {
		r := infoReq{Type: "userFillsByTime", User: user, StartTime: i64(cursor)}
		if end > 0 {
			r.EndTime = i64(end)
		}
		var out []WireFill
		if err := c.Info(ctx, r, &out); err != nil {
			return all, err
		}
		var maxT int64 = -1
		for _, f := range out {
			if f.Time > maxT {
				maxT = f.Time
			}
			key := f.TID
			if key != 0 && seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, f)
		}
		if len(out) < fillsPage || maxT < cursor || (end > 0 && maxT >= end) {
			break
		}
		cursor = maxT + 1
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time < all[j].Time })
	return all, nil
}

// Portfolio returns the account value and PnL histories per window.
func (c *Client) Portfolio(ctx context.Context, user string) (Portfolio, error) {
	var p Portfolio
	err := c.Info(ctx, infoReq{Type: "portfolio", User: user}, &p)
	return p, err
}

// VaultDetails returns ErrNotFound when the server answers null.
func (c *Client) VaultDetails(ctx context.Context, vault, user string) (VaultDetails, error) {
	var v VaultDetails
	if err := c.Info(ctx, infoReq{Type: "vaultDetails", VaultAddress: vault, User: user}, &v); err != nil {
		return VaultDetails{}, err
	}
	if err := v.decodePortfolio(); err != nil {
		return v, &APIError{Status: 200, Code: "DECODE", Body: err.Error()}
	}
	return v, nil
}

// decodePortfolio fills Portfolio from RawPortfolio.
func (v *VaultDetails) decodePortfolio() error {
	if len(v.RawPortfolio) == 0 {
		return nil
	}
	b, err := json.Marshal(v.RawPortfolio)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &v.Portfolio)
}

// UserVaultEquities returns the vaults an address has deposited into.
func (c *Client) UserVaultEquities(ctx context.Context, user string) ([]UserVaultEquity, error) {
	var out []UserVaultEquity
	err := c.Info(ctx, infoReq{Type: "userVaultEquities", User: user}, &out)
	return out, err
}

// UserRole costs weight 60; results are cached per address for the life of
// the Client. Roles: user | agent | vault | subAccount | missing.
func (c *Client) UserRole(ctx context.Context, user string) (string, error) {
	c.roleMu.Lock()
	if r, ok := c.roles[user]; ok {
		c.roleMu.Unlock()
		return r, nil
	}
	c.roleMu.Unlock()
	var out struct {
		Role string `json:"role"`
	}
	if err := c.Info(ctx, infoReq{Type: "userRole", User: user}, &out); err != nil {
		return "", err
	}
	c.roleMu.Lock()
	c.roles[user] = out.Role
	c.roleMu.Unlock()
	return out.Role, nil
}
