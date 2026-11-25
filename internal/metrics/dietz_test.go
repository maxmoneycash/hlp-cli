package metrics

import (
	"math"
	"math/rand"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// growingVault simulates a vault that earns exactly r per week on its
// time-weighted capital while depositors add depositFrac of its equity in the
// middle of every week (a fast-growing vault). It returns weekly account
// value and cumulative PnL, as Hyperliquid's allTime portfolio reports them.
func growingVault(weeks int, r, depositFrac float64) (av, pnl domain.Series) {
	a, p := 1_000_000.0, 0.0
	t := ms(2025, 1, 1)
	av = append(av, domain.Point{T: t, V: a})
	pnl = append(pnl, domain.Point{T: t, V: p})
	for w := 0; w < weeks; w++ {
		dep := depositFrac * a
		earned := r * (a + dep/2) // the new money works for half the week
		a += dep + earned
		p += earned
		t += 7 * dayMs
		av = append(av, domain.Point{T: t, V: a})
		pnl = append(pnl, domain.Point{T: t, V: p})
	}
	return av, pnl
}

// simpleDietz is the old curve (ΔP / A_{i-1}), kept here as the baseline.
func simpleDietz(av, pnl domain.Series) domain.Series {
	out := domain.Series{{T: av[0].T, V: 1}}
	nav := 1.0
	for i := 1; i < len(av); i++ {
		nav *= 1 + (pnl[i].V-pnl[i-1].V)/av[i-1].V
		out = append(out, domain.Point{T: av[i].T, V: nav})
	}
	return out
}

func TestDepositHeavyPeriodsNotOverCredited(t *testing.T) {
	const weekly, weeks = 0.01, 52
	av, pnl := growingVault(weeks, weekly, 0.30) // +30% of equity deposited every week
	truth := math.Pow(1+weekly, weeks)

	got := CurveFromPortfolio(av, pnl)
	if !near(got.Last(), truth, 1e-9) {
		t.Fatalf("modified Dietz NAV %v, want the true %v", got.Last(), truth)
	}
	old := simpleDietz(av, pnl)
	// Simple Dietz credits 1% × 1.15 per week: about 9% more return a year.
	if old.Last() <= truth*1.05 {
		t.Fatalf("baseline should over-credit: simple %v vs true %v", old.Last(), truth)
	}
	if CAGR(got) >= CAGR(old) {
		t.Fatalf("CAGR %v should fall below the over-credited %v", CAGR(got), CAGR(old))
	}
	if hl := Compute(got, nil); !near(hl.TotalReturn, truth-1, 1e-9) {
		t.Fatalf("total return %v", hl.TotalReturn)
	}
}

func TestLargeFlowPeriodSkipped(t *testing.T) {
	// A $1k vault takes a $1M deposit and reports $5k PnL that week. Simple
	// Dietz would call that +500%; the interval is unmeasurable instead.
	day := ms(2025, 1, 1)
	av := domain.Series{{T: day, V: 1000}, {T: day + 7*dayMs, V: 1_006_000}, {T: day + 14*dayMs, V: 1_016_060}}
	pnl := domain.Series{{T: day, V: 0}, {T: day + 7*dayMs, V: 5000}, {T: day + 14*dayMs, V: 15_060}}
	c := CurveFromPortfolio(av, pnl)
	if c[1].V != 1 {
		t.Fatalf("large-flow week moved NAV to %v", c[1].V)
	}
	if !near(c[2].V, 1.01, 1e-12) {
		t.Fatalf("next week %v, want +1%%", c[2].V)
	}
	if old := simpleDietz(av, pnl); old[1].V < 5 {
		t.Fatalf("baseline %v", old[1].V)
	}
}

// gbm samples a geometric Brownian motion with annual drift mu and vol sigma
// at the given gaps (days), deterministically.
func gbm(seed int64, mu, sigma float64, gaps []int) domain.Series {
	rng := rand.New(rand.NewSource(seed))
	t, v := ms(2024, 1, 1), 1.0
	out := domain.Series{{T: t, V: v}}
	for _, g := range gaps {
		dt := float64(g) / 365
		v *= math.Exp((mu-sigma*sigma/2)*dt + sigma*math.Sqrt(dt)*rng.NormFloat64())
		t += int64(g) * dayMs
		out = append(out, domain.Point{T: t, V: v})
	}
	return out
}

func TestIrregularStatsOverUnevenGaps(t *testing.T) {
	const sigma = 0.40
	gaps := make([]int, 0, 400)
	for i := 0; i < 400; i++ {
		gaps = append(gaps, []int{3, 7, 7, 14, 10}[i%5]) // uneven, median 7d
	}
	var vols []float64
	for seed := int64(1); seed <= 20; seed++ {
		c := gbm(seed, 0.2, sigma, gaps)
		if !IsSparse(c) {
			t.Fatal("weekly-ish curve should be sparse")
		}
		vol, _, _, ok := IrregularStats(c)
		if !ok {
			t.Fatal("not ok")
		}
		vols = append(vols, vol)
		if m := Compute(c, nil); m.Vol != vol {
			t.Fatalf("Compute did not use the irregular estimator: %v vs %v", m.Vol, vol)
		}
	}
	mean, _ := meanStd(vols)
	if math.Abs(mean-sigma) > 0.02 {
		t.Fatalf("mean vol %v, want about %v", mean, sigma)
	}
}

func TestIrregularStatsSharpeMatchesDailyConvention(t *testing.T) {
	// For the same process the irregular Sharpe on weekly samples and the
	// classic daily Sharpe should agree (arithmetic drift / vol).
	daily := make([]int, 3000)
	weekly := make([]int, 3000/7)
	for i := range daily {
		daily[i] = 1
	}
	for i := range weekly {
		weekly[i] = 7
	}
	var dS, wS []float64
	for seed := int64(1); seed <= 30; seed++ {
		d := gbm(seed, 0.5, 0.5, daily)
		if IsSparse(d) {
			t.Fatal("daily curve must stay on the daily path")
		}
		dS = append(dS, Compute(d, nil).Sharpe)
		_, s, _, _ := IrregularStats(gbm(seed+1000, 0.5, 0.5, weekly))
		wS = append(wS, s)
	}
	md, _ := meanStd(dS)
	mw, _ := meanStd(wS)
	if math.Abs(md-mw) > 0.3 || math.Abs(mw-1) > 0.3 {
		t.Fatalf("daily Sharpe %v vs irregular %v (true 1.0)", md, mw)
	}
}

func TestIrregularStatsEdges(t *testing.T) {
	if _, _, _, ok := IrregularStats(domain.Series{{T: 0, V: 1}, {T: 7 * dayMs, V: 1.1}}); ok {
		t.Fatal("one interval is not enough")
	}
	// Constant growth: no dispersion, Sharpe undefined (0), no losing interval.
	c := domain.Series{}
	for i := 0; i < 10; i++ {
		c = append(c, domain.Point{T: int64(i) * 7 * dayMs, V: math.Pow(1.01, float64(i))})
	}
	vol, sh, so, ok := IrregularStats(c)
	if !ok || vol > 1e-9 || sh != 0 || so != 0 {
		t.Fatalf("constant growth: %v %v %v %v", vol, sh, so, ok)
	}
	ts := Tearsheet(c, TearsheetOpts{})
	found := false
	for _, w := range ts.Meta.Warnings {
		found = found || w != "" && w[:6] == "sparse"
	}
	if !found {
		t.Fatalf("sparse warning missing: %v", ts.Meta.Warnings)
	}
}
