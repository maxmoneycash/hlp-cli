package core

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid/ws"
	"github.com/maxmoneycash/hlp-cli/internal/paperx"
)

func shortAddr(a string) string {
	if len(a) < 10 {
		return a
	}
	return a[:6] + "…" + a[len(a)-4:]
}

func runAccount(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*AddressInput)
	if id, ok := paperx.ParseAddress(p.Address); ok {
		if id, err := paperID(id); err != nil {
			return nil, err
		} else {
			return paperAccountResult(ctx, env, id)
		}
	}
	addr, err := env.address(p.Address)
	if err != nil {
		return nil, err
	}
	a, err := env.HL.Account(ctx, addr)
	if err != nil {
		return nil, err
	}
	if a.Address == "" {
		a.Address = addr
	}
	sortPositions(a.Positions)
	meta := env.meta("hyperliquid")
	if a.Partial {
		meta.Partial = true
		meta.Warnings = append(meta.Warnings, a.Warnings...)
	}
	return &Result{Card: CardAccount, Title: "Account " + shortAddr(addr), Data: a, Meta: meta}, nil
}

// sortPositions orders by USD notional, largest first.
func sortPositions(ps []domain.Position) {
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Value > ps[j].Value })
}

// MergePerp overlays a WebSocket perp snapshot (positions, perp equity,
// margin) onto a REST account that also carries spot and Earn, and
// recomputes Equity = perps + spot + Earn.
func MergePerp(base, live domain.Account) domain.Account {
	out := base
	out.Positions = live.Positions
	out.StreamGeneration = live.StreamGeneration
	out.PerpEquity = live.PerpEquity
	out.MarginUsed = live.MarginUsed
	out.Withdrawable = live.Withdrawable
	out.GrossNtl = live.GrossNtl
	out.NetNtl = live.NetNtl
	if live.OpenOrders != nil {
		out.OpenOrders = live.OpenOrders
	}
	if live.Time != 0 {
		out.Time = live.Time
	}
	out.Equity = out.PerpEquity + out.SpotUSD + out.EarnUSD
	out.Partial = base.Partial || live.Partial
	out.Warnings = appendUnique(append([]string(nil), base.Warnings...), live.Warnings...)
	if base.SpotUSD != 0 || base.EarnUSD != 0 {
		const prefix = "spot and Earn balances remain from the REST snapshot"
		hasNote := false
		for _, warning := range out.Warnings {
			hasNote = hasNote || strings.HasPrefix(warning, prefix)
		}
		if !hasNote {
			note := prefix
			if base.Time > 0 {
				note += " (account timestamp " + time.UnixMilli(base.Time).UTC().Format(time.RFC3339) + ")"
			}
			out.Warnings = append(out.Warnings, note)
		}
	}
	sortPositions(out.Positions)
	return out
}

func streamAccount(ctx context.Context, env *Env, in any, emit func(*Result)) error {
	if id, ok := paperx.ParseAddress(in.(*AddressInput).Address); ok {
		id, err := paperID(id)
		if err != nil {
			return err
		}
		return streamPaperAccount(ctx, env, id, emit)
	}
	return watchAddress(ctx, env, in.(*AddressInput), false, emit)
}

