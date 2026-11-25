package paper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/sim"
)

// Feed provides live data. core adapts ws.Stream + hyperliquid.Reader into it.
type Feed interface {
	strategy.DataSource
	// Bars delivers each CLOSED bar for coin/interval (from the WS candle
	// stream: a bar is closed when a newer open time arrives).
	Bars(ctx context.Context, coin string, iv domain.Interval) (<-chan domain.Candle, error)
	Book(ctx context.Context, coin string) (domain.Book, error)
}

// Options for a session.
type Options struct {
	ID      string // default: strategy name + short random suffix
	Capital float64
	Costs   strategy.Costs
	DataDir string

	// Now is the wall clock (default time.Now); tests pin it.
	Now func() time.Time
	// History is how many closed bars to load before going live (default
	// max(Spec.Warmup+50, 300), capped at 5000).
	History int
	// Grace is how long a multi-coin session waits for every coin's bar of
	// the same open time before deciding anyway (default 5s).
	Grace time.Duration
	// NoInitialDecision skips the decision on the latest closed bar at
	// start; by default a new session trades right away instead of
	// waiting up to a full bar.
	NoInitialDecision bool
	// Markets are the exchange rules per coin (lot size, $10 minimum,
	// max leverage, liquidation); see sim.Rules. Nil = the plain broker.
	Markets map[string]sim.Rules
	// Watch adds read-only entrants to an arena (real vaults head to head).
	Watch []Watcher
	// WatchEvery is the polling period for watchers (default 60s, min 15s).
	WatchEvery time.Duration
}

// Watcher is a read-only arena entrant whose value comes from outside the
// simulator, e.g. a real vault's share price from the Reader. Value should
// be flow-neutral (a share price or NAV), so deposits do not count as
// performance.
type Watcher struct {
	Name  string
	Kind  string // default "vault"
	Value func(ctx context.Context) (float64, error)
}

// Snapshot is the current session state.
type Snapshot struct {
	ID       string
	Strategy string
	Started  int64
	Equity   domain.Series
	Account  domain.Account
	Fills    []domain.Fill
	Logs     []string
	Running  bool
	Err      string

	Kind     string          `json:",omitempty"` // pine | thesis | llm | rules | vault
	Capital  float64         `json:",omitempty"`
	Return   float64         `json:",omitempty"` // equity / capital - 1
	Note     string          `json:",omitempty"` // latest decision note ("why I bought")
	Universe []string        `json:",omitempty"`
	Interval domain.Interval `json:",omitempty"`
	Updated  int64           `json:",omitempty"`
}

// Session is a running paper strategy.
type Session interface {
	Snapshot() Snapshot
	Updates() <-chan Snapshot // coalesced, at most ~4/s
	Stop() error
}

const (
	maxLogs      = 500
	maxFills     = 10000
	persistEvery = 60 * time.Second
	publishEvery = 250 * time.Millisecond
	stateVersion = 1
)

// stateFile is the on-disk session format.
type stateFile struct {
	Version  int                `json:"version"`
	Snapshot Snapshot           `json:"snapshot"`
	Ledger   sim.LedgerState    `json:"ledger"`
	Pending  []sim.Resting      `json:"pending,omitempty"`
	LastBar  map[string]int64   `json:"lastBar,omitempty"`
	LastFund map[string]int64   `json:"lastFunding,omitempty"`
	Prices   map[string]float64 `json:"prices,omitempty"`
}

// live tracks sessions running in this process (by state path) so List can
// tell a running session from a stale file.
var live = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

type session struct {
	strat strategy.Strategy
	spec  strategy.Spec
	feed  Feed
	o     Options
	path  string

	eng      *sim.Engine
	lastBar  map[string]int64
	lastFund map[string]int64
	decided  int64 // open time of the last decided bar

	mu      sync.Mutex
	snap    Snapshot
	dirty   bool
	updates chan Snapshot

	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
	stopErr  error
}

