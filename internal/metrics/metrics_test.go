package metrics

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

const fixtures = "../../testdata/fixtures/hlpxyz"

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func ms(y int, m time.Month, d int) int64 {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
}

// dailyCurve builds n daily points starting at start with value f(i).
func dailyCurve(start int64, n int, f func(i int) float64) domain.Series {
	s := make(domain.Series, n)
	for i := range s {
		s[i] = domain.Point{T: start + int64(i)*dayMs, V: f(i)}
	}
	return s
}

func TestConstantGrowth(t *testing.T) {
	c := dailyCurve(ms(2026, 1, 1), 366, func(i int) float64 { return math.Pow(1.001, float64(i)) })
	dd, pk, tr := MaxDrawdown(c)
	if dd != 0 || pk != 0 || tr != 0 {
		t.Fatalf("MaxDD of a rising curve = %v (%d,%d), want 0", dd, pk, tr)
	}
	// Constant returns have zero dispersion: Sharpe would be +Inf, guarded to 0.
	if s := Sharpe(Returns(c), 365); s != 0 {
		t.Fatalf("Sharpe of constant returns = %v, want 0 (guarded +Inf)", s)
	}
	if s := Sortino(Returns(c), 365); s != 0 {
		t.Fatalf("Sortino with no losing periods = %v, want 0", s)
	}
	want := math.Pow(1.001, 365) - 1
	if got := CAGR(c); !near(got, want, 1e-9) {
		t.Fatalf("CAGR = %v, want %v", got, want)
	}
	m := Compute(c, nil)
	if m.MaxDD != 0 || m.Sharpe != 0 || m.Calmar != 0 || !near(m.TotalReturn, want, 1e-9) || m.Days != 365 {
		t.Fatalf("Compute = %+v", m)
	}
}

func TestCrash(t *testing.T) {
	c := domain.Series{{T: 1, V: 1}, {T: 2, V: 2}, {T: 3, V: 1}, {T: 4, V: 1.5}}
	dd, pk, tr := MaxDrawdown(c)
	if dd != -0.5 || pk != 2 || tr != 3 {
		t.Fatalf("MaxDrawdown = %v %d %d, want -0.5 2 3", dd, pk, tr)
	}
	want := []float64{0, 0, -0.5, -0.25}
	for i, p := range Drawdown(c) {
		if p.V != want[i] || p.T != c[i].T {
			t.Fatalf("Drawdown[%d] = %+v, want %v", i, p, want[i])
		}
	}
}

func TestReturnsAndRatios(t *testing.T) {
	c := domain.Series{{T: 0, V: 100}, {T: 1, V: 101}, {T: 2, V: 99.99}, {T: 3, V: 101.9898}, {T: 4, V: 101.9898}}
	r := Returns(c)
	want := []float64{0.01, -0.01, 0.02, 0}
	for i := range want {
		if !near(r[i], want[i], 1e-12) {
			t.Fatalf("Returns = %v, want %v", r, want)
		}
	}
	// mean 0.005, sample std sqrt(0.0005/3)
	wantSharpe := 0.005 / math.Sqrt(0.0005/3) * math.Sqrt(365)
	if got := Sharpe(want, 365); !near(got, wantSharpe, 1e-9) {
		t.Fatalf("Sharpe = %v, want %v", got, wantSharpe)
	}
	// downside dev sqrt(0.0001/4) = 0.005 -> mean/dd = 1
	if got := Sortino(want, 365); !near(got, math.Sqrt(365), 1e-9) {
		t.Fatalf("Sortino = %v, want %v", got, math.Sqrt(365))
	}
	if Sharpe([]float64{0.1}, 365) != 0 || Sharpe(nil, 365) != 0 || Sharpe(want, 0) != 0 {
		t.Fatal("degenerate Sharpe inputs must return 0")
	}
	if Returns(domain.Series{{T: 0, V: 0}, {T: 1, V: 1}, {T: 2, V: 2}})[0] != 1 {
		t.Fatal("Returns must skip periods starting at 0")
	}
}

