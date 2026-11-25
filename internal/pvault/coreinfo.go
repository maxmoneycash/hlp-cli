package pvault

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CoreInfo is the slice of HyperCore /info the vault needs: the vault
// contract is its own Core account. Read-only; *HTTPCoreInfo implements it.
type CoreInfo interface {
	ClearinghouseState(ctx context.Context, user, dex string) (*Clearinghouse, error)
	SpotBalances(ctx context.Context, user string) ([]SpotBalance, error)
	ExtraAgents(ctx context.Context, user string) ([]CoreAgent, error)
	// PerpDexNames returns perpDexs names by index ("" for index 0).
	PerpDexNames(ctx context.Context) ([]string, error)
}

// Clearinghouse is the part of clearinghouseState used here. Decimal fields
// stay strings (exact) and are parsed on use.
type Clearinghouse struct {
	AccountValue string
	Withdrawable string
	TotalNtlPos  string
	MarginUsed   string
	Positions    []CorePosition
	TimeMs       int64
}

// CorePosition is one perp position of the vault's Core account.
type CorePosition struct {
	Dex            string  `json:"dex,omitempty"`
	Coin           string  `json:"coin"`
	Size           float64 `json:"size"` // signed base units
	EntryPx        float64 `json:"entryPx"`
	PositionValue  float64 `json:"positionValueUsd"`
	UnrealizedPnl  float64 `json:"unrealizedPnlUsd"`
	Leverage       float64 `json:"leverage"`
	LiquidationPx  float64 `json:"liquidationPx,omitempty"`
	FundingAllTime float64 `json:"fundingAllTimeUsd"`
}

// SpotBalance is one spot balance.
type SpotBalance struct {
	Coin  string `json:"coin"`
	Total string `json:"total"`
	// EntryNtl is the USD cost basis HL reports (used to value non-USDC
	// spot when no mark is known).
	EntryNtl string `json:"entryNtl,omitempty"`
}

// CoreAgent is an approved API wallet (extraAgents).
type CoreAgent struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	ValidUntil int64  `json:"validUntil"` // ms
}

// InfoFunc posts one /info request body and decodes the answer into out.
// hyperliquid.Client.Info has this shape, so CoreInfo reads can share the
// process-wide weight limiter (SECURITY S13).
type InfoFunc func(ctx context.Context, req any, out any) error

// InfoCoreInfo implements CoreInfo over an InfoFunc.
type InfoCoreInfo struct{ Info InfoFunc }

// HTTPCoreInfo posts to a Hyperliquid /info URL directly. It is used only
// when no shared Hyperliquid client serves that URL (another network), so it
// paces itself (MinInterval, default 250 ms) and caps response bodies.
type HTTPCoreInfo struct {
	URL       string
	HTTP      *http.Client
	UserAgent string
	// MinInterval spaces requests from this value (default 250 ms; < 0 = no
	// spacing).
	MinInterval time.Duration

	mu   sync.Mutex
	last time.Time
}

