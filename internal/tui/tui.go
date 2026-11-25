package tui

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp/internal/agent"
	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/llm"
	"github.com/maxmoneycash/hlp/internal/theme"
	"github.com/maxmoneycash/hlp/internal/tui/glyph"
)

// Options configure the interactive shell.
type Options struct {
	Env      *core.Env
	Registry *core.Registry
	Agent    *agent.Agent // nil = no LLM; free text goes through agent.ParseIntent only
	// Initial runs one input line on start (e.g. `hlp chart BTC` opens the
	// shell with that card, `hlp -c "/vaults"`).
	Initial string
	// Pane opens a full-screen pane directly (chart, book, vaults, watch).
	Pane string
	// WSFeed, when set, streams WebSocket connection state to the status
	// bar. The CLI creates it with NewWSStateFeed and passes feed.OnState as
	// ws.Options.OnState.
	WSFeed *WSStateFeed
}

// Model is the root Bubble Tea v2 model. Layout (docs/DESIGN.md 6.1):
//
//	row 0     hlp                    BTC 64,951 −0.9%   ETH …        ■
//	rows 1..  transcript: echo lines and cards, the newest in the reticle
//	          (side panel on the right with ctrl+b on wide terminals)
//	row H-2   ─────────────────────────────────────────────────────────
//	row H-1   ❯ █                                                 ? keys
//
// Full-screen panes (chart, book, vault tearsheet, vaults screener, arena)
// open over the transcript with Enter on a card or ctrl+o and close with
// Esc/q, returning to the transcript.
type Model struct {
	opts Options
	env  *core.Env

	// Candle charts: setup-rush skin ("" = terminal) and style ("" =
	// classic). /theme <skin> and /candles <style> change them.
	chartSkin  string
	chartStyle chart.Style
	reg        *core.Registry
	cmds       []command

	// Seams for tests and for running before core is wired; default to the
	// core/agent implementations.
	parse  func(core.Tool, []string) (json.RawMessage, error)
	invoke func(context.Context, *core.Env, core.Call) (*core.Result, error)
	intent func(string, *core.Registry) (core.Call, bool)
	now    func() time.Time

	// Root context: canceled by Close; every job and stream derives from it.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	th            theme.Theme
	themeExplicit bool
	// termBG is set once the terminal reported its background; termDark
	// is whether that background is dark. A theme of the other polarity
	// repaints the terminal background (View.BackgroundColor).
	termBG, termDark bool
	profile          colorprofile.Profile
	st               chrome
	width, height    int
	focused          bool

	entries []*entry
	nextID  int
	scroll  int // transcript lines scrolled up from the bottom

	in       editor
	hist     *history
	compSel  int
	compOff  bool // completion dismissed with esc until the input changes
	palette  *palette
	showHelp bool
	showSide bool

	panes []*pane // stack; last is on top

	jobs      map[int]context.CancelFunc
	spinFrame int
	spinning  bool

	agentRun   int
	agentHist  []llm.Message
	agentText  int // entry id receiving streamed text (-1 = start a new one)
	agentCh    chan tea.Msg
	agentStop  context.CancelFunc
	pendingFor map[string][]int // agent tool name -> pending entry ids

	confirmCh chan confirmReq
	confirm   *confirmState

	live map[string]*liveSub
	mids map[string]float64
	prev map[string]float64
	// prevDay is each coin's price 24h ago (watchlist change column).
	prevDay map[string]float64
	acct    *domain.Account

	ctrlC       bool
	wsState     string
	latency     time.Duration
	openOnReady string // pane kind to open when the next result lands (Options.Pane)
	quitting    bool

	frame          []string // last rendered screen (Update renders, View serves)
	viewLo, viewHi int      // transcript lines currently on screen
	edges          []int    // per transcript line: first column the floor may use
	floorMemo      map[floorKey]string

	// Motion (DESIGN.md section 7): only the ticker flaps and the cursor
	// move, and a tick is scheduled only while one of them is in flight.
	reduced, reducedSet bool
	flaps               map[string]*glyph.Flap // ticker coin -> its price/change flap
	flapArmed           bool                   // a flapMsg tick is pending
	lastKey             time.Time              // cursor blink phase origin
	blinkArmed          bool                   // a blinkMsg tick is pending
	blinkGen            int                    // invalidates stale blink ticks
	tickerKey           string                 // memo key of tickerLine
	tickerLine          string

	// hoverEntry is the transcript entry under the mouse (0 = none): it
	// takes the reticle from the newest card (DESIGN.md 4.1).
	hoverEntry int
	// ranAny: a command has run in this shell; seen: tips shown once ever.
	ranAny bool
	seen   *seenTips
}