func runFills(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*FillsInput)
	pid, paper := paperx.ParseAddress(p.Address)
	var addr string
	var err error
	if paper {
		if pid, err = paperID(pid); err != nil {
			return nil, err
		}
	} else if addr, err = env.address(p.Address); err != nil {
		return nil, err
	}
	hours := p.Hours
	if hours == 0 {
		hours = 24
	}
	if hours < 1 || hours > 24*365 {
		return nil, Errorf(CodeUsage, "hours must be 1-8760, got %d", hours)
	}
	if paper {
		return paperFillsResult(ctx, env, pid, p, hours)
	}
	coin := ""
	if p.Coin != "" {
		m, err := env.Resolve(ctx, p.Coin)
		if err != nil {
			return nil, err
		}
		coin = m.Coin
	}
	end := env.now()
	fills, err := env.HL.Fills(ctx, addr, end.Add(-time.Duration(hours)*time.Hour).UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	out := make([]domain.Fill, 0, len(fills))
	for _, f := range fills {
		if coin == "" || f.Coin == coin {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return &Result{Card: CardFills, Title: "Fills " + shortAddr(addr), Data: out, Meta: env.meta("hyperliquid")}, nil
}

func runWatch(ctx context.Context, env *Env, in any) (*Result, error) {
	res, err := runAccount(ctx, env, in)
	if err != nil {
		return nil, err
	}
	res.Title = "Watching " + shortAddr(res.Data.(domain.Account).Address)
	return res, nil
}

func streamWatch(ctx context.Context, env *Env, in any, emit func(*Result)) error {
	return watchAddress(ctx, env, in.(*AddressInput), true, emit)
}

// watchAddress emits the REST snapshot, then live account updates (perp
// part from WS merged over spot/Earn) and, when withFills, each batch of
// new fills as a fills card.
//
// A seed (WithSeed: the caller's own fresh snapshot for the same address)
// replaces the REST snapshot, so a TUI card that regains focus resumes
// streaming at 0 weight (PERFORMANCE #4).
func watchAddress(ctx context.Context, env *Env, p *AddressInput, withFills bool, emit func(*Result)) error {
	res, seeded := seededAccount(ctx, env, p)
	if !seeded {
		var err error
		if res, err = runAccount(ctx, env, p); err != nil {
			return err
		}
		if withFills {
			res.Title = "Watching " + shortAddr(res.Data.(domain.Account).Address)
		}
		emit(res)
	}
	if env.Stream == nil {
		return nil
	}
	base := res.Data.(domain.Account)
	addr := base.Address
	pos, cancelPos, err := ws.Positions(ctx, env.Stream, addr)
	if err != nil {
		return err
	}
	defer cancelPos()
	var fills <-chan []domain.Fill
	if withFills {
		ch, cancelFills, err := ws.UserFills(ctx, env.Stream, addr)
		if err != nil {
			return err
		}
		defer cancelFills()
		fills = ch
	}
	title := res.Title
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case live, ok := <-pos:
			if !ok {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return &Error{Code: CodeNetwork, Message: "account positions stream closed", Retryable: true}
			}
			base = MergePerp(base, live)
			meta := env.meta("hyperliquid-ws")
			meta.Partial, meta.Warnings = base.Partial, append([]string(nil), base.Warnings...)
			emit(&Result{Card: CardAccount, Title: title, Data: base, Meta: meta})
		case batch, ok := <-fills:
			if !ok {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return &Error{Code: CodeNetwork, Message: "account fills stream closed", Retryable: true}
			}
			if len(batch) == 0 {
				continue
			}
			sort.SliceStable(batch, func(i, j int) bool { return batch[i].Time > batch[j].Time })
			emit(&Result{Card: CardFills, Title: "Fills " + shortAddr(addr), Data: batch, Meta: env.meta("hyperliquid-ws")})
		}
	}
}

// seededAccount returns the ctx seed when it is an account card for the
// address p resolves to.
func seededAccount(ctx context.Context, env *Env, p *AddressInput) (*Result, bool) {
	seed, ok := SeedFrom(ctx)
	if !ok || seed.Card != CardAccount {
		return nil, false
	}
	a, ok := seed.Data.(domain.Account)
	if !ok {
		return nil, false
	}
	addr, err := env.address(p.Address)
	if err != nil || !strings.EqualFold(a.Address, addr) {
		return nil, false
	}
	return seed, true
}

// ---- hlp positioning ----

