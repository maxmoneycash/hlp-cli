package pvault

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Manipulation-risk model. Moving a book d bp and holding it there for about
// a minute is assumed to cost C(d) ≈ PushCostK · depth10 · d/1e4 (linear in
// the depth within 10 bp of mid; PushCostK is a deliberately rough,
// single-observation estimate, so treat the output as an order of
// magnitude). An attacker
// depositing or redeeming Q at a settle gains Q · L · d/1e4, where L is the
// vault's |notional| in the pushed asset over NAV. Pushes pay when
// C < gain/SafetyK, so the flow per settle that is safe against a push is
// F = PushCostK · depth10 / (L · SafetyK), independent of d.
const (
	PushCostK = 0.27
	SafetyK   = 3.0
	// RiskTTL is the shortest interval between two risk reads of one vault
	// (free public reads, but polled no faster than this).
	RiskTTL = 60 * time.Second
	// Level thresholds on ratio = flowAtRisk / safeFlow.
	riskMedium = 0.3
	riskHigh   = 1.0
	// hip3CriticalL is the HIP-3 exposure (fraction of NAV) above which the
	// deployer-oracle attack (A4) makes the level critical.
	hip3CriticalL = 0.10
	// spotCriticalFrac is the unvalued non-USDC spot (fraction of NAV) that
	// makes the level critical; below it the flag alone is raised (dust).
	spotCriticalFrac = 0.01
	// looseBreakerBps: a maxPriceMoveBps above this is a loose breaker (the
	// recommended share-price jump latch starts at 200 bp).
	looseBreakerBps = 200
	riskModel       = "linear push cost C=0.27*depth10, safety k=3, rough single-observation estimate"
)

// Risk flags.
const (
	FlagHIP3Oracle       = "hip3_deployer_oracle"
	FlagBookBuiltHours   = "book_built_oracle_hours"
	FlagUnvaluedSpot     = "unvalued_spot"
	FlagLateSettle       = "permissionless_late_settle"
	FlagLooseBreaker     = "loose_price_breaker"
	FlagTestnetPrices    = "testnet_prices"
	FlagNoDepositCap     = "no_deposit_cap"
	FlagDepthUnavailable = "depth_unavailable"
)

// Risk levels.
const (
	RiskLow      = "low"
	RiskMedium   = "medium"
	RiskHigh     = "high"
	RiskCritical = "critical"
)

// RiskAsset is one held market's contribution.
type RiskAsset struct {
	Coin        string  `json:"coin"`
	Dex         string  `json:"dex,omitempty"` // "" = main perp dex
	NotionalUSD float64 `json:"notionalUsd"`   // signed: short < 0
	L           float64 `json:"l"`             // |notional| / NAV
	// Depth10USD is the live notional within 10 bp of mid on the thinner
	// side (0 with DepthKnown false when the book was unavailable).
	Depth10USD       float64  `json:"depth10Usd"`
	DepthKnown       bool     `json:"depthKnown"`
	SafeFlowUSD      *float64 `json:"safeFlowUsd,omitempty"` // PushCostK·depth10/(L·SafetyK)
	PushCost10BpUSD  float64  `json:"pushCost10bpUsd"`       // C(10 bp)
	NavBpPer10bpPush float64  `json:"navBpPer10bpPush"`      // L · 10
	MarkOracleGapBps *float64 `json:"markOracleGapBps,omitempty"`
	HIP3             bool     `json:"hip3"`
}