func (h *HTTPCoreInfo) pace(ctx context.Context) error {
	iv := h.MinInterval
	if iv == 0 {
		iv = 250 * time.Millisecond
	}
	if iv < 0 {
		return nil
	}
	h.mu.Lock()
	wait := max(time.Until(h.last.Add(iv)), 0)
	h.last = time.Now().Add(wait)
	h.mu.Unlock()
	if wait == 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (h *HTTPCoreInfo) post(ctx context.Context, body any, out any) error {
	if err := h.pace(ctx); err != nil {
		return err
	}
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.UserAgent != "" {
		req.Header.Set("User-Agent", h.UserAgent)
	}
	hc := h.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hyperliquid info: HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

type wireCH struct {
	MarginSummary struct {
		AccountValue    string `json:"accountValue"`
		TotalNtlPos     string `json:"totalNtlPos"`
		TotalMarginUsed string `json:"totalMarginUsed"`
	} `json:"marginSummary"`
	Withdrawable   string `json:"withdrawable"`
	AssetPositions []struct {
		Position struct {
			Coin     string `json:"coin"`
			Szi      string `json:"szi"`
			EntryPx  string `json:"entryPx"`
			Leverage struct {
				Value float64 `json:"value"`
			} `json:"leverage"`
			PositionValue string  `json:"positionValue"`
			UnrealizedPnl string  `json:"unrealizedPnl"`
			LiquidationPx *string `json:"liquidationPx"`
			CumFunding    struct {
				AllTime string `json:"allTime"`
			} `json:"cumFunding"`
		} `json:"position"`
	} `json:"assetPositions"`
	Time int64 `json:"time"`
}

// ParseClearinghouse maps a clearinghouseState JSON body.
func ParseClearinghouse(raw []byte, dex string) (*Clearinghouse, error) {
	var w wireCH
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	ch := &Clearinghouse{AccountValue: w.MarginSummary.AccountValue, Withdrawable: w.Withdrawable,
		TotalNtlPos: w.MarginSummary.TotalNtlPos, MarginUsed: w.MarginSummary.TotalMarginUsed, TimeMs: w.Time}
	for _, ap := range w.AssetPositions {
		p := ap.Position
		cp := CorePosition{Dex: dex, Coin: p.Coin, Size: f(p.Szi), EntryPx: f(p.EntryPx), PositionValue: f(p.PositionValue),
			UnrealizedPnl: f(p.UnrealizedPnl), Leverage: p.Leverage.Value, FundingAllTime: f(p.CumFunding.AllTime)}
		if p.LiquidationPx != nil {
			cp.LiquidationPx = f(*p.LiquidationPx)
		}
		ch.Positions = append(ch.Positions, cp)
	}
	return ch, nil
}

// ClearinghouseState implements CoreInfo.
func (h *HTTPCoreInfo) ClearinghouseState(ctx context.Context, user, dex string) (*Clearinghouse, error) {
	return clearinghouseState(ctx, h.post, user, dex)
}

// SpotBalances implements CoreInfo.
func (h *HTTPCoreInfo) SpotBalances(ctx context.Context, user string) ([]SpotBalance, error) {
	return spotBalances(ctx, h.post, user)
}

// ExtraAgents implements CoreInfo.
func (h *HTTPCoreInfo) ExtraAgents(ctx context.Context, user string) ([]CoreAgent, error) {
	return extraAgents(ctx, h.post, user)
}

// PerpDexNames implements CoreInfo.
func (h *HTTPCoreInfo) PerpDexNames(ctx context.Context) ([]string, error) {
	return perpDexNames(ctx, h.post)
}

// ClearinghouseState implements CoreInfo.
func (c InfoCoreInfo) ClearinghouseState(ctx context.Context, user, dex string) (*Clearinghouse, error) {
	return clearinghouseState(ctx, c.Info, user, dex)
}

// SpotBalances implements CoreInfo.
func (c InfoCoreInfo) SpotBalances(ctx context.Context, user string) ([]SpotBalance, error) {
	return spotBalances(ctx, c.Info, user)
}

// ExtraAgents implements CoreInfo.
func (c InfoCoreInfo) ExtraAgents(ctx context.Context, user string) ([]CoreAgent, error) {
	return extraAgents(ctx, c.Info, user)
}

// PerpDexNames implements CoreInfo.
func (c InfoCoreInfo) PerpDexNames(ctx context.Context) ([]string, error) {
	return perpDexNames(ctx, c.Info)
}

func clearinghouseState(ctx context.Context, post InfoFunc, user, dex string) (*Clearinghouse, error) {
	body := map[string]string{"type": "clearinghouseState", "user": user}
	if dex != "" {
		body["dex"] = dex
	}
	var raw json.RawMessage
	if err := post(ctx, body, &raw); err != nil {
		return nil, err
	}
	return ParseClearinghouse(raw, dex)
}

func spotBalances(ctx context.Context, post InfoFunc, user string) ([]SpotBalance, error) {
	var w struct {
		Balances []SpotBalance `json:"balances"`
	}
	err := post(ctx, map[string]string{"type": "spotClearinghouseState", "user": user}, &w)
	return w.Balances, err
}

func extraAgents(ctx context.Context, post InfoFunc, user string) ([]CoreAgent, error) {
	var out []CoreAgent
	err := post(ctx, map[string]string{"type": "extraAgents", "user": user}, &out)
	return out, err
}

func perpDexNames(ctx context.Context, post InfoFunc) ([]string, error) {
	var raw []json.RawMessage
	if err := post(ctx, map[string]string{"type": "perpDexs"}, &raw); err != nil {
		return nil, err
	}
	out := make([]string, len(raw))
	for i, r := range raw {
		var d struct {
			Name string `json:"name"`
		}
		if string(r) != "null" {
			_ = json.Unmarshal(r, &d)
		}
		out[i] = d.Name
	}
	return out, nil
}

func f(s string) float64 {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok {
		return 0
	}
	v, _ := r.Float64()
	return v
}

// usdcUnits parses a decimal USD string into 6-dp base units (rounded).
func usdcUnits(s string) *big.Int {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok {
		return new(big.Int)
	}
	r.Mul(r, new(big.Rat).SetInt64(1_000_000))
	n := new(big.Int).Quo(r.Num(), r.Denom())
	rem := new(big.Int).Rem(r.Num(), r.Denom())
	if new(big.Int).Abs(new(big.Int).Mul(rem, big.NewInt(2))).Cmp(r.Denom()) >= 0 {
		if r.Sign() >= 0 {
			n.Add(n, big.NewInt(1))
		} else {
			n.Sub(n, big.NewInt(1))
		}
	}
	return n
}