// HLPChildren are the HLP child vaults; the live
// list from vaultDetails.relationship wins when available.
var HLPChildren = []string{
	"0x469f690213c467c39a23efacfd2816896009d7d8", // Strategy X
	"0xb0a55f13d22f66e6d495ac98113841b2326e9540", // Liquidator 2
	"0x010461c14e146ac35fe42271bdc1134ee31c703a", // Strategy A
	"0x31ca8395cf837de08b24da3f660e77761dfb974b", // Strategy B
	"0x2e3d94f0562703b25c83308a05046ddaf9a8dd14", // Liquidator
	"0x5e177e5e39c0f4e421f5865a6d8beed8d921cb70", // Liquidator 3
	"0x2ed5c4484ea3ff8b57d5f2fb152a40d9f2b68308", // Liquidator 4
}

// vaultChildrenReader is the cheap child-list read (*hyperliquid.Client:
// vaultDetails only, memoized for an hour). Fakes without it fall back to
// Vault, which also fans out to the vault's Account.
type vaultChildrenReader interface {
	VaultChildren(ctx context.Context, vault string) ([]string, error)
}

func hlpMembers(ctx context.Context, env *Env) ([]string, []string) {
	var warns []string
	children := HLPChildren
	var kids []string
	var err error
	if vc, ok := env.HL.(vaultChildrenReader); ok {
		kids, err = vc.VaultChildren(ctx, HLPAddress)
	} else {
		var d domain.VaultDetail
		d, err = env.HL.Vault(ctx, HLPAddress)
		kids = d.Children
	}
	switch {
	case err != nil:
		warns = append(warns, "HLP children from the built-in list: "+AsError(err).Message)
	case len(kids) > 0:
		children = kids
	}
	return append([]string{HLPAddress}, children...), warns
}

// AggregatePositions nets positions per coin across accounts: sizes, USD
// value and PnL are summed, entry is size-weighted, leverage and liquidation
// prices are dropped (they are per-account). Spot, Earn and perp equity add up.
func AggregatePositions(addr string, accts []domain.Account) domain.Account {
	out := domain.Account{Address: addr, Role: "vault", Aggregated: true}
	type agg struct {
		p        domain.Position
		entryNtl float64
	}
	byCoin := map[string]*agg{}
	var order []string
	holders := map[string][]string{} // coin -> accounts holding it (AGENT-UX #28)
	for _, a := range accts {
		out.Equity += a.Equity
		out.PerpEquity += a.PerpEquity
		out.SpotUSD += a.SpotUSD
		out.EarnUSD += a.EarnUSD
		out.Withdrawable += a.Withdrawable
		out.MarginUsed += a.MarginUsed
		out.Time = max(out.Time, a.Time)
		out.Partial = out.Partial || a.Partial
		out.Warnings = append(out.Warnings, a.Warnings...)
		for _, p := range a.Positions {
			x, ok := byCoin[p.Coin]
			if !ok {
				x = &agg{p: domain.Position{Coin: p.Coin, Dex: p.Dex, Cross: true, Aggregated: true}}
				byCoin[p.Coin] = x
				order = append(order, p.Coin)
			}
			value := math.Abs(p.Value)
			signedVal := value
			if p.Size < 0 {
				signedVal = -value
				out.ShortNtl += value
				x.p.ShortValue += value
			} else {
				out.LongNtl += value
				x.p.LongValue += value
			}
			out.GrossNtl += value
			out.NetNtl += signedVal
			x.p.GrossValue += value
			x.p.Legs = append(x.p.Legs, domain.PositionLeg{Vault: strings.ToLower(a.Address), Coin: p.Coin, Size: p.Size,
				Entry: p.Entry, Mark: p.Mark, Value: value, UPnL: p.UPnL, ROE: p.ROE, Leverage: p.Leverage, Cross: p.Cross,
				LiqPx: p.LiqPx, MarginUsed: p.MarginUsed, FundingAll: p.FundingAll})
			if a.Address != "" && p.Size != 0 {
				addr := strings.ToLower(a.Address)
				holders[p.Coin] = append(holders[p.Coin], addr)
				if x.p.ByVault == nil {
					x.p.ByVault = map[string]float64{}
				}
				x.p.ByVault[addr] += p.Size
			}
			x.p.Size += p.Size
			x.p.Value += signedVal // signed while aggregating
			x.p.UPnL += p.UPnL
			x.p.MarginUsed += p.MarginUsed
			x.p.FundingAll += p.FundingAll
			x.entryNtl += p.Entry * p.Size
			if p.Mark != 0 {
				x.p.Mark = p.Mark
			}
		}
	}
	for _, c := range order {
		x := byCoin[c]
		if x.p.Size == 0 && x.p.GrossValue == 0 {
			continue
		}
		if x.p.Size != 0 {
			x.p.Entry = x.entryNtl / x.p.Size
		}
		x.p.Vaults = holders[c]
		if len(holders[c]) == 1 {
			x.p.Vault = holders[c][0]
		}
		if x.p.Value < 0 {
			x.p.Value = -x.p.Value
		}
		// Netting opposite legs across accounts makes UPnL/margin
		// meaningless (a hedged leg's PnL lands on a tiny net margin), so
		// the aggregate ROE is the unlevered price return of the net
		// position from its blended entry.
		if x.p.Entry > 0 && x.p.Mark > 0 {
			x.p.ROE = x.p.Mark/x.p.Entry - 1
			if x.p.Size < 0 {
				x.p.ROE = -x.p.ROE
			}
		}
		out.Positions = append(out.Positions, x.p)
	}
	sortPositions(out.Positions)
	return out
}