// RiskView is the "manipulation risk (estimate)" block of a strategy-vault
// tearsheet. It is an estimate from free public
// reads, not a guarantee.
type RiskView struct {
	Label string `json:"label"` // "manipulation risk (estimate)"
	Level string `json:"level"` // low | medium | high | critical
	// SafeFlowUSD is min over held assets of the safe flow per settle; nil
	// when the vault holds no position (nothing to push).
	SafeFlowUSD  *float64 `json:"safeFlowUsd,omitempty"`
	BindingAsset string   `json:"bindingAsset,omitempty"`
	// FlowAtRiskUSD is max(depositCap − NAV, pending deposits + pending
	// redeem value): the most that can be priced at one settle.
	FlowAtRiskUSD float64 `json:"flowAtRiskUsd"`
	// Ratio is flowAtRisk / safeFlow (0 without positions; capped at 1e6).
	Ratio            float64            `json:"ratio"`
	NavBpPer10bpPush map[string]float64 `json:"navBpPer10bpPush"`
	MarkOracleGapBps map[string]float64 `json:"markOracleGapBps"`
	Assets           []RiskAsset        `json:"assets"`
	// UnvaluedSpotUSD estimates non-USDC Core spot, which NAV counts as 0.
	UnvaluedSpotUSD float64  `json:"unvaluedSpotUsd,omitempty"`
	Flags           []string `json:"flags"`
	Model           string   `json:"model"`
	// DepthSource says which network's books gave depth10 ("mainnet" is a
	// proxy for a testnet vault).
	DepthSource string   `json:"depthSource"`
	Notes       []string `json:"notes,omitempty"`
	AsOf        int64    `json:"asOf"`     // ms when read
	NextRead    int64    `json:"nextRead"` // ms: earliest refresh (RiskTTL)
	Cached      bool     `json:"cached"`   // served from the RiskTTL cache
	Partial     bool     `json:"partial"`  // some reads failed
	Warnings    []string `json:"warnings,omitempty"`
}

// Book is the part of an l2Book used here: price and size per level, best
// first.
type Book struct {
	Bids, Asks [][2]float64
}

// AssetCtx is mark and oracle of one perp market.
type AssetCtx struct {
	Mark, Oracle float64
}

// MarketInfo is the optional market-data side of a CoreInfo (l2Book and
// metaAndAssetCtxs). InfoCoreInfo and HTTPCoreInfo implement it.
type MarketInfo interface {
	L2Book(ctx context.Context, coin string) (*Book, error)
	// AssetCtxs maps coin name (as clearinghouseState spells it) to mark and
	// oracle for one dex ("" = main).
	AssetCtxs(ctx context.Context, dex string) (map[string]AssetCtx, error)
}

// Depth10 is the notional within 10 bp of mid on the thinner side.
func Depth10(b *Book) (float64, bool) {
	if b == nil || len(b.Bids) == 0 || len(b.Asks) == 0 {
		return 0, false
	}
	mid := (b.Bids[0][0] + b.Asks[0][0]) / 2
	if mid <= 0 {
		return 0, false
	}
	lo, hi := mid*(1-10.0/1e4), mid*(1+10.0/1e4)
	var bid, ask float64
	for _, l := range b.Bids {
		if l[0] >= lo {
			bid += l[0] * l[1]
		}
	}
	for _, l := range b.Asks {
		if l[0] <= hi {
			ask += l[0] * l[1]
		}
	}
	return math.Min(bid, ask), true
}

// RiskInputs is what BuildRisk combines.
type RiskInputs struct {
	NavUSD             float64
	DepositCapUSD      float64 // 0 = unknown or no cap
	PendingDepositsUSD float64
	PendingRedeemUSD   float64 // pending redeem shares × share price
	MaxPriceMoveBps    uint64  // 0 = unknown
	Testnet            bool
	LateSettle         bool // settle() is permissionless after the deadline
	Positions          []CorePosition
	Depth              map[string]float64 // key riskKey(dex, coin); absent = unknown
	Ctxs               map[string]AssetCtx
	UnvaluedSpotUSD    float64
	HasUnvaluedSpot    bool
	DepthSource        string
	Now                time.Time
}

func riskKey(dex, coin string) string { return dex + "|" + coin }