// Start warms the strategy with history from feed, then runs on live bars
// until Stop is called or ctx ends.
//
// If DataDir holds a session with the same ID, its ledger, open orders,
// equity curve, fills and logs are restored and the strategy resumes
// (Pine replays history silently, thesis re-targets on its next boundary).
func Start(ctx context.Context, s strategy.Strategy, feed Feed, o Options) (Session, error) {
	if s == nil || feed == nil {
		return nil, errors.New("paper: nil strategy or feed")
	}
	spec := s.Spec()
	if len(spec.Universe) == 0 {
		return nil, errors.New("paper: strategy has an empty universe")
	}
	if spec.Interval == "" {
		spec.Interval = domain.I1h
	}
	if !spec.Interval.Valid() {
		return nil, fmt.Errorf("paper: invalid interval %q", spec.Interval)
	}
	o = withDefaults(o, spec)
	if o.ID == "" {
		o.ID = slug(spec.Name) + "-" + randHex(3)
	}
	if !validID(o.ID) {
		return nil, fmt.Errorf("paper: invalid session id %q (letters, digits, - and _ only)", o.ID)
	}
	ss := &session{strat: s, spec: spec, feed: feed, o: o, lastBar: map[string]int64{}, lastFund: map[string]int64{},
		updates: make(chan Snapshot, 1), done: make(chan struct{})}
	if o.DataDir != "" {
		ss.path = filepath.Join(o.DataDir, "paper", o.ID+".json")
		live.Lock()
		if live.m[ss.path] {
			live.Unlock()
			return nil, fmt.Errorf("paper: session %s is already running", o.ID)
		}
		live.m[ss.path] = true
		live.Unlock()
	}
	ok := false
	defer func() {
		if !ok && ss.path != "" {
			live.Lock()
			delete(live.m, ss.path)
			live.Unlock()
		}
	}()

	resumed, err := ss.restore()
	if err != nil {
		return nil, err
	}
	if !resumed {
		ss.eng = sim.NewEngine(o.Capital, o.Costs, spec.Universe)
		ss.snap = Snapshot{ID: o.ID, Strategy: spec.Name, Started: o.Now().UnixMilli(), Kind: spec.Kind,
			Capital: o.Capital, Universe: spec.Universe, Interval: spec.Interval}
	}
	ss.eng.MaxBars = max(o.History, 500)
	if len(o.Markets) > 0 {
		ss.eng.Rules = o.Markets
	}
	ss.hook()

	// Warm with history.
	dur := spec.Interval.Duration().Milliseconds()
	now := o.Now().UnixMilli()
	var lastT int64
	for _, coin := range spec.Universe {
		cs, err := feed.Candles(ctx, coin, spec.Interval, o.History, 0)
		if err != nil {
			return nil, fmt.Errorf("paper: history %s %s: %w", coin, spec.Interval, err)
		}
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].T < cs[j].T })
		for _, c := range cs {
			if c.T+dur > now { // still forming
				continue
			}
			ss.eng.AddBar(coin, c)
			if c.T > lastT {
				lastT = c.T
			}
		}
	}
	if lastT == 0 {
		return nil, fmt.Errorf("paper: no closed %s history for %s", spec.Interval, strings.Join(spec.Universe, ", "))
	}
	ss.eng.SetNow(lastT)
	if err := s.Init(ss.eng); err != nil {
		return nil, fmt.Errorf("paper: init: %w", err)
	}
	if resumed {
		ss.logf("resumed session (equity $%.2f)", ss.eng.Equity())
	}

	rctx, cancel := context.WithCancel(ctx)
	ss.cancel = cancel
	ss.eng.SetStdContext(rctx)
	// Subscribe before the first decision so no bar is missed.
	type barMsg struct {
		coin string
		c    domain.Candle
	}
	merged := make(chan barMsg, 64)
	var wg sync.WaitGroup
	for _, coin := range spec.Universe {
		ch, err := feed.Bars(rctx, coin, spec.Interval)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("paper: subscribe %s: %w", coin, err)
		}
		wg.Add(1)
		go func(coin string, ch <-chan domain.Candle) {
			defer wg.Done()
			for {
				select {
				case <-rctx.Done():
					return
				case c, ok := <-ch:
					if !ok {
						return
					}
					select {
					case merged <- barMsg{coin, c}:
					case <-rctx.Done():
						return
					}
				}
			}
		}(coin, ch)
	}
	ss.snap.Running = true
	if !o.NoInitialDecision && (!resumed || ss.lastBar[spec.Universe[0]] < lastT) {
		ss.decide(rctx, lastT)
	} else {
		ss.refresh()
	}
	ss.persist()
	ok = true

	// Run loop.
	go func() {
		defer close(ss.done)
		defer ss.finish()
		defer wg.Wait()
		persistT := time.NewTicker(persistEvery)
		defer persistT.Stop()
		pubT := time.NewTicker(publishEvery)
		defer pubT.Stop()
		var grace *time.Timer
		var graceC <-chan time.Time
		pending := map[string]domain.Candle{}
		var pendingT int64
		flush := func() {
			if len(pending) == 0 {
				return
			}
			for coin, c := range pending {
				ss.closeBar(coin, c)
			}
			clear(pending)
			if pendingT > ss.decided {
				ss.decide(rctx, pendingT)
			} else {
				ss.refresh() // a straggler for a bar already decided
			}
			if grace != nil {
				grace.Stop()
				graceC = nil
			}
		}
		for {
			select {
			case <-rctx.Done():
				return
			case m := <-merged:
				if m.c.T <= ss.lastBar[m.coin] {
					continue // replayed or duplicate
				}
				if len(pending) > 0 && m.c.T != pendingT {
					flush()
				}
				pending[m.coin] = m.c
				pendingT = m.c.T
				if len(pending) == len(spec.Universe) {
					flush()
				} else if graceC == nil {
					grace = time.NewTimer(o.Grace)
					graceC = grace.C
				}
			case <-graceC:
				graceC = nil
				flush()
			case <-persistT.C:
				ss.persist()
			case <-pubT.C:
				ss.publish()
			}
		}
	}()
	return ss, nil
}

