package pvault

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/maxmoneycash/hlp/internal/evm"
)

// MaxEpochsPerCall is the epochRange page size (100 worked live).
const MaxEpochsPerCall = 100

// maxNavDexes is MAX_NAV_DEXES on the deployed contract.
const maxNavDexes = 4

// Reader reads one chain's strategy vaults. Lens may be zero: reads then use
// the vault's own views (and Core figures need CoreInfo).
type Reader struct {
	RPC  evm.Caller
	Lens evm.Address // zero = no lens on this chain
	// Core serves HyperCore /info for the chain's network. Optional with a
	// lens; required for Core figures without one.
	Core CoreInfo
}

// HasLens reports whether a lens address is configured.
func (r *Reader) HasLens() bool { return !r.Lens.IsZero() }

// ErrNotAVault means the address has no contract code on the chain.
var ErrNotAVault = errors.New("no contract at this address on this chain")

// CheckCode returns ErrNotAVault when addr has no code (one eth_getCode,
// cached by *evm.Client).
func (r *Reader) CheckCode(ctx context.Context, addr evm.Address) error {
	code, err := r.RPC.GetCode(ctx, addr)
	if err != nil {
		return err
	}
	if len(code) == 0 {
		return ErrNotAVault
	}
	return nil
}

// LensVaultState reads VaultLens.vaultState(vault).
func (r *Reader) LensVaultState(ctx context.Context, vault evm.Address) (*VaultState, error) {
	if !r.HasLens() {
		return nil, errors.New("no lens configured for this chain")
	}
	b, err := r.RPC.Call(ctx, r.Lens, evm.Calldata(selVaultState, evm.AddressWord(vault)))
	if err != nil {
		return nil, err
	}
	return DecodeVaultState(b)
}

