package backtest_test

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/pinecorpus"
	"github.com/maxmoneycash/hlp-cli/internal/strategy"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/pine"
)

// TestCorpusTradingViewMode grades the builtin engine in TradingView broker
// mode (backtest.ModeTradingView: the script's commission, no slippage,
// funding or exchange rules) against the corpus's TradingView tapes
// (docs/PINEFORGE.md). It is opt-in and never part of the default
// `go test ./...`:
//
//	HLP_LIVE=1 HLP_PINECORPUS_DIR=/path/to/corpus go test ./internal/strategy/backtest -run CorpusTradingView -v
//
// HLP_CORPUS_N bounds the number of plain probes (default 30). The
// scoreboard is logged; the test fails only when no probe could run.
func TestCorpusTradingViewMode(t *testing.T) {
	if os.Getenv("HLP_LIVE") != "1" || pinecorpus.Dir() == "" {
		t.Skip("set HLP_LIVE=1 and HLP_PINECORPUS_DIR to grade against the corpus")
	}
	root := pinecorpus.Dir()
	probes, err := pinecorpus.Probes(root)
	if err != nil {
		t.Fatal(err)
	}
	n := 30
	if v, err := strconv.Atoi(os.Getenv("HLP_CORPUS_N")); err == nil && v > 0 {
		n = v
	}
	bars, err := pinecorpus.ChartFeed(root, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cs := pinecorpus.Candles(bars)
	var jobs []pinecorpus.Job
	for _, p := range probes {
		if len(jobs) >= n {
			break
		}
		if !p.Plain() || p.Anomaly() {
			continue
		}
		src, err := p.Pine()
		if err != nil {
			continue
		}
		jobs = append(jobs, pinecorpus.Job{Probe: p, Pine: src, Candles: cs, Interval: domain.I15m})
	}
	rows, err := pinecorpus.Score(context.Background(), jobs, []pinecorpus.Engine{tvEngine{}}, pinecorpus.GradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + pinecorpus.Markdown(rows))
	ran := 0
	for _, r := range rows {
		if r.Err == "" {
			ran++
		}
	}
	if ran == 0 && len(rows) > 0 {
		t.Fatalf("no probe ran in TradingView mode")
	}
}

type tvEngine struct{}

func (tvEngine) Name() string { return "builtin (tradingview mode)" }

var commissionRE = regexp.MustCompile(`commission_value\s*=\s*([0-9.]+)`)

func (tvEngine) Run(ctx context.Context, job pinecorpus.Job) (*pinecorpus.EngineRun, error) {
	s, err := pine.Parse(job.Pine)
	if err != nil {
		return nil, err
	}
	st, err := pine.Compile(s, "ETH", job.Interval, nil)
	if err != nil {
		return nil, err
	}
	var costs strategy.Costs
	if m := commissionRE.FindStringSubmatch(job.Pine); m != nil && regexp.MustCompile(`commission\.percent`).MatchString(job.Pine) {
		v, _ := strconv.ParseFloat(m[1], 64)
		costs = strategy.Costs{TakerBps: v * 100, MakerBps: v * 100}
	}
	last := job.Candles[len(job.Candles)-1].T + job.Interval.Duration().Milliseconds()
	res, err := backtest.Run(ctx, st, corpusFeed(job.Candles), backtest.Options{Capital: s.InitialCapital, Costs: costs,
		Mode: backtest.ModeTradingView, MaxLogs: 1, Now: func() time.Time { return time.UnixMilli(last + 1) }})
	if err != nil {
		return nil, err
	}
	return &pinecorpus.EngineRun{Trades: pinecorpus.FillsToTrades(res.Fills)}, nil
}

type corpusFeed []domain.Candle

func (f corpusFeed) Candles(_ context.Context, _ string, _ domain.Interval, _ int, _ int64) ([]domain.Candle, error) {
	return f, nil
}

func (corpusFeed) Funding(context.Context, string, int64, int64) ([]domain.FundingPoint, error) {
	return nil, nil
}