func withDefaults(o Options, spec strategy.Spec) Options {
	if o.Capital <= 0 {
		o.Capital = 10000
	}
	if o.Costs == (strategy.Costs{}) {
		o.Costs = strategy.DefaultCosts
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.History <= 0 {
		o.History = max(spec.Warmup+50, 300)
	}
	o.History = min(o.History, 5000)
	if o.Grace <= 0 {
		o.Grace = 5 * time.Second
	}
	if o.WatchEvery <= 0 {
		o.WatchEvery = 60 * time.Second
	}
	if o.WatchEvery < 15*time.Second {
		o.WatchEvery = 15 * time.Second
	}
	return o
}

// hook wires engine callbacks into the snapshot.
func (ss *session) hook() {
	ss.eng.OnLog = func(msg string) { ss.logf("%s", msg) }
	ss.eng.OnFill = func(f domain.Fill, tag string) {
		ss.mu.Lock()
		ss.snap.Fills = append(ss.snap.Fills, f)
		if len(ss.snap.Fills) > maxFills {
			ss.snap.Fills = ss.snap.Fills[len(ss.snap.Fills)-maxFills:]
		}
		ss.dirty = true
		ss.mu.Unlock()
	}
	ss.eng.OnMark = func(coin string, buy bool, label string) {
		side := "sell"
		if buy {
			side = "buy"
		}
		if label != "" {
			ss.logf("signal %s %s: %s", side, coin, label)
		}
	}
}

func (ss *session) logf(format string, args ...any) {
	t := time.UnixMilli(ss.o.Now().UnixMilli()).UTC().Format("2006-01-02 15:04")
	ss.mu.Lock()
	ss.snap.Logs = append(ss.snap.Logs, t+" "+fmt.Sprintf(format, args...))
	if len(ss.snap.Logs) > maxLogs {
		ss.snap.Logs = ss.snap.Logs[len(ss.snap.Logs)-maxLogs:]
	}
	ss.dirty = true
	ss.mu.Unlock()
}

// closeBar matches resting orders against a newly closed bar, then makes
// the bar visible. Runs on the session goroutine.
func (ss *session) closeBar(coin string, c domain.Candle) {
	ss.eng.MatchResting(coin, c)
	ss.eng.CheckLiquidation(coin, c)
	ss.eng.AddBar(coin, c)
	ss.lastBar[coin] = c.T
}

// decide runs one decision on the bar that opened at t: funding, OnBar,
// fills against the live book, equity mark. Runs on the session goroutine.
func (ss *session) decide(ctx context.Context, t int64) {
	dur := ss.spec.Interval.Duration().Milliseconds()
	closeT := t + dur
	ss.decided = t
	for _, coin := range ss.spec.Universe {
		if lb, ok := ss.eng.LastBar(coin); ok && lb.T == t {
			ss.lastBar[coin] = t
		}
	}
	if ss.o.Costs.Funding {
		ss.applyFunding(ctx, closeT)
	}
	ss.eng.SetNow(t)
	if !ss.eng.CheckWipeout(closeT) {
		if err := ss.strat.OnBar(ss.eng); err != nil {
			ss.logf("strategy error: %v", err)
			ss.mu.Lock()
			ss.snap.Err = err.Error()
			ss.mu.Unlock()
		}
	}
	fillT := ss.o.Now().UnixMilli()
	nf := 0
	for _, coin := range ss.spec.Universe {
		if !ss.hasMarket(coin) {
			continue
		}
		px := ss.eng.Price(coin)
		b, err := ss.feed.Book(ctx, coin)
		if err != nil || (len(b.Bids) == 0 && len(b.Asks) == 0) {
			nf += len(ss.eng.FillMarketAt(coin, px, fillT))
			continue
		}
		nf += len(ss.eng.FillMarketBook(coin, b, px, fillT))
	}
	ss.eng.CheckWipeout(fillT)
	ss.mu.Lock()
	ss.snap.Equity = append(ss.snap.Equity, domain.Point{T: closeT, V: ss.eng.Equity()})
	ss.mu.Unlock()
	ss.refresh()
	if nf > 0 {
		ss.persist()
	}
}

func (ss *session) hasMarket(coin string) bool {
	for _, r := range ss.eng.Pending() {
		if r.Coin == coin && r.Kind == strategy.Market {
			return true
		}
	}
	return false
}

// applyFunding charges hourly funding that settled since the last check.
// It only asks the feed once per hour boundary.
func (ss *session) applyFunding(ctx context.Context, upTo int64) {
	const hour = int64(time.Hour / time.Millisecond)
	for _, coin := range ss.spec.Universe {
		from := ss.lastFund[coin]
		if from == 0 {
			from = upTo - hour
		}
		if upTo/hour == from/hour {
			continue
		}
		fs, err := ss.feed.Funding(ctx, coin, from+1, upTo)
		if err != nil {
			continue // retried on the next boundary
		}
		sort.SliceStable(fs, func(i, j int) bool { return fs[i].Time < fs[j].Time })
		for _, f := range fs {
			if f.Time <= from || f.Time > upTo {
				continue
			}
			if ss.lastFund[coin] != 0 { // never charge for time before the session existed
				ss.eng.ApplyFunding(coin, f.Rate, ss.eng.Price(coin))
			}
			ss.eng.SetFunding(coin, f.Rate)
		}
		ss.lastFund[coin] = upTo
	}
}

// refresh rebuilds the derived snapshot fields from the engine.
func (ss *session) refresh() {
	acct := ss.eng.L.Account("paper:"+ss.o.ID, ss.eng.Price, ss.o.Now().UnixMilli())
	note := ""
	if n, ok := ss.strat.(strategy.Noter); ok {
		note = n.LastNote()
	}
	failure, fails := "", false
	if f, ok := ss.strat.(strategy.Failer); ok {
		failure, fails = f.LastFailure(), true
	}
	ss.mu.Lock()
	if fails {
		ss.snap.Err = failure
	}
	ss.snap.Account = acct
	if ss.snap.Capital > 0 {
		ss.snap.Return = acct.Equity/ss.snap.Capital - 1
	}
	if note != "" {
		ss.snap.Note = note
	} else if len(ss.snap.Logs) > 0 {
		ss.snap.Note = stripTime(ss.snap.Logs[len(ss.snap.Logs)-1])
	}
	ss.snap.Updated = ss.o.Now().UnixMilli()
	ss.dirty = true
	ss.mu.Unlock()
}

func stripTime(l string) string {
	if len(l) > 17 && l[16] == ' ' {
		return l[17:]
	}
	return l
}

// publish sends the latest snapshot if it changed (coalescing).
func (ss *session) publish() {
	ss.mu.Lock()
	if !ss.dirty {
		ss.mu.Unlock()
		return
	}
	ss.dirty = false
	snap := ss.copySnap()
	ss.mu.Unlock()
	select {
	case <-ss.updates:
	default:
	}
	select {
	case ss.updates <- snap:
	default:
	}
}

func (ss *session) copySnap() Snapshot {
	s := ss.snap
	s.Equity = append(domain.Series(nil), s.Equity...)
	s.Fills = append([]domain.Fill(nil), s.Fills...)
	s.Logs = append([]string(nil), s.Logs...)
	s.Account.Positions = append([]domain.Position(nil), s.Account.Positions...)
	return s
}

// Snapshot returns a copy of the current state.
func (ss *session) Snapshot() Snapshot {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.copySnap()
}

// Updates streams coalesced snapshots (at most ~4/s). It is closed by Stop.
func (ss *session) Updates() <-chan Snapshot { return ss.updates }

// finish marks the session stopped and writes the final state. It runs
// once, when the run loop exits (Stop or the Start context ending).
func (ss *session) finish() {
	ss.mu.Lock()
	ss.snap.Running = false
	ss.dirty = true
	ss.mu.Unlock()
	ss.stopErr = ss.persist()
	if ss.path != "" {
		live.Lock()
		delete(live.m, ss.path)
		live.Unlock()
	}
}

// Stop ends the session, persists the final state and closes Updates.
func (ss *session) Stop() error {
	ss.stopOnce.Do(func() {
		ss.cancel()
		<-ss.done
		ss.publish()
		close(ss.updates)
	})
	return ss.stopErr
}

// persist writes the state file atomically (no-op without DataDir).
func (ss *session) persist() error {
	if ss.path == "" {
		return nil
	}
	prices := map[string]float64{}
	for _, c := range ss.spec.Universe {
		if p := ss.eng.Price(c); p > 0 {
			prices[c] = p
		}
	}
	ss.mu.Lock()
	st := stateFile{Version: stateVersion, Snapshot: ss.copySnap(), Ledger: ss.eng.L.State(), Pending: ss.eng.Pending(),
		LastBar: copyMap(ss.lastBar), LastFund: copyMap(ss.lastFund), Prices: prices}
	ss.mu.Unlock()
	b, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("paper: encode state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(ss.path), 0o700); err != nil {
		return fmt.Errorf("paper: %w", err)
	}
	tmp := ss.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("paper: write state: %w", err)
	}
	if err := os.Rename(tmp, ss.path); err != nil {
		return fmt.Errorf("paper: write state: %w", err)
	}
	return nil
}

