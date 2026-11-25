package tui

import (
	"math"
	"runtime"
	"strconv"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp/internal/agent"
	"github.com/maxmoneycash/hlp/internal/config"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/theme"
)

// Performance regression budgets. The
// tests assert them; the benchmarks report the numbers.
const (
	budgetAgentStream = 150 * time.Millisecond // 1,000 tokens, whole stream
	budgetChartUpdate = 150 * time.Microsecond // unchanged chart pane, 200x60
	budgetMidsTick    = 100 * time.Microsecond // 300 transcript entries
	budgetHeapMB      = 64                     // after 3,000 chart cards
)

func benchModel(tb testing.TB, w, h int) *Model {
	tb.Helper()
	f := &fakes{stream: &fakeStream{}}
	reg := fakeRegistry(tb, f)
	th, _ := theme.Get("hlp")
	env := &core.Env{Config: config.Defaults(), Theme: th, Profile: colorprofile.TrueColor, Now: time.Now}
	env.Config.Theme = "hlp"
	env.Paths.DataDir = tb.TempDir()
	env.Stream = f.stream
	m := New(Options{Env: env, Registry: reg})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	tb.Cleanup(m.Close)
	return m
}

func synthCandles(n int) []domain.Candle {
	cs := make([]domain.Candle, n)
	p := 100.0
	for i := range cs {
		d := math.Sin(float64(i)/7) * 1.5
		cs[i] = domain.Candle{T: int64(i) * 60000, O: p, H: p + 2 + math.Abs(d), L: p - 2 - math.Abs(d), C: p + d, V: float64(i%17) + 1}
		p = cs[i].C
	}
	return cs
}

func chartResult(n int) *core.Result {
	cs := synthCandles(n)
	return &core.Result{Card: core.CardChart, Title: "BTC 1m", Meta: domain.Meta{Source: "fixture", AsOf: time.Now()},
		Data: &core.ChartData{Coin: "BTC", Interval: domain.I1m, Candles: cs, Summary: core.ChartSummary{Last: cs[n-1].C}}}
}

// agentStream feeds 1,000 streamed tokens through Update, as the agent does.
func agentStream(m *Model, tokens int) {
	m.agentRun++
	run := m.agentRun
	m.agentText = -1
	for i := 0; i < tokens; i++ {
		txt := "word "
		if i%40 == 39 {
			txt = "**bold** and `code`.\n\n"
		}
		m.Update(agentEventMsg{run: run, ev: agent.Event{Kind: agent.EvText, Text: txt}})
	}
	m.Update(agentDoneMsg{run: run})
}

// chartPane opens a 5,000-candle chart pane at 200x60.
func chartPane(tb testing.TB) *Model {
	m := benchModel(tb, 200, 60)
	m.pushPane(newPane(chartResult(5000), 0, core.Tool{}, core.Call{}))
	m.Update(tea.FocusMsg{})
	return m
}

// midsModel has 300 transcript entries and the live watchlist feed.
func midsModel(tb testing.TB) (*Model, *liveSub, map[string]float64) {
	m := benchModel(tb, 80, 24)
	for i := 0; i < 300; i++ {
		m.add(&entry{kind: entryInfo, text: "line " + strconv.Itoa(i)})
	}
	sub := &liveSub{key: "side:mids", cancel: func() {}, ch: make(chan tea.Msg, 4)}
	m.live["side:mids"] = sub
	mids := make(map[string]float64, 250)
	for i := 0; i < 250; i++ {
		mids["C"+strconv.Itoa(i)] = float64(i)
	}
	m.Update(midsMsg{sub: sub, mids: mids})
	return m, sub, mids
}

func BenchmarkAgentStream1000(b *testing.B) {
	m := benchModel(b, 120, 40)
	b.ReportAllocs()
	for b.Loop() {
		m.entries = m.entries[:1]
		agentStream(m, 1000)
	}
}