func TestPeriodsPerYear(t *testing.T) {
	d := dailyCurve(0, 10, func(int) float64 { return 1 })
	if got := PeriodsPerYear(d); got != 365 {
		t.Fatalf("daily = %v", got)
	}
	h := domain.Series{{T: 0}, {T: 3_600_000}, {T: 7_200_000}, {T: 7_200_000 + 86_400_000}}
	if got := PeriodsPerYear(h); got != 8760 {
		t.Fatalf("hourly median = %v, want 8760", got)
	}
	if PeriodsPerYear(domain.Series{{T: 5}}) != 0 || PeriodsPerYear(domain.Series{{T: 5}, {T: 5}}) != 0 {
		t.Fatal("degenerate spacing must give 0")
	}
}

func TestNormalize(t *testing.T) {
	n := Normalize(domain.Series{{T: 1, V: 0}, {T: 2, V: 4}, {T: 3, V: 6}})
	if len(n) != 2 || n[0].V != 1 || n[1].V != 1.5 || n[0].T != 2 {
		t.Fatalf("Normalize = %+v", n)
	}
	if Normalize(domain.Series{{T: 1, V: 0}}) != nil {
		t.Fatal("all-zero series has no base")
	}
}

func TestDaily(t *testing.T) {
	d0 := ms(2026, 3, 1)
	s := domain.Series{
		{T: d0 + 3600_000, V: 1},
		{T: d0 + 7200_000, V: 2}, // last of day 0
		{T: d0 + 3*dayMs + 5, V: 3},
	}
	got := Daily(s)
	want := []float64{2, 2, 2, 3}
	if len(got) != 4 {
		t.Fatalf("Daily len %d: %+v", len(got), got)
	}
	for i, p := range got {
		if p.V != want[i] || p.T != d0+int64(i)*dayMs {
			t.Fatalf("Daily[%d] = %+v, want %v at %d", i, p, want[i], d0+int64(i)*dayMs)
		}
	}
}