// UserState reads VaultLens.userState(vault, user), or the vault's own
// per-user views without a lens.
func (r *Reader) UserState(ctx context.Context, vault, user evm.Address) (*UserState, error) {
	if r.HasLens() {
		b, err := r.RPC.Call(ctx, r.Lens, evm.Calldata(selUserState, evm.AddressWord(vault), evm.AddressWord(user)))
		if err != nil {
			return nil, err
		}
		return DecodeUserState(b)
	}
	uw := evm.AddressWord(user)
	sels := []evm.Selector{selBalanceOf, selPendingDepositRequest, selClaimableDepositRequest, selPendingRedeemRequest, selClaimableRedeemRequest, selDepositAllowlistEnabled, selDepositorAllowed}
	msgs := make([]evm.CallMsg, len(sels))
	for i, s := range sels {
		if s == selDepositAllowlistEnabled {
			msgs[i] = evm.CallMsg{To: vault, Data: evm.Calldata(s)}
			continue
		}
		msgs[i] = evm.CallMsg{To: vault, Data: evm.Calldata(s, uw)}
	}
	res, err := r.RPC.BatchCall(ctx, msgs)
	if err != nil {
		return nil, err
	}
	u := &UserState{}
	vals := make([]*big.Int, 5)
	for i := 0; i < 5; i++ {
		if res[i].Err != nil {
			return nil, fmt.Errorf("%s: %w", selectorSignatures[sels[i]], res[i].Err)
		}
		v, err := evm.Words(res[i].Data).Uint(0)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	u.Shares, u.PendingDepositAssets, u.ClaimableShares = evm.NewBig(vals[0]), evm.NewBig(vals[1]), evm.NewBig(vals[2])
	u.PendingRedeemShares, u.ClaimableAssets = evm.NewBig(vals[3]), evm.NewBig(vals[4])
	allow, _ := evm.Words(res[5].Data).Bool(0)
	allowed, _ := evm.Words(res[6].Data).Bool(0)
	u.MayDeposit = !allow || allowed
	return u, nil
}

// Epochs returns settled-epoch data for [from, to] (inclusive, clamped to
// currentEpoch by the lens), paged by MaxEpochsPerCall. Without a lens it
// reads epochData(e) per epoch in batches. Unsettled (all-zero) epochs are
// dropped.
func (r *Reader) Epochs(ctx context.Context, vault evm.Address, from, to uint64) ([]EpochData, error) {
	if from == 0 {
		from = 1
	}
	if to < from {
		return nil, nil
	}
	var out []EpochData
	if r.HasLens() {
		for lo := from; lo <= to; lo += MaxEpochsPerCall {
			hi := min(lo+MaxEpochsPerCall-1, to)
			b, err := r.RPC.Call(ctx, r.Lens, evm.Calldata(selEpochRange, evm.AddressWord(vault), evm.Uint64Word(lo), evm.Uint64Word(hi)))
			if err != nil {
				return out, err
			}
			es, err := DecodeEpochRange(lo, b)
			if err != nil {
				return out, err
			}
			out = appendSettled(out, es)
			if uint64(len(es)) < hi-lo+1 { // clamped at currentEpoch
				break
			}
		}
		return out, nil
	}
	msgs := make([]evm.CallMsg, 0, to-from+1)
	for e := from; e <= to; e++ {
		msgs = append(msgs, evm.CallMsg{To: vault, Data: evm.Calldata(selEpochData, evm.Uint64Word(e))})
	}
	res, err := r.RPC.BatchCall(ctx, msgs)
	if err != nil {
		return nil, err
	}
	for i, x := range res {
		if x.Err != nil {
			continue
		}
		e, err := DecodeEpochData(from+uint64(i), x.Data)
		if err != nil {
			return out, err
		}
		out = appendSettled(out, []EpochData{*e})
	}
	return out, nil
}

func appendSettled(out, es []EpochData) []EpochData {
	for _, e := range es {
		if e.Settled() {
			out = append(out, e)
		}
	}
	return out
}

// view is one tolerant direct-view read.
type view struct {
	sel   evm.Selector
	args  []evm.Word
	name  string
	apply func(evm.Words) error
}

func (v view) label() string {
	if v.name != "" {
		return v.name
	}
	return selectorSignatures[v.sel]
}

func u64Into(p *uint64) func(evm.Words) error {
	return func(w evm.Words) (err error) { *p, err = w.Uint64(0); return }
}

func bigInto(p *evm.Big) func(evm.Words) error {
	return func(w evm.Words) error {
		v, err := w.Uint(0)
		*p = evm.NewBig(v)
		return err
	}
}

func addrInto(p *evm.Address) func(evm.Words) error {
	return func(w evm.Words) (err error) { *p, err = w.Address(0); return }
}

func boolInto(p *bool) func(evm.Words) error {
	return func(w evm.Words) (err error) { *p, err = w.Bool(0); return }
}

// runViews batches views; a failing view is recorded in unknown, never
// fatal (the shipped ABI is newer than the deployment).
func (r *Reader) runViews(ctx context.Context, vault evm.Address, vs []view) (unknown []string, err error) {
	msgs := make([]evm.CallMsg, len(vs))
	for i, v := range vs {
		msgs[i] = evm.CallMsg{To: vault, Data: evm.Calldata(v.sel, v.args...)}
	}
	res, err := r.RPC.BatchCall(ctx, msgs)
	if err != nil {
		return nil, err
	}
	for i, v := range vs {
		if res[i].Err != nil {
			unknown = append(unknown, v.label())
			continue
		}
		w, err := evm.NewWords(res[i].Data)
		if err == nil {
			err = v.apply(w)
		}
		if err != nil {
			unknown = append(unknown, v.label())
		}
	}
	return unknown, nil
}

// Details reads identity, config, fees, roles and transfer bookkeeping in
// one batch. Views that revert are listed in Details.Unknown.
func (r *Reader) Details(ctx context.Context, vault evm.Address) (*Details, error) {
	d := &Details{}
	var pausedUntil uint64
	var navCount uint64
	navs := make([]uint64, maxNavDexes)
	navOK := make([]bool, maxNavDexes)
	vs := []view{
		{sel: selName, apply: func(w evm.Words) (err error) { d.Name, err = w.String(0); return }},
		{sel: selSymbol, apply: func(w evm.Words) (err error) { d.Symbol, err = w.String(0); return }},
		{sel: selDecimals, apply: func(w evm.Words) (err error) { d.Decimals, err = w.Uint8(0); return }},
		{sel: selAsset, apply: addrInto(&d.Asset)},
		{sel: selEpochDuration, apply: u64Into(&d.EpochDuration)},
		{sel: selSettleGrace, apply: u64Into(&d.SettleGrace)},
		{sel: selMinDeposit, apply: bigInto(&d.MinDeposit)},
		{sel: selDepositCap, apply: bigInto(&d.DepositCap)},
		{sel: selMinIdleBps, apply: u64Into(&d.MinIdleBps)},
		{sel: selMaxPriceMoveBps, apply: u64Into(&d.MaxPriceMoveBps)},
		{sel: selCoreSettleDelayBlocks, apply: u64Into(&d.CoreSettleDelayBlocks)},
		{sel: selMaxDeploy, apply: bigInto(&d.MaxDeploy)},
		{sel: selQuotaMode, apply: boolInto(&d.QuotaMode)},
		{sel: selDepositAllowlistEnabled, apply: boolInto(&d.DepositAllowlist)},
		{sel: selManagementFeeBps, apply: u64Into(&d.ManagementFeeBps)},
		{sel: selPerformanceFeeBps, apply: u64Into(&d.PerformanceFeeBps)},
		{sel: selExitFeeBps, apply: u64Into(&d.ExitFeeBps)},
		{sel: selOwner, apply: addrInto(&d.Owner)},
		{sel: selPendingOwner, apply: addrInto(&d.PendingOwner)},
		{sel: selKeeper, apply: addrInto(&d.Keeper)},
		{sel: selGuardian, apply: addrInto(&d.Guardian)},
		{sel: selManager, apply: addrInto(&d.Manager)},
		{sel: selFeeRecipient, apply: addrInto(&d.FeeRecipient)},
		{sel: selAPIWallet, apply: addrInto(&d.APIWallet)},
		{sel: selLastSharePrice, apply: bigInto(&d.LastSharePrice)},
		{sel: selLastFeeAccrual, apply: u64Into(&d.LastFeeAccrual)},
		{sel: selConfirmedToCore, apply: bigInto(&d.ConfirmedToCore)},
		{sel: selConfirmedToEvm, apply: bigInto(&d.ConfirmedToEVM)},
		{sel: selPendingTransfer, apply: func(w evm.Words) error {
			p, err := DecodePendingTransfer(w)
			if err == nil {
				d.PendingTransfer = *p
			}
			return err
		}},
		{sel: selPaused, apply: boolInto(&d.Paused)},
		{sel: selPausedUntil, apply: u64Into(&pausedUntil)},
		{sel: selNavDexCount, apply: u64Into(&navCount)},
	}
	for i := 0; i < maxNavDexes; i++ {
		i := i
		vs = append(vs, view{sel: selNavDexes, args: []evm.Word{evm.Uint64Word(uint64(i))}, name: fmt.Sprintf("navDexes(%d)", i),
			apply: func(w evm.Words) (err error) {
				navs[i], err = w.Uint64(0)
				navOK[i] = err == nil
				return
			}})
	}
	unknown, err := r.runViews(ctx, vault, vs)
	if err != nil {
		return nil, err
	}
	for _, u := range unknown {
		if strings.HasPrefix(u, "navDexes(") {
			continue // out-of-range indices revert by design
		}
		d.Unknown = append(d.Unknown, u) // e.g. pausedUntil() on the older deployment
	}
	if !contains(unknown, "pausedUntil()") {
		d.PausedUntil = &pausedUntil
	}
	for i := 0; i < int(min(navCount, maxNavDexes)); i++ {
		if navOK[i] {
			d.NavDexes = append(d.NavDexes, uint32(navs[i]))
		}
	}
	return d, nil
}

// StateFromViews assembles a VaultState without a lens: EVM figures from
// the vault's views, Core figures from CoreInfo (summed over navDexes).
// deployLimit and inFlightToCore are not derivable and stay zero; they are
// listed in the returned unknown names.
func (r *Reader) StateFromViews(ctx context.Context, vault evm.Address, d *Details) (*VaultState, []string, error) {
	s := &VaultState{}
	var phase uint64
	vs := []view{
		{sel: selCurrentEpoch, apply: u64Into(&s.Epoch)},
		{sel: selPhase, apply: u64Into(&phase)},
		{sel: selCutoffAt, apply: u64Into(&s.CutoffAt)},
		{sel: selTotalAssets, apply: bigInto(&s.TotalAssets)},
		{sel: selTotalSupply, apply: bigInto(&s.TotalSupply)},
		{sel: selSharePrice, apply: bigInto(&s.SharePrice)},
		{sel: selHighWaterMark, apply: bigInto(&s.HighWaterMark)},
		{sel: selTotalPendingDeposits, apply: bigInto(&s.TotalPendingDeposits)},
		{sel: selTotalPendingRedeemShares, apply: bigInto(&s.TotalPendingRedeemShares)},
		{sel: selRedeemReserve, apply: bigInto(&s.RedeemReserve)},
		{sel: selIdleAssets, apply: bigInto(&s.IdleAssets)},
		{sel: selCoreLinkProven, apply: boolInto(&s.CoreLinkProven)},
	}
	if !d.Manager.IsZero() {
		vs = append(vs, view{sel: selBalanceOf, args: []evm.Word{evm.AddressWord(d.Manager)}, name: "managerShares", apply: bigInto(&s.ManagerShares)})
	}
	unknown, err := r.runViews(ctx, vault, vs)
	if err != nil {
		return nil, nil, err
	}
	s.Phase = Phase(phase)
	s.SettleDeadline = s.CutoffAt + d.SettleGrace
	s.QuotaMode, s.Paused, s.APIWallet, s.Manager = d.QuotaMode, d.Paused, d.APIWallet, d.Manager
	s.PendingKind, s.PendingAmount, s.PendingSentAt = d.PendingTransfer.Kind, d.PendingTransfer.Amount, d.PendingTransfer.SentAt
	s.DeployLimit, s.InFlightToCore = evm.NewBig(nil), evm.NewBig(nil)
	unknown = append(unknown, "deployLimit", "inFlightToCore", "pendingAction")
	s.CoreSpot, s.CorePerpEquity, s.CorePerpWithdrawable = evm.NewBig(nil), evm.NewBig(nil), evm.NewBig(nil)
	if r.Core == nil {
		unknown = append(unknown, "coreSpot", "corePerpEquity", "corePerpWithdrawable")
		return s, unknown, nil
	}
	user := vault.Hex()
	dexes := d.NavDexes
	if len(dexes) == 0 {
		dexes = []uint32{0}
	}
	names, nerr := r.dexNames(ctx, dexes)
	eq, wd := new(big.Int), new(big.Int)
	for _, dx := range dexes {
		name := ""
		if dx != 0 {
			if nerr != nil || int(dx) >= len(names) {
				unknown = append(unknown, fmt.Sprintf("corePerpEquity(dex %d)", dx))
				continue
			}
			name = names[dx]
		}
		ch, err := r.Core.ClearinghouseState(ctx, user, name)
		if err != nil {
			unknown = append(unknown, fmt.Sprintf("corePerpEquity(dex %d)", dx))
			continue
		}
		s.CoreAccountExists = true
		eq.Add(eq, usdcUnits(ch.AccountValue))
		wd.Add(wd, usdcUnits(ch.Withdrawable))
	}
	s.CorePerpEquity, s.CorePerpWithdrawable = evm.NewBig(eq), evm.NewBig(wd)
	if bals, err := r.Core.SpotBalances(ctx, user); err == nil {
		for _, b := range bals {
			if b.Coin == "USDC" {
				s.CoreSpot = evm.NewBig(usdcUnits(b.Total))
			}
		}
	} else {
		unknown = append(unknown, "coreSpot")
	}
	return s, unknown, nil
}

func (r *Reader) dexNames(ctx context.Context, dexes []uint32) ([]string, error) {
	for _, d := range dexes {
		if d != 0 {
			return r.Core.PerpDexNames(ctx)
		}
	}
	return []string{""}, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
