package hyperliquid

import (
	"math"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// Wire -> domain mappers. Exported so ws can reuse them for stream frames.

// ToCandle converts a wire candle (keeps the OPEN time).
func ToCandle(w WireCandle) domain.Candle {
	return domain.Candle{T: w.OpenTime, O: w.O.Float(), H: w.H.Float(), L: w.L.Float(), C: w.C.Float(), V: w.V.Float(), N: w.N}
}

// ToLevels converts wire book levels.
func ToLevels(ws []WireLevel) []domain.Level {
	out := make([]domain.Level, 0, len(ws))
	for _, l := range ws {
		out = append(out, domain.Level{Px: l.Px.Float(), Sz: l.Sz.Float(), N: l.N})
	}
	return out
}

// ToBook converts an l2Book snapshot (bids descending, asks ascending as sent).
func ToBook(b L2Book) domain.Book {
	return domain.Book{Coin: b.Coin, Time: b.Time, Bids: ToLevels(b.Levels[0]), Asks: ToLevels(b.Levels[1])}
}

// ToTrade converts a public trade.
func ToTrade(t WireTrade) domain.Trade {
	return domain.Trade{Coin: t.Coin, Side: domain.Side(t.Side), Px: t.Px.Float(), Sz: t.Sz.Float(), Time: t.Time, TID: t.TID, Hash: t.Hash}
}

// ToFill converts an account fill.
func ToFill(f WireFill) domain.Fill {
	return domain.Fill{
		Coin:      f.Coin,
		Side:      domain.Side(f.Side),
		Px:        f.Px.Float(),
		Sz:        f.Sz.Float(),
		Time:      f.Time,
		Dir:       f.Dir,
		StartPos:  f.StartPosition.Float(),
		ClosedPnL: f.ClosedPnl.Float(),
		Fee:       f.Fee.Float(),
		OID:       f.OID,
		TID:       f.TID,
		Hash:      f.Hash,
		Liq:       len(f.Liquidation) > 0 && string(f.Liquidation) != "null",
	}
}

// ToFunding converts a fundingHistory element.
func ToFunding(f WireFunding) domain.FundingPoint {
	return domain.FundingPoint{Coin: f.Coin, Time: f.Time, Rate: f.FundingRate.Float(), Premium: f.Premium.Float()}
}

// ToOrder converts a frontendOpenOrders element.
func ToOrder(o FrontendOrder) domain.Order {
	return domain.Order{
		Coin:       o.Coin,
		Side:       domain.Side(o.Side),
		Px:         o.LimitPx.Float(),
		Sz:         o.Sz.Float(),
		OrigSz:     o.OrigSz.Float(),
		OID:        o.OID,
		CLOID:      o.Cloid,
		Type:       o.OrderType,
		TIF:        o.Tif,
		ReduceOnly: o.ReduceOnly,
		TriggerPx:  o.TriggerPx.Float(),
		Time:       o.Timestamp,
	}
}

// ToPosition converts a perp position. dex is "" for the native dex.
func ToPosition(p WirePosition, dex string) domain.Position {
	size := p.Szi.Float()
	value := math.Abs(p.PositionValue.Float())
	pos := domain.Position{
		Coin:       p.Coin,
		Dex:        dex,
		Size:       size,
		Entry:      p.EntryPx.Float(),
		Value:      value,
		UPnL:       p.UnrealizedPnl.Float(),
		ROE:        p.ReturnOnEquity.Float(),
		Leverage:   float64(p.Leverage.Value),
		Cross:      p.Leverage.Type == "cross",
		LiqPx:      p.LiquidationPx.Float(),
		MarginUsed: p.MarginUsed.Float(),
		FundingAll: p.CumFunding.AllTime.Float(),
	}
	if size != 0 {
		pos.Mark = value / math.Abs(size)
	}
	if pos.Dex == "" {
		if d, _, ok := strings.Cut(p.Coin, ":"); ok {
			pos.Dex = d
		}
	}
	return pos
}

// DexState is one dex's clearinghouse state (as in allDexsClearinghouseState).
type DexState struct {
	Dex   string
	State ClearinghouseState
}

// PerpAccount aggregates per-dex clearinghouse states into the perp part of
// a domain.Account (Equity = PerpEquity; spot and Earn are added by Account).
func PerpAccount(addr string, states []DexState) domain.Account {
	a := domain.Account{Address: addr, Positions: []domain.Position{}}
	for _, ds := range states {
		s := ds.State
		a.PerpEquity += s.MarginSummary.AccountValue.Float()
		a.Withdrawable += s.Withdrawable.Float()
		a.MarginUsed += s.MarginSummary.TotalMarginUsed.Float()
		if s.Time > a.Time {
			a.Time = s.Time
		}
		for _, ap := range s.AssetPositions {
			p := ToPosition(ap.Position, ds.Dex)
			if p.Size == 0 {
				continue
			}
			a.GrossNtl += p.Value
			if p.Size > 0 {
				a.NetNtl += p.Value
			} else {
				a.NetNtl -= p.Value
			}
			a.Positions = append(a.Positions, p)
		}
	}
	a.Equity = a.PerpEquity
	return a
}

// Capacity is the deposit room under the leader-5% rule (a vault leader
// must keep at least 5% of the vault):
// tvl*(leaderFraction-0.05)/0.05, -1 (unlimited) for protocol vaults, 0
// when the leader is below ~5%.
func Capacity(tvl, leaderFraction float64, protocol bool) float64 {
	if protocol {
		return -1
	}
	if leaderFraction < 0.049 || tvl <= 0 {
		return 0
	}
	return math.Max(0, tvl*(leaderFraction-0.05)/0.05)
}
