package pvault

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/evm"
)

// depthMarket returns the MarketInfo whose books give depth10 for chain
// ch, and its label. A testnet vault uses mainnet books as a proxy
// because testnet books are thin and say nothing about real push cost.
func (s *Service) depthMarket(r *Reader, ch evm.Chain) (MarketInfo, string) {
	if !ch.Testnet() {
		mi, _ := r.Core.(MarketInfo)
		return mi, "live " + netName(ch) + " book"
	}
	s.riskMu.Lock()
	defer s.riskMu.Unlock()
	if mi, ok := s.depth[evm.ChainMainnet]; ok {
		return mi, "mainnet book (proxy for testnet)"
	}
	main, ok := s.o.Chains[evm.ChainMainnet]
	var core CoreInfo
	switch {
	case !ok || main.Info == "":
	case s.o.NewCore != nil:
		main.ID = evm.ChainMainnet
		core = s.o.NewCore(main)
	default:
		core = &HTTPCoreInfo{URL: main.Info, HTTP: s.o.HTTP, UserAgent: s.o.UserAgent}
	}
	mi, _ := core.(MarketInfo)
	s.depth[evm.ChainMainnet] = mi
	return mi, "mainnet book (proxy for testnet)"
}

func netName(ch evm.Chain) string {
	if ch.Testnet() {
		return "testnet"
	}
	return "mainnet"
}

// risk computes (or serves from the RiskTTL cache) the manipulation-risk
// block. Read failures make it partial, never fail the tearsheet.
func (s *Service) risk(ctx context.Context, r *Reader, ch evm.Chain, vault evm.Address, t *Tearsheet, d *Details, pos []CorePosition, havePos bool, m *domain.Meta) *RiskView {
	key := strconv.FormatUint(ch.ID, 10) + "|" + vault.Hex()
	now := s.o.Now()
	s.riskMu.Lock()
	if c := s.riskCache[key]; c != nil && now.UnixMilli() < c.NextRead {
		cp := *c
		cp.Cached = true
		s.riskMu.Unlock()
		if cp.Partial {
			warn(m, "manipulation risk is partial (cached): %s", strings.Join(cp.Warnings, "; "))
		}
		return &cp
	}
	s.riskMu.Unlock()

	var warns []string
	if r.Core == nil {
		warn(m, "manipulation risk unavailable: no HyperCore /info for chain %d", ch.ID)
		return nil
	}
	if !havePos {
		pos, _ = s.corePositions(ctx, r, vault, d, false, m)
	}
	in := RiskInputs{NavUSD: t.NavUSD, PendingDepositsUSD: t.PendingDepositsUSD, PendingRedeemUSD: t.PendingRedeemShares * t.SharePrice,
		Testnet: ch.Testnet(), Positions: pos, Depth: map[string]float64{}, Ctxs: map[string]AssetCtx{}, Now: now}
	if t.Config != nil {
		in.DepositCapUSD, in.MaxPriceMoveBps = t.Config.DepositCapUSD, t.Config.MaxPriceMoveBps
	}
	if d != nil && d.SettleGrace > 0 {
		// The deployed contract lets anyone settle once settleDeadline has
		// passed (checked by simulating the call against the contract).
		in.LateSettle = true
	}
	// mark and oracle, from the vault's own network (NAV is marked there)
	own, _ := r.Core.(MarketInfo)
	dexes := map[string]bool{}
	for _, p := range pos {
		dexes[p.Dex] = true
	}
	mainCtx := map[string]AssetCtx(nil)
	if own != nil {
		for _, dx := range sortedKeys(dexes) {
			cx, err := own.AssetCtxs(ctx, dx)
			if err != nil {
				warns = append(warns, "mark/oracle on dex "+dexLabel(dx)+": "+err.Error())
				continue
			}
			if dx == "" {
				mainCtx = cx
			}
			for coin, c := range cx {
				in.Ctxs[riskKey(dx, coin)] = c
			}
		}
	} else {
		warns = append(warns, "mark/oracle unavailable (no market reads for this network)")
	}
	// depth10 per held asset
	dm, src := s.depthMarket(r, ch)
	in.DepthSource = src
	if dm == nil {
		warns = append(warns, "order books unavailable")
	} else {
		for _, p := range pos {
			if p.Size == 0 {
				continue
			}
			b, err := dm.L2Book(ctx, p.Coin)
			if err != nil {
				warns = append(warns, "book "+p.Coin+": "+err.Error())
				continue
			}
			if d10, ok := Depth10(b); ok {
				in.Depth[riskKey(p.Dex, p.Coin)] = d10
			} else {
				warns = append(warns, "book "+p.Coin+" is empty on the "+src)
			}
		}
	}
	// non-USDC spot is valued at 0 by the vault's NAV
	if bals, err := r.Core.SpotBalances(ctx, vault.Hex()); err != nil {
		warns = append(warns, "spot balances: "+err.Error())
	} else {
		for _, b := range bals {
			if b.Coin == "USDC" || f(b.Total) == 0 {
				continue
			}
			in.HasUnvaluedSpot = true
			if own != nil && mainCtx == nil {
				mainCtx, _ = own.AssetCtxs(ctx, "")
			}
			if c, ok := mainCtx[b.Coin]; ok && c.Mark > 0 {
				in.UnvaluedSpotUSD += f(b.Total) * c.Mark
			} else {
				in.UnvaluedSpotUSD += f(b.EntryNtl)
			}
		}
	}
	v := BuildRisk(in)
	if len(warns) > 0 {
		v.Partial, v.Warnings = true, warns
		warn(m, "manipulation risk is partial: %s", strings.Join(warns, "; "))
	}
	s.riskMu.Lock()
	s.riskCache[key] = v
	s.riskMu.Unlock()
	cp := *v
	return &cp
}

func dexLabel(d string) string {
	if d == "" {
		return "main"
	}
	return d
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