func BenchmarkChartPaneUnchangedUpdate(b *testing.B) {
	m := chartPane(b)
	b.ReportAllocs()
	for b.Loop() {
		m.Update(wsStateMsg("live"))
		m.Update(wsStateMsg("reconnecting"))
	}
}

func BenchmarkMidsTick300(b *testing.B) {
	m, sub, mids := midsModel(b)
	b.ReportAllocs()
	i := 0.0
	for b.Loop() {
		i++
		mids["C1"] = i
		m.Update(midsMsg{sub: sub, mids: mids})
	}
}

// perfBudget runs f a few times and returns the best per-op time, which is
// what a budget on a loaded machine can fairly be compared against.
func perfBudget(t *testing.T, ops int, f func()) time.Duration {
	t.Helper()
	best := time.Duration(math.MaxInt64)
	for range 5 {
		start := time.Now()
		for range ops {
			f()
		}
		best = min(best, time.Since(start)/time.Duration(ops))
	}
	return best
}

func skipPerf(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budgets need a normal (non -race, non -short) build")
	}
}

// E-T1: 1,000 agent tokens take under 150 ms (was 3.1 s).
func TestPerfAgentStreamBudget(t *testing.T) {
	skipPerf(t)
	m := benchModel(t, 120, 40)
	got := perfBudget(t, 1, func() { m.entries = m.entries[:1]; agentStream(m, 1000) })
	t.Logf("1,000 tokens: %v", got)
	if got > budgetAgentStream {
		t.Fatalf("agent stream took %v, budget %v", got, budgetAgentStream)
	}
	// The final frame is the full markdown render, not the plain tail.
	e := m.entries[len(m.entries)-1]
	if e.kind != entryAssistant || e.streaming {
		t.Fatalf("stream not finished: %+v", e.kind)
	}
}

// E-T2: an Update that changes nothing in a 200x60 chart pane is cheap.
func TestPerfChartPaneBudget(t *testing.T) {
	skipPerf(t)
	m := chartPane(t)
	states := []tea.Msg{wsStateMsg("live"), wsStateMsg("reconnecting")}
	i := 0
	got := perfBudget(t, 200, func() { m.Update(states[i%2]); i++ })
	t.Logf("unchanged chart pane Update: %v", got)
	if got > budgetChartUpdate {
		t.Fatalf("chart pane Update took %v, budget %v", got, budgetChartUpdate)
	}
}

// E-T3: a mids tick with 300 transcript entries.
func TestPerfMidsTickBudget(t *testing.T) {
	skipPerf(t)
	m, sub, mids := midsModel(t)
	i := 0.0
	got := perfBudget(t, 500, func() { i++; mids["C1"] = i; m.Update(midsMsg{sub: sub, mids: mids}) })
	t.Logf("mids tick, 300 entries: %v", got)
	if got > budgetMidsTick {
		t.Fatalf("mids tick took %v, budget %v", got, budgetMidsTick)
	}
}

// E-T4: the transcript stays bounded: 3,000 chart cards of 5,000 candles
// each leave under 64 MB of heap.
func TestTranscriptHeapBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a few GB in total")
	}
	m := benchModel(t, 160, 50)
	for i := 0; i < 3000; i++ {
		e := m.add(&entry{kind: entryCard, result: chartResult(5000), call: core.Call{Tool: "get_candles"}})
		m.renderEntry(e, 160)
		if i%100 == 0 {
			m.Update(tea.FocusMsg{})
		}
	}
	m.Update(tea.FocusMsg{})
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	mb := ms.HeapAlloc >> 20
	t.Logf("heap after 3,000 chart cards: %d MB, %d entries", mb, len(m.entries))
	if len(m.entries) > maxEntries {
		t.Fatalf("%d entries kept", len(m.entries))
	}
	if mb > budgetHeapMB {
		t.Fatalf("heap %d MB, budget %d MB", mb, budgetHeapMB)
	}
}