// restore loads a persisted session with the same ID, if any.
func (ss *session) restore() (bool, error) {
	if ss.path == "" {
		return false, nil
	}
	b, err := os.ReadFile(ss.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("paper: read state: %w", err)
	}
	var st stateFile
	if err := json.Unmarshal(b, &st); err != nil {
		return false, fmt.Errorf("paper: corrupt state %s: %w", ss.path, err)
	}
	if st.Snapshot.Strategy != "" && st.Snapshot.Strategy != ss.spec.Name {
		return false, fmt.Errorf("paper: session %s belongs to strategy %q, not %q", ss.o.ID, st.Snapshot.Strategy, ss.spec.Name)
	}
	ss.eng = sim.NewEngineWithLedger(sim.LedgerFromState(st.Ledger), ss.o.Costs, ss.spec.Universe)
	ss.eng.SetPending(st.Pending)
	ss.snap = st.Snapshot
	ss.snap.Err = ""
	ss.snap.Capital = st.Ledger.Capital
	ss.snap.Universe, ss.snap.Interval, ss.snap.Kind = ss.spec.Universe, ss.spec.Interval, ss.spec.Kind
	for k, v := range st.LastBar {
		ss.lastBar[k] = v
	}
	for k, v := range st.LastFund {
		ss.lastFund[k] = v
	}
	return true, nil
}

