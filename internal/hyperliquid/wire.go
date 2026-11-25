package hyperliquid

import (
	"encoding/json"
	"fmt"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Additional wire types (perp dexs, spot metadata, Earn) used by the
// registry, Markets and Account.

// SpotAssetCtx is one spotMetaAndAssetCtxs context (aligned with universe).
type SpotAssetCtx struct {
	Coin              string `json:"coin"`
	PrevDayPx         Dec    `json:"prevDayPx"`
	DayNtlVlm         Dec    `json:"dayNtlVlm"`
	MarkPx            Dec    `json:"markPx"`
	MidPx             Dec    `json:"midPx"`
	CirculatingSupply Dec    `json:"circulatingSupply"`
	TotalSupply       Dec    `json:"totalSupply"`
	DayBaseVlm        Dec    `json:"dayBaseVlm"`
}

// BorrowLendSide is the {basis, value} pair of one Earn leg.
type BorrowLendSide struct {
	Basis Dec `json:"basis"`
	Value Dec `json:"value"`
}

// BorrowLendTokenState is one token's borrow and supply.
type BorrowLendTokenState struct {
	Borrow BorrowLendSide `json:"borrow"`
	Supply BorrowLendSide `json:"supply"`
}

// BorrowLendUserState is the borrowLendUserState response (Earn). It is
// needed for true vault equity (HLP Strategy X holds ~$100M here).
type BorrowLendUserState struct {
	// TokenToState is [[token, {borrow, supply}]].
	TokenToState []TokenBorrowLend `json:"tokenToState"`
	Health       string            `json:"health"`
	HealthFactor Dec               `json:"healthFactor"`
}

// TokenBorrowLend is one [token, state] pair of BorrowLendUserState.
type TokenBorrowLend struct {
	Token int
	State BorrowLendTokenState
}

// UnmarshalJSON decodes the [token, {...}] tuple.
func (t *TokenBorrowLend) UnmarshalJSON(b []byte) error {
	var raw [2]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw[0], &t.Token); err != nil {
		return fmt.Errorf("borrowLend token: %w", err)
	}
	return json.Unmarshal(raw[1], &t.State)
}

// MarshalJSON encodes the [token, {...}] tuple (round-trips UnmarshalJSON).
func (t TokenBorrowLend) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]any{t.Token, t.State})
}

// OutcomeSide is one side of a HIP-4 outcome.
type OutcomeSide struct {
	Name string `json:"name"`
}

// OutcomeSpec is one outcomeMeta.outcomes entry.
type OutcomeSpec struct {
	Outcome     int           `json:"outcome"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	SideSpecs   []OutcomeSide `json:"sideSpecs"`
	QuoteToken  string        `json:"quoteToken"`
	Venue       string        `json:"venue"`
}

// OutcomeMeta is the outcomeMeta response (questions are ignored).
type OutcomeMeta struct {
	Outcomes []OutcomeSpec `json:"outcomes"`
}

// PerpCategory is one [coin, category] pair of perpCategories.
type PerpCategory struct {
	Coin     string
	Category string
}

// UnmarshalJSON decodes the [coin, category] tuple.
func (p *PerpCategory) UnmarshalJSON(b []byte) error {
	var raw [2]string
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	p.Coin, p.Category = raw[0], raw[1]
	return nil
}

// MarshalJSON encodes the [coin, category] tuple.
func (p PerpCategory) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]string{p.Coin, p.Category})
}

// PerpAnnotation is the perpAnnotation response for one coin.
type PerpAnnotation struct {
	Category    string   `json:"category"`
	Description string   `json:"description"`
	DisplayName string   `json:"displayName"`
	Keywords    []string `json:"keywords"`
}

// UnmarshalJSON accepts the [[window, {...}], ...] wire form as well as a
// plain JSON object keyed by window.
func (p *Portfolio) UnmarshalJSON(b []byte) error {
	var pairs []json.RawMessage
	if err := json.Unmarshal(b, &pairs); err != nil {
		var m map[string]PortfolioWindow
		if err2 := json.Unmarshal(b, &m); err2 != nil {
			return err
		}
		*p = m
		return nil
	}
	out := make(Portfolio, len(pairs))
	for _, raw := range pairs {
		var kv [2]json.RawMessage
		if err := json.Unmarshal(raw, &kv); err != nil {
			return fmt.Errorf("portfolio entry: %w", err)
		}
		var k string
		if err := json.Unmarshal(kv[0], &k); err != nil {
			return fmt.Errorf("portfolio window: %w", err)
		}
		var w PortfolioWindow
		if err := json.Unmarshal(kv[1], &w); err != nil {
			return fmt.Errorf("portfolio %s: %w", k, err)
		}
		out[k] = w
	}
	*p = out
	return nil
}

// AccountValues returns the window's accountValueHistory as a Series.
func (w PortfolioWindow) AccountValues() domain.Series { return parseHistory(w.AccountValueHistory) }

// PnLs returns the window's cumulative pnlHistory as a Series.
func (w PortfolioWindow) PnLs() domain.Series { return parseHistory(w.PnlHistory) }