func TestMonthlyThreeMonths(t *testing.T) {
	// Daily series Jan 1 .. Mar 31 2026: 1.0 through Jan 31, 1.1 in Feb, 0.99 in Mar.
	start := ms(2026, 1, 1)
	c := dailyCurve(start, 90, func(i int) float64 {
		switch m := time.UnixMilli(start + int64(i)*dayMs).UTC().Month(); m {
		case time.January:
			return 1.0
		case time.February:
			return 1.1
		default:
			return 0.99
		}
	})
	got := Monthly(c)
	want := []domain.MonthlyReturn{{Year: 2026, Month: 1}, {Year: 2026, Month: 2, Ret: 0.1}, {Year: 2026, Month: 3, Ret: -0.1}}
	if len(got) != 3 {
		t.Fatalf("Monthly = %+v", got)
	}
	for i := range want {
		if got[i].Year != want[i].Year || got[i].Month != want[i].Month || !near(got[i].Ret, want[i].Ret, 1e-12) {
			t.Fatalf("Monthly[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	m := Compute(c, nil)
	if !near(m.BestMonth, 0.1, 1e-12) || !near(m.WorstMonth, -0.1, 1e-12) {
		t.Fatalf("best/worst = %v/%v", m.BestMonth, m.WorstMonth)
	}
}

func TestWindowReturn(t *testing.T) {
	// 2025-06-01 .. 2026-03-01 daily, value = 1 + day index/100.
	start := ms(2025, 6, 1)
	c := dailyCurve(start, 274, func(i int) float64 { return 1 + float64(i)/100 })
	end := c[len(c)-1]
	at := func(t int64) float64 { return 1 + float64((t-start)/dayMs)/100 }
	cases := map[domain.Window]float64{
		domain.W1D:  end.V/at(end.T-dayMs) - 1,
		domain.W1W:  end.V/at(end.T-7*dayMs) - 1,
		domain.W1M:  end.V/at(time.UnixMilli(end.T).UTC().AddDate(0, -1, 0).UnixMilli()) - 1,
		domain.WYTD: end.V/at(ms(2026, 1, 1)) - 1,
		domain.W1Y:  end.V/c[0].V - 1, // longer than history -> since inception
		domain.WAll: end.V/c[0].V - 1,
	}
	for w, want := range cases {
		if got := WindowReturn(c, w); !near(got, want, 1e-12) {
			t.Errorf("WindowReturn(%s) = %v, want %v", w, got, want)
		}
	}
	if WindowReturn(c[:1], domain.WAll) != 0 {
		t.Error("single point has no return")
	}
}

func TestCorrelationBeta(t *testing.T) {
	start := ms(2026, 1, 1)
	r := []float64{0.01, -0.02, 0.015, 0.005, -0.01, 0.02, -0.005, 0.01}
	mk := func(k float64) domain.Series {
		v := 1.0
		return dailyCurve(start, len(r)+1, func(i int) float64 {
			if i > 0 {
				v *= 1 + k*r[i-1]
			}
			return v
		})
	}
	a, a2, neg := mk(1), mk(2), mk(-1)
	if got := Correlation(a, a2); !near(got, 1, 1e-9) {
		t.Fatalf("corr(a,2a) = %v", got)
	}
	if got := Correlation(a, neg); !near(got, -1, 1e-9) {
		t.Fatalf("corr(a,-a) = %v", got)
	}
	if got := Beta(a2, a); !near(got, 2, 1e-9) {
		t.Fatalf("beta(2a,a) = %v", got)
	}
	flat := dailyCurve(start, 9, func(int) float64 { return 1 })
	if Correlation(a, flat) != 0 || Beta(a, flat) != 0 {
		t.Fatal("flat benchmark must give 0")
	}
	other := dailyCurve(start+100*dayMs, 9, func(int) float64 { return 1 })
	if Correlation(a, other) != 0 {
		t.Fatal("non-overlapping series must give 0")
	}
}

func TestCapacity(t *testing.T) {
	cases := []struct {
		tvl, lf float64
		hlp     bool
		want    float64
	}{
		{1e6, 0.10, false, 1e6},
		{1e6, 0.04, false, 0},
		{1e6, 0.0495, false, 0}, // between 4.9% and 5%: clamp, never negative
		{2e6, 0.075, false, 1e6},
		{1e6, 0.10, true, -1},
		{0, 0.5, false, 0},
	}
	for _, c := range cases {
		if got := Capacity(c.tvl, c.lf, c.hlp); !near(got, c.want, 1e-6) {
			t.Errorf("Capacity(%v,%v,%v) = %v, want %v", c.tvl, c.lf, c.hlp, got, c.want)
		}
	}
}

func TestComputeTradeStats(t *testing.T) {
	m := Compute(nil, []float64{10, -5, 0, 20, -5})
	if m.WinRate != 0.5 || m.ProfitFactor != 3 {
		t.Fatalf("win %v pf %v", m.WinRate, m.ProfitFactor)
	}
	m = Compute(nil, []float64{1, 2})
	if m.WinRate != 1 || m.ProfitFactor != 0 {
		t.Fatalf("all winners: win %v pf %v (pf undefined -> 0)", m.WinRate, m.ProfitFactor)
	}
}

func TestComputeNoNaN(t *testing.T) {
	for _, c := range []domain.Series{nil, {{T: 1, V: 1}}, {{T: 1, V: 0}, {T: 2, V: 0}}, {{T: 1, V: 1}, {T: 1, V: 1}}} {
		m := Compute(c, nil)
		b, err := json.Marshal(Tearsheet(c, TearsheetOpts{}))
		if err != nil {
			t.Fatalf("Tearsheet(%v) not JSON-safe: %v (%+v)", c, err, m)
		}
		_ = b
	}
}

func TestClip(t *testing.T) {
	s := domain.Series{{T: 1, V: 10}, {T: 5, V: 20}, {T: 9, V: 30}, {T: 12, V: 40}}
	got := Clip(s, 3, 9)
	want := domain.Series{{T: 3, V: 10}, {T: 5, V: 20}, {T: 9, V: 30}}
	if len(got) != len(want) {
		t.Fatalf("Clip = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Clip = %+v, want %+v", got, want)
		}
	}
	if got := Clip(s, 5, 6); len(got) != 1 || got[0].T != 5 {
		t.Fatalf("exact start must not duplicate: %+v", got)
	}
}

func TestTearsheet(t *testing.T) {
	start := ms(2026, 1, 1)
	subj := dailyCurve(start, 60, func(i int) float64 { return 2 * (1 + 0.01*math.Sin(float64(i))) * (1 + float64(i)/200) })
	// BTC spans a wider range than the subject; HYPE none of it.
	btc := dailyCurve(start-30*dayMs, 120, func(i int) float64 { return 60000 + 100*float64(i%7) })
	hlpS := dailyCurve(start, 60, func(i int) float64 { return 1 + float64(i)/1000 })
	hype := dailyCurve(start+200*dayMs, 10, func(i int) float64 { return 40 })
	ts := Tearsheet(subj, TearsheetOpts{
		Title:      "Test",
		Benchmarks: map[string]domain.Series{"BTC": btc, "HYPE": hype, "HLP": hlpS, "AAA": hlpS},
		Now:        time.UnixMilli(start + 60*dayMs),
	})
	if ts.Title != "Test" || ts.Curve[0].V != 1 || len(ts.Curve) != 60 || len(ts.Drawdown) != 60 {
		t.Fatalf("curve/drawdown: %+v", ts.Curve[:2])
	}
	if ts.Meta.AsOf.IsZero() || len(ts.Monthly) != 3 {
		t.Fatalf("meta/monthly: %+v %+v", ts.Meta, ts.Monthly)
	}
	var names []string
	for _, b := range ts.Benchmarks {
		names = append(names, b.Name)
		if b.Curve[0].T != subj[0].T || b.Curve[len(b.Curve)-1].T > subj[len(subj)-1].T {
			t.Errorf("%s not clipped to subject range: %d..%d", b.Name, b.Curve[0].T, b.Curve[len(b.Curve)-1].T)
		}
		if b.Curve[0].V != 1 {
			t.Errorf("%s not normalized", b.Name)
		}
	}
	if got := len(names); got != 3 || names[0] != "HLP" || names[1] != "BTC" || names[2] != "AAA" {
		t.Fatalf("benchmarks = %v, want [HLP BTC AAA] (HYPE has no overlap)", names)
	}
	if ts.Benchmarks[0].Corr == 0 && ts.Benchmarks[1].Corr == 0 {
		t.Fatal("Corr not filled")
	}
}

// portfolioWindow loads one window of a Hyperliquid portfolio fixture.
func portfolioWindow(t *testing.T, window string) (av, pnl domain.Series) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, "hyperliquid", "portfolio_vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw [][2]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	conv := func(rows [][2]json.RawMessage) domain.Series {
		s := make(domain.Series, len(rows))
		for i, r := range rows {
			var ts int64
			var v string
			if json.Unmarshal(r[0], &ts) != nil || json.Unmarshal(r[1], &v) != nil {
				t.Fatalf("bad row %s", r)
			}
			f, _ := strconv.ParseFloat(v, 64)
			s[i] = domain.Point{T: ts, V: f}
		}
		return s
	}
	for _, w := range raw {
		var name string
		_ = json.Unmarshal(w[0], &name)
		if name != window {
			continue
		}
		var body struct {
			AV  [][2]json.RawMessage `json:"accountValueHistory"`
			PnL [][2]json.RawMessage `json:"pnlHistory"`
		}
		if err := json.Unmarshal(w[1], &body); err != nil {
			t.Fatal(err)
		}
		return conv(body.AV), conv(body.PnL)
	}
	t.Fatalf("window %s not in fixture", window)
	return nil, nil
}