func copyMap(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// List returns persisted sessions in dataDir, newest first. Running is true
// only for sessions running in this process.
func List(dataDir string) ([]Snapshot, error) {
	if dataDir == "" {
		return nil, errors.New("paper: no data dir")
	}
	dir := filepath.Join(dataDir, "paper")
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Snapshot{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("paper: %w", err)
	}
	out := []Snapshot{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		s, err := loadPath(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // a corrupt file must not hide the others
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Started != out[j].Started {
			return out[i].Started > out[j].Started
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Load returns one persisted session by ID.
func Load(dataDir, id string) (Snapshot, error) {
	if !validID(id) {
		return Snapshot{}, fmt.Errorf("paper: invalid session id %q", id)
	}
	return loadPath(filepath.Join(dataDir, "paper", id+".json"))
}

// Remove deletes a persisted session that is not running.
func Remove(dataDir, id string) error {
	if !validID(id) {
		return fmt.Errorf("paper: invalid session id %q", id)
	}
	p := filepath.Join(dataDir, "paper", id+".json")
	live.Lock()
	running := live.m[p]
	live.Unlock()
	if running {
		return fmt.Errorf("paper: session %s is running; stop it first", id)
	}
	if err := os.Remove(p); err != nil {
		return fmt.Errorf("paper: %w", err)
	}
	return nil
}

func loadPath(p string) (Snapshot, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return Snapshot{}, fmt.Errorf("paper: %w", err)
	}
	var st stateFile
	if err := json.Unmarshal(b, &st); err != nil {
		return Snapshot{}, fmt.Errorf("paper: corrupt state %s: %w", p, err)
	}
	s := st.Snapshot
	live.Lock()
	s.Running = live.m[p]
	live.Unlock()
	if s.Capital == 0 {
		s.Capital = st.Ledger.Capital
	}
	if s.Capital > 0 && len(s.Equity) > 0 {
		s.Return = s.Equity.Last()/s.Capital - 1
	}
	return s, nil
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "session"
	}
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}

func validID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(b)
}
