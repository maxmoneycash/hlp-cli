package pineforge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

const v6 = `//@version=6
strategy("Cross", overlay=true)
fastLen = input.int(9, "Fast Length")
slowLen = input.int(21, title="Slow Length")
if ta.crossover(ta.ema(close, fastLen), ta.ema(close, slowLen))
    strategy.entry("L", strategy.long)
`

func candles(n int) []domain.Candle {
	out := make([]domain.Candle, n)
	for i := range out {
		px := 100 + float64(i)
		out[i] = domain.Candle{T: 1735689600000 + int64(i)*900_000, O: px, H: px + 1, L: px - 1, C: px + 0.5, V: 10}
	}
	return out
}

// standIn writes a fake container CLI that logs its argv (one per line,
// calls separated by "--") to log, then runs body.
func standIn(t *testing.T, body string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "argv.log")
	bin = fakeDocker(t, `for a in "$@"; do echo "$a" >> "`+log+`"; done; echo -- >> "`+log+`"
`+body)
	return bin, log
}

func readLog(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestDetect(t *testing.T) {
	bin, log := standIn(t, `[ "$1" = image ] && { echo sha256:abc; exit 0; }; exit 1`)
	t.Setenv(EnvDocker, bin)
	t.Setenv(EnvImage, "")
	e, err := Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if e.ImageID != "sha256:abc" || e.Image != DefaultImage || e.Version != "1.12.0" || e.Docker != bin {
		t.Fatalf("engine = %+v", e)
	}
	if got := readLog(t, log); !strings.Contains(got, "image\ninspect\n--format\n{{.Id}}\n"+DefaultImage) {
		t.Fatalf("argv = %q", got)
	}
	if !Available(context.Background()) {
		t.Fatal("Available = false")
	}
}

func TestDetectImageMissing(t *testing.T) {
	bin, _ := standIn(t, `echo "Error: No such image: x" >&2; exit 1`)
	t.Setenv(EnvDocker, bin)
	t.Setenv(EnvImage, "local/pf:dev")
	_, err := Detect(context.Background())
	if !errors.Is(err, ErrImageMissing) || !strings.Contains(err.Error(), "pull local/pf:dev") {
		t.Fatalf("err = %v", err)
	}
	bin, _ = standIn(t, `echo "Cannot connect to the Docker daemon" >&2; exit 1`)
	t.Setenv(EnvDocker, bin)
	if _, err := Detect(context.Background()); err == nil || errors.Is(err, ErrImageMissing) || !strings.Contains(err.Error(), "Cannot connect") {
		t.Fatalf("daemon err = %v", err)
	}
}

func TestDetectNotInstalled(t *testing.T) {
	t.Setenv(EnvDocker, "hlp-no-such-docker-binary")
	if _, err := Detect(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}

func TestExecuteGates(t *testing.T) {
	bin, log := standIn(t, `exit 0`)
	e := &Engine{Docker: bin, Image: "img"}
	if _, err := e.Execute(context.Background(), v6, candles(3), Options{}); !errors.Is(err, ErrLicenseNotAccepted) {
		t.Fatalf("err = %v", err)
	}
	e.LicenseAccepted = true
	if _, err := e.Execute(context.Background(), v6, candles(3), Options{Vault: true}); !errors.Is(err, ErrCommercialUse) {
		t.Fatalf("vault err = %v", err)
	}
	if _, err := e.Execute(context.Background(), "//@version=5\nstrategy(\"x\")\n", candles(3), Options{}); err == nil || !strings.Contains(err.Error(), "v6") {
		t.Fatalf("v5 err = %v", err)
	}
	bad := candles(3)
	bad[2].T = bad[1].T
	if _, err := e.Execute(context.Background(), v6, bad, Options{}); err == nil {
		t.Fatal("unordered candles accepted")
	}
	if readLog(t, log) != "" {
		t.Fatal("container started despite a refused request")
	}
}

func TestExecute(t *testing.T) {
	fixture, _ := filepath.Abs("testdata/report_synthetic.json")
	bin, log := standIn(t, `
for a in "$@"; do case "$a" in *:/in/ohlcv.csv:ro) f="${a%%:/in/ohlcv.csv:ro}";; esac; done
dirname "$f" > "$(dirname "$0")/mountdir"
[ "$(wc -l < "$f" | tr -d ' ')" = 4 ] || { echo "bad bar count" >&2; exit 2; }
cat "`+fixture+`"`)
	e := &Engine{Docker: bin, Image: "img:1.2.3", LicenseAccepted: true}
	r, err := e.Execute(context.Background(), v6, candles(3), Options{
		Inputs:   map[string]any{"fastlen": 5, "Slow Length": 30.0, "nope": true},
		Interval: domain.I15m,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Trades) != 2 || !r.Trades[0].Long() || r.Trades[1].Long() || !r.Trades[1].OpenAtEnd {
		t.Fatalf("trades = %+v", r.Trades)
	}
	if r.Stats.ClosedTrades != 1 || r.Stats.NetProfit != 3 || r.Stats.InitialCapital != 1000 || r.Stats.FinalEquity != 1001.5 || r.Bars != 4 {
		t.Fatalf("stats = %+v bars=%d", r.Stats, r.Bars)
	}
	if len(r.Equity) != 2 || r.Equity[1].T != 1735700400000 {
		t.Fatalf("equity = %+v", r.Equity)
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], `"nope"`) {
		t.Fatalf("warnings = %v", r.Warnings)
	}
	argv := readLog(t, log)
	for _, want := range []string{"run\n--name\nhlp-pineforge-", "--pull=never", "--network=none", "PINEFORGE_INPUT_TF=15",
		`"Fast Length":"5"`, `"Slow Length":"30"`, "\nimg:1.2.3\n"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q:\n%s", want, argv)
		}
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(bin), "mountdir"))
	if err != nil {
		t.Fatal(err)
	}
	if dir := strings.TrimSpace(string(b)); !strings.Contains(dir, "hlp-pineforge-") {
		t.Fatalf("mount dir = %q", dir)
	} else if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %s not removed (stat err %v)", dir, err)
	}
}