// fetchAccounts reads several accounts two at a time: each Account is
// itself ~14 requests, so a wider fan-out bursts into 429s (PERFORMANCE #1).
func fetchAccounts(ctx context.Context, env *Env, addrs []string) ([]domain.Account, []string) {
	accts := make([]domain.Account, len(addrs))
	errs := make([]error, len(addrs))
	parallelN(len(addrs), 2, func(i int) {
		accts[i], errs[i] = env.HL.Account(ctx, addrs[i])
	})
	var ok []domain.Account
	var warns []string
	for i, err := range errs {
		if err != nil {
			warns = append(warns, shortAddr(addrs[i])+": "+AsError(err).Message)
			continue
		}
		ok = append(ok, accts[i])
	}
	return ok, warns
}

// hlpSnap is the last per-account HLP state of one Env, so a live card
// that restarts (focus, scroll) within hlpSnapTTL resumes without the
// ~836-weight fetch (PERFORMANCE #1, #4).
type hlpSnap struct {
	at      time.Time
	members []string
	byAddr  map[string]domain.Account
}

const hlpSnapTTL = 60 * time.Second

var hlpSnaps sync.Map // *Env -> hlpSnap

func storeHLPSnap(env *Env, members []string, byAddr map[string]domain.Account) {
	cp := make(map[string]domain.Account, len(byAddr))
	for k, v := range byAddr {
		cp[k] = v
	}
	hlpSnaps.Store(env, hlpSnap{at: env.now(), members: members, byAddr: cp})
}

func loadHLPSnap(env *Env) (hlpSnap, bool) {
	v, ok := hlpSnaps.Load(env)
	if !ok {
		return hlpSnap{}, false
	}
	s := v.(hlpSnap)
	if env.now().Sub(s.at) >= hlpSnapTTL {
		return hlpSnap{}, false
	}
	return s, true
}

// loadHLP is the one HLP fetch: members (cached child list), then every
// account two at a time, aggregated.
func loadHLP(ctx context.Context, env *Env) (*Result, []string, []domain.Account, error) {
	members, warns := hlpMembers(ctx, env)
	accts, w2 := fetchAccounts(ctx, env, members)
	warns = append(warns, w2...)
	if len(accts) == 0 {
		return nil, members, nil, &Error{Code: CodeAPI, Message: "could not load any HLP account", Details: warns}
	}
	meta := env.meta("hyperliquid")
	meta.Warnings = warns
	meta.Partial = len(accts) < len(members)
	agg := AggregatePositions(HLPAddress, accts)
	byAddr := make(map[string]domain.Account, len(accts))
	for _, a := range accts {
		byAddr[strings.ToLower(a.Address)] = a
	}
	storeHLPSnap(env, members, byAddr)
	return &Result{Card: CardAccount, Title: hlpTitle(members), Data: agg, Meta: meta}, members, accts, nil
}