// BuildRisk is pure: it computes the risk block.
func BuildRisk(in RiskInputs) *RiskView {
	v := &RiskView{Label: "manipulation risk (estimate)", Model: riskModel, DepthSource: in.DepthSource,
		NavBpPer10bpPush: map[string]float64{}, MarkOracleGapBps: map[string]float64{}, Assets: []RiskAsset{}, Flags: []string{}}
	if !in.Now.IsZero() {
		v.AsOf = in.Now.UnixMilli()
		v.NextRead = in.Now.Add(RiskTTL).UnixMilli()
	}
	flags := map[string]bool{}
	nav := in.NavUSD
	critical := false
	var hip3L float64
	for _, p := range in.Positions {
		if p.Size == 0 {
			continue
		}
		notional := math.Abs(p.PositionValue)
		if p.Size < 0 {
			notional = -notional
		}
		a := RiskAsset{Coin: p.Coin, Dex: p.Dex, NotionalUSD: notional, HIP3: p.Dex != "" || strings.Contains(p.Coin, ":")}
		if nav > 0 {
			a.L = math.Abs(notional) / nav
		}
		a.NavBpPer10bpPush = round(a.L*10, 2)
		if d, ok := in.Depth[riskKey(p.Dex, p.Coin)]; ok {
			a.Depth10USD, a.DepthKnown = d, true
			a.PushCost10BpUSD = round(PushCostK*d*10/1e4, 2)
			if a.L > 0 {
				f := round(PushCostK*d/(a.L*SafetyK), 0)
				a.SafeFlowUSD = &f
			}
		} else {
			flags[FlagDepthUnavailable] = true
		}
		if c, ok := in.Ctxs[riskKey(p.Dex, p.Coin)]; ok && c.Oracle > 0 {
			g := round((c.Mark-c.Oracle)/c.Oracle*1e4, 1)
			a.MarkOracleGapBps = &g
			v.MarkOracleGapBps[p.Coin] = g
		}
		if a.HIP3 {
			flags[FlagHIP3Oracle] = true
			hip3L += a.L
		}
		v.NavBpPer10bpPush[p.Coin] = a.NavBpPer10bpPush
		v.Assets = append(v.Assets, a)
		if a.SafeFlowUSD != nil && (v.SafeFlowUSD == nil || *a.SafeFlowUSD < *v.SafeFlowUSD) {
			f := *a.SafeFlowUSD
			v.SafeFlowUSD, v.BindingAsset = &f, a.Coin
		}
	}
	sort.SliceStable(v.Assets, func(i, j int) bool { return v.Assets[i].L > v.Assets[j].L })
	headroom := 0.0
	if in.DepositCapUSD > 0 {
		headroom = math.Max(0, in.DepositCapUSD-nav)
	} else {
		flags[FlagNoDepositCap] = true
		headroom = nav // without a known cap, assume a deposit as large as the vault
	}
	v.FlowAtRiskUSD = round(math.Max(headroom, in.PendingDepositsUSD+in.PendingRedeemUSD), 2)
	if v.SafeFlowUSD != nil {
		if *v.SafeFlowUSD > 0 {
			v.Ratio = math.Min(v.FlowAtRiskUSD / *v.SafeFlowUSD, 1e6)
		} else if v.FlowAtRiskUSD > 0 {
			v.Ratio = 1e6
		}
		v.Ratio = round(v.Ratio, 4)
	}
	switch {
	case v.Ratio > riskHigh:
		v.Level = RiskHigh
	case v.Ratio >= riskMedium:
		v.Level = RiskMedium
	default:
		v.Level = RiskLow
	}
	if in.HasUnvaluedSpot {
		flags[FlagUnvaluedSpot] = true
		v.UnvaluedSpotUSD = round(in.UnvaluedSpotUSD, 2)
		if nav > 0 && in.UnvaluedSpotUSD/nav > spotCriticalFrac {
			critical = true
			v.Notes = append(v.Notes, fmt.Sprintf("non-USDC spot worth about $%.2f is valued at 0 in NAV: new depositors buy it cheaply", in.UnvaluedSpotUSD))
		}
	}
	if hip3L > hip3CriticalL {
		critical = true
		v.Notes = append(v.Notes, fmt.Sprintf("%.0f%% of NAV in HIP-3 markets with no on-chain reference check: the deployer's oracle can move NAV at no book cost", hip3L*100))
	}
	if critical {
		v.Level = RiskCritical
	}
	if in.LateSettle {
		flags[FlagLateSettle] = true
	}
	if in.MaxPriceMoveBps > looseBreakerBps {
		flags[FlagLooseBreaker] = true
	}
	if in.Testnet {
		flags[FlagTestnetPrices] = true
		v.Notes = append(v.Notes, "testnet prices are not meaningful (testnet marks can sit thousands of bp from their oracle); depth is "+in.DepthSource)
	}
	if bookBuiltHours(in.Positions, in.Now) {
		flags[FlagBookBuiltHours] = true
	}
	for f := range flags {
		v.Flags = append(v.Flags, f)
	}
	sort.Strings(v.Flags)
	v.Notes = append(v.Notes, "depth10 is the live book, not a low percentile: an attacker can wait for the book to thin out")
	return v
}

