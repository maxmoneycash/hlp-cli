package evm

import "math/big"

var pow10 = func() [37]*big.Int {
	var t [37]*big.Int
	t[0] = big.NewInt(1)
	for i := 1; i < len(t); i++ {
		t[i] = new(big.Int).Mul(t[i-1], big.NewInt(10))
	}
	return t
}()

// Pow10 returns 10^n for 0 <= n <= 36 (shared, do not mutate).
func Pow10(n int) *big.Int { return pow10[n] }

// Float converts a fixed-point integer with dec decimals to float64 through
// big.Rat, so 1e18/1e30 math never passes through float64 before dividing.
// Display only; keep the *big.Int for arithmetic.
func Float(v *big.Int, dec int) float64 {
	if v == nil {
		return 0
	}
	f, _ := new(big.Rat).SetFrac(v, pow10[dec]).Float64()
	return f
}

// FloatBig is Float for a Big.
func FloatBig(v Big, dec int) float64 { return Float(v.V(), dec) }