func TestCurveFromPortfolioFixture(t *testing.T) {
	av, pnl := portfolioWindow(t, "day")
	c := CurveFromPortfolio(av, pnl)
	if len(c) != len(av) || c[0].V != 1 {
		t.Fatalf("curve = %+v", c)
	}
	// Hand-computed modified Dietz: r_i = ΔP / (A_{i-1} + F/2), F = ΔA - ΔP.
	want := 1.0
	for i := 1; i < len(av); i++ {
		dp := pnl[i].V - pnl[i-1].V
		f := (av[i].V - av[i-1].V) - dp
		want *= 1 + dp/(av[i-1].V+f/2)
	}
	if !near(c[len(c)-1].V, want, 1e-12) {
		t.Fatalf("final NAV %v, want %v", c[len(c)-1].V, want)
	}

	// Flow neutrality: inject a $1M deposit between points 1 and 2 (account
	// value jumps, PnL does not). Returns are measured on the larger base
	// afterwards, so compare against the hand-computed curve and check the
	// deposit period itself is flat apart from its PnL.
	dep := append(domain.Series(nil), av...)
	for i := 2; i < len(dep); i++ {
		dep[i].V += 1e6
	}
	c2 := CurveFromPortfolio(dep, pnl)
	dp := pnl[2].V - pnl[1].V
	f := (dep[2].V - dep[1].V) - dp
	if r := c2[2].V / c2[1].V; !near(r, 1+dp/(av[1].V+f/2), 1e-12) {
		t.Fatalf("deposit moved NAV: period return %v", r-1)
	}
	// A pure deposit with zero PnL leaves NAV exactly unchanged.
	flat := domain.Series{{T: 1, V: 100}, {T: 2, V: 1100}, {T: 3, V: 1100}}
	zero := domain.Series{{T: 1, V: 0}, {T: 2, V: 0}, {T: 3, V: 0}}
	for _, p := range CurveFromPortfolio(flat, zero) {
		if p.V != 1 {
			t.Fatalf("deposit with no PnL moved NAV: %+v", p)
		}
	}
}

