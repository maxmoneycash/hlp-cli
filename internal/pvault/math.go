package pvault

import (
	"math/big"

	"github.com/maxmoneycash/hlp-cli/internal/evm"
)

var (
	conv     = evm.Pow10(ConvDecimals) // 1e30
	bpsDenom = big.NewInt(10_000)
)

// PreviewDeposit is the shares a deposit of assets (USDC 6 dp) mints at
// sharePrice (1e18 fp): assets × 1e30 / sharePrice. The real price is the
// next settle's; this previews at the given one.
func PreviewDeposit(assets, sharePrice *big.Int) *big.Int {
	if sharePrice == nil || sharePrice.Sign() <= 0 || assets == nil {
		return new(big.Int)
	}
	v := new(big.Int).Mul(assets, conv)
	return v.Quo(v, sharePrice)
}

// PreviewRedeem is the USDC (6 dp) a redemption of shares pays at
// sharePrice after the exit fee: shares × price / 1e30 × (1e4 − exit) / 1e4.
func PreviewRedeem(shares, sharePrice *big.Int, exitFeeBps uint64) *big.Int {
	if shares == nil || sharePrice == nil {
		return new(big.Int)
	}
	v := new(big.Int).Mul(shares, sharePrice)
	v.Quo(v, conv)
	if exitFeeBps > 0 {
		keep := new(big.Int).Sub(bpsDenom, new(big.Int).SetUint64(exitFeeBps))
		if keep.Sign() < 0 {
			keep.SetInt64(0)
		}
		v.Mul(v, keep).Quo(v, bpsDenom)
	}
	return v
}

// SharePriceOf is assets × 1e30 / supply (1e18 fp), 0 without supply.
func SharePriceOf(assets, supply *big.Int) *big.Int {
	if supply == nil || supply.Sign() <= 0 || assets == nil {
		return new(big.Int)
	}
	v := new(big.Int).Mul(assets, conv)
	return v.Quo(v, supply)
}

// FeeResult is one settle's performance-fee computation.
type FeeResult struct {
	PrePrice   *big.Int // pre-fee price, 1e18 fp
	FeeAssets  *big.Int // USDC 6 dp
	FeeShares  *big.Int // minted to feeRecipient
	SharePrice *big.Int // post-fee settle price, 1e18 fp
	NewHWM     *big.Int // ratcheted high-water mark
}

// PerformanceFee reproduces the contract's settle fee math (verified on
// 415/441 settles; the rest differ by 1 micro-USDC
// of rounding). S = supply before settle, A = totalAssets before (6 dp),
// H = high-water mark (1e18 fp), pf = performanceFeeBps. The management fee
// is not modelled (it is 0 on the only deployment).
func PerformanceFee(A, S, H *big.Int, pf uint64) FeeResult {
	r := FeeResult{PrePrice: SharePriceOf(A, S), FeeAssets: new(big.Int), FeeShares: new(big.Int), NewHWM: new(big.Int).Set(H)}
	if r.PrePrice.Cmp(H) > 0 && pf > 0 {
		gain := new(big.Int).Sub(r.PrePrice, H)
		fa := gain.Mul(gain, S)
		fa.Mul(fa, new(big.Int).SetUint64(pf))
		fa.Quo(fa, conv)
		fa.Quo(fa, bpsDenom)
		r.FeeAssets = fa
		den := new(big.Int).Sub(A, fa)
		if den.Sign() > 0 {
			r.FeeShares = new(big.Int).Quo(new(big.Int).Mul(fa, S), den)
		}
	}
	r.SharePrice = SharePriceOf(A, new(big.Int).Add(S, r.FeeShares))
	if r.FeeShares.Sign() > 0 {
		r.NewHWM = new(big.Int).Set(r.SharePrice)
	}
	return r
}

// usd, shares and price are display conversions.
func usd(b evm.Big) float64    { return evm.FloatBig(b, USDCDecimals) }
func shares(b evm.Big) float64 { return evm.FloatBig(b, ShareDecimals) }
func price(b evm.Big) float64  { return evm.FloatBig(b, PriceDecimals) }