func hlpTitle(members []string) string {
	return "HLP positioning (parent + " + itoa(len(members)-1) + " children)"
}

func runHLP(ctx context.Context, env *Env, _ any) (*Result, error) {
	r, _, _, err := loadHLP(ctx, env)
	return r, err
}

// streamHLP emits one snapshot, then re-aggregates on every child's WS
// position update. It fetches once (never twice, as before), and not at all
// when seeded with a fresh card whose per-account state this Env still holds.
func streamHLP(ctx context.Context, env *Env, in any, emit func(*Result)) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	var members []string
	byAddr := map[string]domain.Account{}
	title := ""
	_, seeded := SeedFrom(ctx)
	if snap, ok := loadHLPSnap(env); seeded && ok {
		members, byAddr, title = snap.members, snap.byAddr, hlpTitle(snap.members)
	} else {
		res, ms, accts, err := loadHLP(ctx, env)
		if err != nil {
			return err
		}
		emit(res)
		members, title = ms, res.Title
		for _, a := range accts {
			byAddr[strings.ToLower(a.Address)] = a
		}
	}
	if env.Stream == nil {
		return nil
	}
	type upd struct {
		addr string
		a    domain.Account
	}
	updates := make(chan upd, 16)
	closed := make(chan string, len(members))
	live := make(map[string]bool, len(members))
	// Every child subscription is multiplexed on this Env's same WS connection.
	var generation int64
	for _, member := range members {
		m := strings.ToLower(member)
		ch, cancel, err := ws.Positions(ctx, env.Stream, m)
		if err != nil {
			return &Error{Code: CodeNetwork, Message: "HLP positions subscription failed for " + shortAddr(m) + ": " + AsError(err).Message, Retryable: true}
		}
		defer cancel()
		go func(addr string, ch <-chan domain.Account) {
			defer func() { closed <- addr }()
			for a := range ch {
				select {
				case updates <- upd{addr, a}:
				case <-ctx.Done():
					return
				}
			}
		}(m, ch)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case address := <-closed:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &Error{Code: CodeNetwork, Message: "HLP positions stream closed for " + shortAddr(address), Retryable: true}
		case u := <-updates:
			if u.a.StreamGeneration < generation {
				continue // a buffered frame from the previous socket
			}
			if u.a.StreamGeneration > generation {
				clear(live)
				generation = u.a.StreamGeneration
			}
			live[u.addr] = true
			merged := MergePerp(byAddr[u.addr], u.a)
			if merged.Address == "" {
				merged.Address = u.addr
			}
			byAddr[u.addr] = merged
			storeHLPSnap(env, members, byAddr)
			all := make([]domain.Account, 0, len(byAddr))
			meta := env.meta("hyperliquid-ws")
			for _, m := range members {
				if a, ok := byAddr[strings.ToLower(m)]; ok {
					all = append(all, a)
					if !live[strings.ToLower(m)] {
						meta.Partial = true
						meta.Warnings = append(meta.Warnings, "waiting for live HLP positions from "+shortAddr(m))
					}
				} else {
					meta.Partial = true
					meta.Warnings = append(meta.Warnings, "HLP positions unavailable for "+shortAddr(m))
				}
			}
			aggregate := AggregatePositions(HLPAddress, all)
			if len(live) == len(members) {
				aggregate.StreamGeneration = generation
			}
			meta.Partial = meta.Partial || aggregate.Partial
			meta.Warnings = append(meta.Warnings, aggregate.Warnings...)
			emit(&Result{Card: CardAccount, Title: title, Data: aggregate, Meta: meta})
		}
	}
}