func TestCurveFromPortfolioEmptyStart(t *testing.T) {
	// hlp.xyz snapshots start at av=0 before the first deposit.
	av := domain.Series{{T: 1, V: 0}, {T: 2, V: 1000}, {T: 3, V: 1100}}
	pnl := domain.Series{{T: 1, V: 0}, {T: 2, V: 5}, {T: 3, V: 105}}
	c := CurveFromPortfolio(av, pnl)
	if c[1].V != 1 || !near(c[2].V, 1.1, 1e-12) {
		t.Fatalf("curve = %+v", c)
	}
	if CurveFromPortfolio(nil, pnl) != nil {
		t.Fatal("no av -> nil")
	}
}

func checkByID(cs []domain.Check) map[string]domain.Check {
	m := map[string]domain.Check{}
	for _, c := range cs {
		m[c.ID] = c
	}
	return m
}

func TestChecklist(t *testing.T) {
	start := ms(2025, 1, 1)
	crash := dailyCurve(start, 200, func(i int) float64 {
		if i == 100 {
			return 0.4
		}
		return 1 + float64(i)/1000
	})
	followers := []domain.Follower{{User: "0xleader", Equity: 5000}}
	for i := 0; i < 12; i++ {
		followers = append(followers, domain.Follower{User: "0xf" + itoa(i), Equity: 9000})
	}
	d := domain.VaultDetail{
		Summary: domain.VaultSummary{
			Address: "0xabc", Leader: "0xLEADER", TVL: 120_000, AgeDays: 30, LeaderFraction: 0.07,
			MaxGrossLev: 7.5, AllowDeposits: false, Stale: true,
		},
		NAV:         crash,
		Followers:   followers,
		CapacityUSD: 0,
	}
	hlpM := &domain.Metrics{Sharpe: 3}
	cs := Checklist(d, hlpM)
	wantOrder := []string{CheckMaxDD, CheckAge, CheckTVL, CheckLeaderSkin, CheckCapacity, CheckConcentration, CheckLeverage, CheckDepositsOpen, CheckDataFresh, CheckSharpeVsHLP}
	if len(cs) != len(wantOrder) {
		t.Fatalf("got %d checks", len(cs))
	}
	for i, id := range wantOrder {
		if cs[i].ID != id || cs[i].Label == "" || cs[i].Detail == "" {
			t.Fatalf("check %d = %+v, want id %s", i, cs[i], id)
		}
	}
	got := checkByID(cs)
	want := map[string]domain.CheckStatus{
		CheckMaxDD:         domain.CheckFail, // 0.4 vs peak 1.099
		CheckAge:           domain.CheckWarn,
		CheckTVL:           domain.CheckPass,
		CheckLeaderSkin:    domain.CheckWarn,
		CheckCapacity:      domain.CheckNA,   // deposits closed: room is moot
		CheckConcentration: domain.CheckPass, // top 10 non-leader = 90k of 120k TVL = 75%
		CheckLeverage:      domain.CheckWarn,
		CheckDepositsOpen:  domain.CheckWarn,
		CheckDataFresh:     domain.CheckWarn,
		CheckSharpeVsHLP:   domain.CheckWarn,
	}
	for id, st := range want {
		if got[id].Status != st {
			t.Errorf("%s = %s (%s), want %s", id, got[id].Status, got[id].Detail, st)
		}
	}

	// HLP: protocol vault, unlimited capacity.
	h := domain.VaultDetail{Summary: domain.VaultSummary{Address: hlpAddress, TVL: 151e6, AgeDays: 900, AllowDeposits: true, MaxDD: -0.06, Sharpe: 7}, CapacityUSD: -1}
	got = checkByID(Checklist(h, hlpM))
	for id, st := range map[string]domain.CheckStatus{
		CheckLeaderSkin: domain.CheckNA, CheckCapacity: domain.CheckPass, CheckSharpeVsHLP: domain.CheckNA,
		CheckMaxDD: domain.CheckPass, CheckDepositsOpen: domain.CheckPass, CheckDataFresh: domain.CheckPass,
		CheckConcentration: domain.CheckNA, CheckLeverage: domain.CheckNA,
	} {
		if got[id].Status != st {
			t.Errorf("HLP %s = %s, want %s", id, got[id].Status, st)
		}
	}

	// Unknown inputs are n/a, closed vaults fail deposits_open.
	got = checkByID(Checklist(domain.VaultDetail{Summary: domain.VaultSummary{Closed: true}}, nil))
	for id, st := range map[string]domain.CheckStatus{
		CheckMaxDD: domain.CheckNA, CheckAge: domain.CheckNA, CheckLeaderSkin: domain.CheckNA,
		CheckCapacity: domain.CheckNA, CheckSharpeVsHLP: domain.CheckNA, CheckDepositsOpen: domain.CheckFail,
	} {
		if got[id].Status != st {
			t.Errorf("empty %s = %s, want %s", id, got[id].Status, st)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := map[float64]string{151318485.7: "$151.3M", 95000: "$95.0K", 12: "$12", 2.5e9: "$2.50B", -2000: "-$2.0K"}
	for v, want := range cases {
		if got := usd(v); got != want {
			t.Errorf("usd(%v) = %q, want %q", v, got, want)
		}
	}
	if pct(-0.3123) != "-31.2%" {
		t.Error(pct(-0.3123))
	}
}

func TestUnsortedInput(t *testing.T) {
	c := domain.Series{{T: ms(2026, 2, 10), V: 1.1}, {T: ms(2026, 1, 10), V: 1}, {T: ms(2026, 3, 10), V: 1.21}}
	m := Monthly(c)
	if len(m) != 3 || m[0].Month != 1 || !near(m[1].Ret, 0.1, 1e-12) || !near(m[2].Ret, 0.1, 1e-12) {
		t.Fatalf("Monthly(unsorted) = %+v", m)
	}
	if c[0].V != 1.1 {
		t.Fatal("input was mutated")
	}
	if got := WindowReturn(c, domain.WAll); !near(got, 0.21, 1e-12) {
		t.Fatalf("WindowReturn(unsorted) = %v", got)
	}
}

func TestSourceLabel(t *testing.T) {
	for in, want := range map[string]string{
		"hyperliquid-ws":     "Hyperliquid",
		"stats-data+hlp.xyz": "Hyperliquid + hlp.xyz",
		"hlpxyz-v6":          "hlp.xyz",
		"paper":              "paper",
	} {
		if got := SourceLabel(in); got != want {
			t.Errorf("SourceLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