// New builds the root model.
func New(opts Options) *Model {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Model{
		opts:       opts,
		env:        opts.Env,
		reg:        opts.Registry,
		parse:      core.ParseArgs,
		intent:     intentFor(opts.Env),
		now:        time.Now,
		ctx:        ctx,
		cancel:     cancel,
		focused:    true,
		jobs:       map[int]context.CancelFunc{},
		live:       map[string]*liveSub{},
		mids:       map[string]float64{},
		prev:       map[string]float64{},
		prevDay:    map[string]float64{},
		pendingFor: map[string][]int{},
		agentText:  -1,
		confirmCh:  make(chan confirmReq),
		profile:    colorprofile.TrueColor,
		width:      100,
		height:     30,
	}
	if m.reg != nil {
		reg := m.reg
		m.invoke = func(ctx context.Context, env *core.Env, call core.Call) (*core.Result, error) {
			return reg.Invoke(tuiSource(ctx), env, call)
		}
	} else {
		m.invoke = func(context.Context, *core.Env, core.Call) (*core.Result, error) {
			return nil, core.Errorf(core.CodeInternal, "no tool registry")
		}
	}
	m.cmds = commandsFrom(m.reg)
	dataDir := ""
	if m.env != nil {
		if m.env.Now != nil {
			m.now = m.env.Now
		}
		if m.env.Profile != colorprofile.Unknown {
			m.profile = m.env.Profile
		}
		m.th = m.env.Theme
		m.themeExplicit = m.env.Config.Theme != ""
		m.chartSkin = m.env.Config.Chart.Theme
		if st, ok := chart.ParseStyle(m.env.Config.Chart.Style); ok {
			m.chartStyle = st
		}
		dataDir = m.env.Paths.DataDir
		// The shell is interactive: mutating tools ask here (confirm card).
		m.env.Confirm = m.confirmer
	}
	if m.th.Name == "" {
		m.th = theme.Default(true)
	}
	m.hist = newHistory(dataDir)
	m.seen = loadSeen(dataDir)
	m.flaps = map[string]*glyph.Flap{}
	m.restyle()
	m.openOnReady = opts.Pane
	m.addWelcome()
	return m
}

// Init implements tea.Model: ask the terminal for its background, start
// listening for confirm requests and WS state, and run the initial line.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor, m.waitConfirm()}
	if m.opts.WSFeed != nil {
		cmds = append(cmds, waitWSState(m.opts.WSFeed.C()))
	}
	if m.opts.Initial != "" {
		cmds = append(cmds, m.submit(m.opts.Initial))
	}
	cmds = append(cmds, m.reconcileLive())
	return tea.Batch(cmds...)
}

// Close cancels every running tool, agent turn and stream and waits (up to
// two seconds) for their goroutines to finish. Run calls it on exit.
func (m *Model) Close() {
	m.cancel()
	for k, s := range m.live {
		s.cancel()
		delete(m.live, k)
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// Run starts the program and blocks until exit.
func Run(ctx context.Context, opts Options) error {
	m := New(opts)
	defer m.Close()
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

// tuiSource tags a call the shell runs for the person at the keyboard as
// "tui", so the trading gates apply the interactive policy (audit S6). A
// ctx that already carries a source (the agent's "agent") keeps it: the
// assistant must never be upgraded to the human's trust.
func tuiSource(ctx context.Context) context.Context {
	if core.SourceFrom(ctx) == core.SourceUnknown {
		return core.WithSource(ctx, "tui")
	}
	return ctx
}

// refuseSecret reports whether line looks like a key or seed phrase and,
// if so, shows the refusal instead of sending it (audit S10). The submit
// handler calls it before anything is sent to the model or the history.
func (m *Model) refuseSecret(line string) bool {
	if !looksSecret(line) {
		return false
	}
	m.add(&entry{kind: entryError, err: &core.Error{Code: core.CodeSafety, Message: secretRefusal}})
	return true
}

// intentFor is agent.ParseIntentWith with IsCoin backed by the market
// registry, so bare coin phrases ("pengu 4h") work for every listed market,
// not only the built-in list.
func intentFor(env *core.Env) func(string, *core.Registry) (core.Call, bool) {
	var isCoin func(string) bool
	if env != nil && env.HL != nil {
		isCoin = func(s string) bool {
			r := env.HL.Registry()
			if r == nil {
				return false
			}
			_, err := r.Resolve(s)
			return err == nil
		}
	}
	return func(text string, r *core.Registry) (core.Call, bool) {
		return agent.ParseIntentWith(text, r, agent.IntentOptions{IsCoin: isCoin})
	}
}