// bookBuiltHours: Entropy (io:) oracles are always book-built; xyz's are
// from Friday 20:00 to Sunday 20:05 New York time.
func bookBuiltHours(ps []CorePosition, now time.Time) bool {
	for _, p := range ps {
		dex := p.Dex
		if dex == "" {
			if i := strings.Index(p.Coin, ":"); i > 0 {
				dex = p.Coin[:i]
			}
		}
		switch dex {
		case "io":
			return true
		case "xyz":
			if !now.IsZero() && xyzWeekend(now) {
				return true
			}
		}
	}
	return false
}

func xyzWeekend(t time.Time) bool {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("ET", -5*3600)
	}
	t = t.In(loc)
	mins := t.Hour()*60 + t.Minute()
	switch t.Weekday() {
	case time.Friday:
		return mins >= 20*60
	case time.Saturday:
		return true
	case time.Sunday:
		return mins < 20*60+5
	}
	return false
}

func round(x float64, dp int) float64 {
	p := math.Pow(10, float64(dp))
	return math.Round(x*p) / p
}

// ---- reads ----

type wireBook struct {
	Levels [2][]struct {
		Px string `json:"px"`
		Sz string `json:"sz"`
	} `json:"levels"`
}

func l2Book(ctx context.Context, post InfoFunc, coin string) (*Book, error) {
	var w wireBook
	if err := post(ctx, map[string]string{"type": "l2Book", "coin": coin}, &w); err != nil {
		return nil, err
	}
	b := &Book{}
	for _, l := range w.Levels[0] {
		b.Bids = append(b.Bids, [2]float64{f(l.Px), f(l.Sz)})
	}
	for _, l := range w.Levels[1] {
		b.Asks = append(b.Asks, [2]float64{f(l.Px), f(l.Sz)})
	}
	return b, nil
}

func assetCtxs(ctx context.Context, post InfoFunc, dex string) (map[string]AssetCtx, error) {
	body := map[string]string{"type": "metaAndAssetCtxs"}
	if dex != "" {
		body["dex"] = dex
	}
	var raw [2]json.RawMessage
	if err := post(ctx, body, &raw); err != nil {
		return nil, err
	}
	var meta struct {
		Universe []struct {
			Name string `json:"name"`
		} `json:"universe"`
	}
	var ctxs []struct {
		MarkPx   string `json:"markPx"`
		OraclePx string `json:"oraclePx"`
	}
	if err := json.Unmarshal(raw[0], &meta); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw[1], &ctxs); err != nil {
		return nil, err
	}
	out := map[string]AssetCtx{}
	for i, u := range meta.Universe {
		if i < len(ctxs) {
			out[u.Name] = AssetCtx{Mark: f(ctxs[i].MarkPx), Oracle: f(ctxs[i].OraclePx)}
		}
	}
	return out, nil
}

// L2Book implements MarketInfo.
func (c InfoCoreInfo) L2Book(ctx context.Context, coin string) (*Book, error) {
	return l2Book(ctx, c.Info, coin)
}

// AssetCtxs implements MarketInfo.
func (c InfoCoreInfo) AssetCtxs(ctx context.Context, dex string) (map[string]AssetCtx, error) {
	return assetCtxs(ctx, c.Info, dex)
}

// L2Book implements MarketInfo.
func (h *HTTPCoreInfo) L2Book(ctx context.Context, coin string) (*Book, error) {
	return l2Book(ctx, h.post, coin)
}

// AssetCtxs implements MarketInfo.
func (h *HTTPCoreInfo) AssetCtxs(ctx context.Context, dex string) (map[string]AssetCtx, error) {
	return assetCtxs(ctx, h.post, dex)
}

// Info posts one /info body (the InfoFunc shape), paced and capped like
// every HTTPCoreInfo read.
func (h *HTTPCoreInfo) Info(ctx context.Context, req any, out any) error {
	return h.post(ctx, req, out)
}
