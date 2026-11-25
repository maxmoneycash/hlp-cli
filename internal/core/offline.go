package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sahilm/fuzzy"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
)

// FixtureReader is an offline hyperliquid.Reader backed by JSON files in a
// directory (HLP_FIXTURES=<dir>, `hlp --demo`, CLI end-to-end tests). It
// never touches the network. Files are domain types unless noted:
//
//	markets.json                []domain.Market (also feeds the Registry)
//	candles_<COIN>_<iv>.json    []domain.Candle or raw HL candleSnapshot
//	candles_<COIN>.json         fallback for any interval
//	book_<COIN>.json            domain.Book
//	trades_<COIN>.json          []domain.Trade
//	funding_<COIN>.json         []domain.FundingPoint
//	account_<addr>.json         domain.Account
//	fills_<addr>.json           []domain.Fill
//	vaults.json                 []domain.VaultSummary
//	vault_<addr>.json           domain.VaultDetail
//
// COIN is the wire coin with ':' and '/' replaced by '_' ("xyz_SP500").
type FixtureReader struct {
	Dir string

	regOnce sync.Once
	reg     *fixtureRegistry
}

var _ hyperliquid.Reader = (*FixtureReader)(nil)

// NewFixtureReader returns a Reader serving files from dir.
func NewFixtureReader(dir string) *FixtureReader { return &FixtureReader{Dir: dir} }

func fixtureName(s string) string {
	return strings.NewReplacer(":", "_", "/", "_", "@", "at").Replace(s)
}

func (f *FixtureReader) load(name string, out any) error {
	b, err := os.ReadFile(filepath.Join(f.Dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("fixture %s: %w", name, hyperliquid.ErrNotFound)
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("fixture %s: %w", name, err)
	}
	return nil
}

func (f *FixtureReader) meta() domain.Meta {
	return domain.Meta{Source: "fixtures", AsOf: time.Now()}
}

// Markets implements hyperliquid.Reader.
func (f *FixtureReader) Markets(context.Context) ([]domain.Market, domain.Meta, error) {
	var ms []domain.Market
	err := f.load("markets.json", &ms)
	return ms, f.meta(), err
}

// Candles implements hyperliquid.Reader. Raw HL candleSnapshot files work too.
func (f *FixtureReader) Candles(_ context.Context, coin string, iv domain.Interval, n int, end int64) ([]domain.Candle, error) {
	var raw json.RawMessage
	err := f.load("candles_"+fixtureName(coin)+"_"+string(iv)+".json", &raw)
	if errors.Is(err, hyperliquid.ErrNotFound) {
		err = f.load("candles_"+fixtureName(coin)+".json", &raw)
	}
	if err != nil {
		return nil, err
	}
	var wire []hyperliquid.WireCandle
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("fixture candles %s: %w", coin, err)
	}
	cs := make([]domain.Candle, 0, len(wire))
	for _, w := range wire {
		cs = append(cs, hyperliquid.ToCandle(w))
	}
	if end > 0 {
		k := sort.Search(len(cs), func(i int) bool { return cs[i].T > end })
		cs = cs[:k]
	}
	if n > 0 && len(cs) > n {
		cs = cs[len(cs)-n:]
	}
	return cs, nil
}

// Book implements hyperliquid.Reader.
func (f *FixtureReader) Book(_ context.Context, coin string, _ hyperliquid.BookOpts) (domain.Book, error) {
	var b domain.Book
	err := f.load("book_"+fixtureName(coin)+".json", &b)
	return b, err
}

// Trades implements hyperliquid.Reader.
func (f *FixtureReader) Trades(_ context.Context, coin string) ([]domain.Trade, error) {
	var ts []domain.Trade
	err := f.load("trades_"+fixtureName(coin)+".json", &ts)
	return ts, err
}

// Funding implements hyperliquid.Reader.
func (f *FixtureReader) Funding(_ context.Context, coin string, start, end int64) ([]domain.FundingPoint, error) {
	var ps []domain.FundingPoint
	if err := f.load("funding_"+fixtureName(coin)+".json", &ps); err != nil {
		return nil, err
	}
	out := ps[:0:0]
	for _, p := range ps {
		if p.Time >= start && (end == 0 || p.Time <= end) {
			out = append(out, p)
		}
	}
	return out, nil
}

// Account implements hyperliquid.Reader.
func (f *FixtureReader) Account(_ context.Context, addr string) (domain.Account, error) {
	var a domain.Account
	err := f.load("account_"+strings.ToLower(addr)+".json", &a)
	return a, err
}

// Fills implements hyperliquid.Reader.
func (f *FixtureReader) Fills(_ context.Context, addr string, start, end int64) ([]domain.Fill, error) {
	var fs []domain.Fill
	if err := f.load("fills_"+strings.ToLower(addr)+".json", &fs); err != nil {
		if errors.Is(err, hyperliquid.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	out := fs[:0:0]
	for _, x := range fs {
		if x.Time >= start && (end == 0 || x.Time <= end) {
			out = append(out, x)
		}
	}
	return out, nil
}

// VaultList implements hyperliquid.Reader.
func (f *FixtureReader) VaultList(context.Context) ([]domain.VaultSummary, domain.Meta, error) {
	var vs []domain.VaultSummary
	err := f.load("vaults.json", &vs)
	return vs, f.meta(), err
}

// Vault implements hyperliquid.Reader.
func (f *FixtureReader) Vault(_ context.Context, addr string) (domain.VaultDetail, error) {
	var d domain.VaultDetail
	err := f.load("vault_"+strings.ToLower(addr)+".json", &d)
	return d, err
}

// Registry implements hyperliquid.Reader over markets.json.
func (f *FixtureReader) Registry() hyperliquid.Registry {
	f.regOnce.Do(func() { f.reg = &fixtureRegistry{f: f} })
	return f.reg
}

type fixtureRegistry struct {
	f       *FixtureReader
	mu      sync.Mutex
	markets []domain.Market
}

func (r *fixtureRegistry) Refresh(ctx context.Context) error {
	ms, _, err := r.f.Markets(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.markets = ms
	r.mu.Unlock()
	return nil
}

func (r *fixtureRegistry) Markets() []domain.Market {
	r.mu.Lock()
	ms := r.markets
	r.mu.Unlock()
	if ms == nil {
		_ = r.Refresh(context.Background())
		r.mu.Lock()
		ms = r.markets
		r.mu.Unlock()
	}
	return ms
}

func (r *fixtureRegistry) Resolve(input string) (domain.Market, error) {
	ms := r.Markets()
	for _, m := range ms {
		if strings.EqualFold(m.Coin, input) || strings.EqualFold(m.Display, input) {
			return m, nil
		}
	}
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = m.Coin
	}
	var sugg []string
	for _, m := range fuzzy.Find(input, names) {
		if len(sugg) == 5 {
			break
		}
		sugg = append(sugg, m.Str)
	}
	if len(sugg) > 0 {
		return domain.Market{}, fmt.Errorf("%w: %s (did you mean %s?)", hyperliquid.ErrUnknownCoin, input, strings.Join(sugg, ", "))
	}
	return domain.Market{}, fmt.Errorf("%w: %s", hyperliquid.ErrUnknownCoin, input)
}