func TestQuietMessagesSkipTheFrame(t *testing.T) {
	m := benchModel(t, 100, 30)
	m.Update(tea.FocusMsg{})
	m.entries = []*entry{{kind: entryInfo, text: "x"}} // no welcome strip on screen
	m.Update(tea.FocusMsg{})
	sub := &liveSub{key: "side:mids", cancel: func() {}, ch: make(chan tea.Msg, 4)}
	m.live["side:mids"] = sub
	before := &m.frame[0]
	m.Update(midsMsg{sub: sub, mids: map[string]float64{"ZZZ": 1}})
	if &m.frame[0] != before {
		t.Fatal("a price nobody shows re-rendered the frame")
	}
	m.live["side:mids"] = sub                                       // the reconcile above may drop the hand-made sub
	m.Update(midsMsg{sub: sub, mids: map[string]float64{"BTC": 2}}) // the ticker shows BTC
	if &m.frame[0] == before {
		t.Fatal("a shown price did not re-render")
	}
}

func TestStreamingReplyIsCleanAndFinal(t *testing.T) {
	m := benchModel(t, 100, 30)
	m.agentRun = 1
	m.agentText = -1
	m.Update(agentEventMsg{run: 1, ev: agent.Event{Kind: agent.EvText, Text: "hello \x1b]52;c;MHhkZWFk\x07**world**"}})
	e := m.entries[len(m.entries)-1]
	if !e.streaming {
		t.Fatal("not streaming")
	}
	for _, r := range e.text {
		if r == 0x1b || r == 0x07 {
			t.Fatalf("control kept in %q", e.text)
		}
	}
	m.Update(agentEventMsg{run: 1, ev: agent.Event{Kind: agent.EvToolStart, Tool: "get_candles"}})
	if e.streaming || e.cache != "" && e.mdLines != nil {
		t.Fatal("tool start did not finish the reply")
	}
}

func TestMergeCandleOwnedCopiesOnce(t *testing.T) {
	r := chartResult(100)
	orig := r.Data.(*core.ChartData)
	var owned *core.ChartData
	last := orig.Candles[99]
	c := last
	c.C += 1
	r1 := mergeCandleOwned(r, c, &owned)
	if orig.Candles[99].C != last.C {
		t.Fatal("first merge mutated the caller's candles")
	}
	d1 := r1.Data.(*core.ChartData)
	if owned != d1 {
		t.Fatal("first merge did not take ownership")
	}
	c.C += 1
	r2 := mergeCandleOwned(r1, c, &owned)
	if r2 == r1 || r2.Data.(*core.ChartData) != d1 || d1.Candles[99].C != c.C {
		t.Fatal("second merge did not update in place with a new Result")
	}
	nc := domain.Candle{T: c.T + 60000, C: 5}
	r3 := mergeCandleOwned(r2, nc, &owned)
	if d := r3.Data.(*core.ChartData); len(d.Candles) != 101 || d.Summary.Last != 5 {
		t.Fatalf("append: %d", len(d.Candles))
	}
	if len(orig.Candles) != 100 {
		t.Fatal("original grew")
	}
}

func TestSlimCardKeepsTextAndReopens(t *testing.T) {
	m := benchModel(t, 100, 30)
	first := m.add(&entry{kind: entryCard, result: chartResult(500), call: core.Call{Tool: "get_candles", Args: []byte(`{"coin":"BTC"}`)}})
	text := m.renderEntry(first, 100)
	for i := 0; i < slimAfter+1; i++ {
		m.add(&entry{kind: entryCard, result: chartResult(10)})
	}
	if !first.slim {
		t.Fatal("old card not slimmed")
	}
	if d, _ := asChart(first.result.Data); d == nil || d.Candles != nil {
		t.Fatal("candles kept")
	}
	first.cache = ""
	if got := m.renderEntry(first, 100); got != text {
		t.Fatal("slim card renders differently")
	}
	cmd := m.openEntry(first)
	if cmd == nil {
		t.Fatal("opening a slim card does not re-run its call")
	}
	collect(cmd, time.Second) // run the job so Close does not wait on it
}