func TestExecuteTimeoutRemovesContainer(t *testing.T) {
	bin, log := standIn(t, `[ "$1" = rm ] && exit 0; exec sleep 30`)
	// The first exec of a fresh script can stall on macOS while the system
	// scans it, longer than the run timeout below; warm it up so the run's
	// argv reaches the log before the timeout kills it.
	_ = exec.Command(bin, "rm", "warm").Run()
	_ = os.Remove(log)
	e := &Engine{Docker: bin, Image: "img", LicenseAccepted: true, Timeout: 300 * time.Millisecond}
	start := time.Now()
	_, err := e.Execute(context.Background(), v6, candles(3), Options{})
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v", err)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("took %s", el)
	}
	argv := readLog(t, log)
	i := strings.Index(argv, "--name\n")
	if i < 0 {
		t.Fatalf("no --name in %q", argv)
	}
	name := strings.SplitN(argv[i+len("--name\n"):], "\n", 2)[0]
	if !strings.Contains(argv, "rm\n-f\n"+name+"\n") {
		t.Fatalf("container %s not removed:\n%s", name, argv)
	}
}

func TestExecuteOutputCap(t *testing.T) {
	bin, _ := standIn(t, `[ "$1" = rm ] && exit 0; head -c 100000 /dev/zero`)
	e := &Engine{Docker: bin, Image: "img", LicenseAccepted: true, MaxOutput: 1000}
	if _, err := e.Execute(context.Background(), v6, candles(3), Options{}); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestExecuteContainerFailure(t *testing.T) {
	bin, _ := standIn(t, `echo "Unable to find image 'img' locally" >&2; exit 125`)
	e := &Engine{Docker: bin, Image: "img", LicenseAccepted: true}
	_, err := e.Execute(context.Background(), v6, candles(3), Options{})
	var pe *Error
	if !errors.As(err, &pe) || pe.Kind != "container" || !strings.Contains(pe.Msg, "Unable to find image") {
		t.Fatalf("err = %#v", err)
	}
}

func TestBacktest(t *testing.T) {
	fixture, _ := filepath.Abs("testdata/report_synthetic.json")
	bin, log := standIn(t, `cat "`+fixture+`"`)
	e := &Engine{Docker: bin, Image: "img:1.12.0", Version: "1.12.0", LicenseAccepted: true}
	res, r, err := e.Backtest(context.Background(), v6, "ETH", domain.I15m, candles(4), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || res.Spec.Name != "Cross" || len(res.Equity) != 2 {
		t.Fatalf("res = %+v", res)
	}
	// Closed long: open + close fills; open short: opening fill only.
	if len(res.Fills) != 3 || res.Fills[1].ClosedPnL != 3 || res.Fills[2].Dir != "Open Short" {
		t.Fatalf("fills = %+v", res.Fills)
	}
	if !strings.Contains(res.Warnings[0], "PineForge 1.12.0") || res.Tearsheet.Meta.Source != "backtest (pineforge)" {
		t.Fatalf("warnings = %v source=%q", res.Warnings, res.Tearsheet.Meta.Source)
	}
	if !strings.Contains(readLog(t, log), "PINEFORGE_INPUT_TF=15") {
		t.Fatal("interval not passed through")
	}
}

func TestTagAndTF(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/a/b:1.12.0": "1.12.0", "localhost:5000/pf": "", "pf": "", "pf:dev@sha256:00": "dev",
	} {
		if got := tagOf(in); got != want {
			t.Errorf("tagOf(%q) = %q, want %q", in, got, want)
		}
	}
	for iv, want := range map[domain.Interval]string{domain.I15m: "15", domain.I4h: "240", domain.I1d: "D", "": ""} {
		if got := tf(iv); got != want {
			t.Errorf("tf(%q) = %q, want %q", iv, got, want)
		}
	}
}

// TestLivePineforge runs the real release image. It needs Docker, the image
// pulled by the user (docker pull $HLP_PINEFORGE_IMAGE or DefaultImage) and
// HLP_LIVE=1 plus HLP_PINEFORGE=1, which also records that whoever runs it
// accepted LicenseNotice for this noncommercial test.
func TestLivePineforge(t *testing.T) {
	if os.Getenv("HLP_LIVE") != "1" || os.Getenv("HLP_PINEFORGE") != "1" {
		t.Skip("set HLP_LIVE=1 and HLP_PINEFORGE=1 (needs Docker and the pineforge-release image; see docs/PINEFORGE.md)")
	}
	e, err := Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e.LicenseAccepted = true
	cs := make([]domain.Candle, 2000)
	for i := range cs {
		// A slow sine so the EMA cross fires a few dozen times.
		px := 100 + 10*float64((i/50)%2*2-1)*float64(i%50)/50
		cs[i] = domain.Candle{T: 1735689600000 + int64(i)*900_000, O: px, H: px + 0.5, L: px - 0.5, C: px + 0.1, V: 1}
	}
	src := v6 + `if ta.crossunder(ta.ema(close, fastLen), ta.ema(close, slowLen))
    strategy.close("L")
`
	r, err := e.Execute(context.Background(), src, cs, Options{Interval: domain.I15m})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("image %s: %d bars, %d trades, net %.2f in %s", e.Image, r.Bars, len(r.Trades), r.Stats.NetProfit, r.Elapsed)
	if r.Bars != len(cs) || len(r.Trades) == 0 {
		t.Fatalf("bars=%d trades=%d", r.Bars, len(r.Trades))
	}
}

func TestResolveInputsTitles(t *testing.T) {
	src := `//@version=6
strategy("x")
tf = input.timeframe("D", "TF")
gen = input("x", "Generic")
fast = input.int(9, title = "Fast")
`
	out, unknown := resolveInputs(src, map[string]any{"tf": "240", "gen": "y", "FAST": float64(5)})
	if len(unknown) != 0 || out["TF"] != "240" || out["Generic"] != "y" || out["Fast"] != "5" {
		t.Fatalf("out %v unknown %v", out, unknown)
	}
	if _, ok := out["D"]; ok {
		t.Fatal("default value used as the title")
	}
}

// Every exported way to run PineForge goes through the licence gate.
func TestEntryPointsNeedLicence(t *testing.T) {
	bin, _ := standIn(t, `exit 0`)
	e := &Engine{Docker: bin, Image: "img"}
	if _, err := e.Execute(context.Background(), v6, candles(3), Options{}); !errors.Is(err, ErrLicenseNotAccepted) {
		t.Errorf("Execute: %v", err)
	}
	if _, _, err := e.Backtest(context.Background(), v6, "ETH", domain.I15m, candles(3), Options{}); !errors.Is(err, ErrLicenseNotAccepted) {
		t.Errorf("Backtest: %v", err)
	}
	e.LicenseAccepted = true
	if _, _, err := e.Backtest(context.Background(), v6, "ETH", domain.I15m, candles(3), Options{Vault: true}); !errors.Is(err, ErrCommercialUse) {
		t.Errorf("Backtest vault: %v", err)
	}
}

// PineForge's time_ms is the bar open; Backtest stamps equity at bar close
// like the builtin engine and pyne.Backtest.
func TestBacktestEquityAtBarClose(t *testing.T) {
	fixture, _ := filepath.Abs("testdata/report_synthetic.json")
	bin, _ := standIn(t, `cat "`+fixture+`"`)
	e := &Engine{Docker: bin, Image: "img:1.12.0", LicenseAccepted: true}
	cs := candles(4) // the fixture's bars are hourly from the same start
	for i := range cs {
		cs[i].T = 1735689600000 + int64(i)*3_600_000
	}
	res, _, err := e.Backtest(context.Background(), v6, "ETH", domain.I1h, cs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	last := cs[len(cs)-1]
	if got := res.Equity[len(res.Equity)-1].T; got != last.T+3_600_000 {
		t.Fatalf("last equity T %d, want last bar close %d", got, last.T+3_600_000)
	}
	found := false
	for _, w := range res.Warnings {
		found = found || strings.Contains(w, "funding")
	}
	if !found {
		t.Errorf("warnings lack the not-applied note: %v", res.Warnings)
	}
}
