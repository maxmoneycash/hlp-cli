package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/maxmoneycash/hlp-cli/internal/agent"
	"github.com/maxmoneycash/hlp-cli/internal/chart"
	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/llm"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
	"github.com/maxmoneycash/hlp-cli/internal/tui/glyph"
)

// resultMsg carries a finished tool call back to the model.
type resultMsg struct {
	job    int
	target target
	res    *core.Result
	err    error
	dur    time.Duration
}

// target says where a result goes: a transcript entry or a pane.
type target struct {
	entry int   // transcript entry id (0 = none)
	pane  *pane // pane to fill (nil = none)
	push  bool  // push a new pane for the result instead of filling one
}

// spinMsg advances the pending-card spinner while any job runs.
type spinMsg struct{}

// agentEventMsg / agentDoneMsg stream an agent turn.
type agentEventMsg struct {
	run int
	ev  agent.Event
}

type agentDoneMsg struct {
	run  int
	hist []llm.Message
	err  error
}

// submit handles a line from the prompt (or Options.Initial).
func (m *Model) submit(line string) tea.Cmd {
	text := strings.TrimSpace(line)
	if text == "" {
		return nil
	}
	if m.refuseSecret(text) {
		m.in.Reset()
		return nil
	}
	m.hist.Add(text)
	m.in.Reset()
	m.ranAny = true
	m.seen.Mark(tipWelcome) // the first-run line has done its job
	m.compSel, m.compOff = 0, false
	if strings.HasPrefix(text, "/") {
		return m.runSlash(text)
	}
	return m.runText(text)
}

func (m *Model) runSlash(text string) tea.Cmd {
	fields := splitArgs(strings.TrimPrefix(text, "/"))
	if len(fields) == 0 {
		m.showHelp = true
		return nil
	}
	name, args := strings.ToLower(fields[0]), fields[1:]
	switch name {
	case "help", "?":
		m.showHelp = true
		return nil
	case "clear":
		m.entries = nil
		m.scroll, m.hoverEntry = 0, 0
		m.addWelcome()
		return nil
	case "theme":
		if len(args) == 0 {
			skin := m.chartSkin
			if skin == "" {
				skin = chart.DefaultPalette
			}
			if m.chartSkin == "" && m.th.ChartSkin != "" {
				skin = m.th.ChartSkin
			}
			m.add(&entry{kind: entryInfo, text: m.choiceList("THEME", theme.Names(), m.th.Name) + "\n" +
				m.choiceList("CHART", append(chart.PaletteNames(), chart.SkinNone), skin)})
			return nil
		}
		if t, ok := theme.Get(args[0]); ok {
			m.setTheme(t)
			return nil
		}
		if p, ok := chart.PaletteByName(args[0]); ok || strings.EqualFold(args[0], chart.SkinNone) {
			m.chartSkin = p.Name
			if !ok {
				m.chartSkin = chart.SkinNone
			}
			m.restyle()
			return nil
		}
		m.add(&entry{kind: entryUser, text: text})
		m.add(&entry{kind: entryError, err: &core.Error{Code: core.CodeUsage, Message: "Unknown theme " + args[0], Hint: "/theme"}})
		return nil
	case "candles":
		if len(args) == 0 {
			names := make([]string, len(chart.Styles))
			for i, s := range chart.Styles {
				names[i] = string(s)
			}
			cur := m.chartStyle
			if cur == "" {
				cur = chart.Classic
			}
			m.add(&entry{kind: entryInfo, text: m.choiceList("CANDLES", names, string(cur))})
			return nil
		}
		if st, ok := chart.ParseStyle(args[0]); ok {
			m.chartStyle = st
			for _, p := range m.panes {
				p.style = 0
			}
			m.restyle()
			return nil
		}
		m.add(&entry{kind: entryUser, text: text})
		m.add(&entry{kind: entryError, err: &core.Error{Code: core.CodeUsage, Message: "Unknown candle style " + args[0], Hint: "/candles"}})
		return nil
	case "open":
		return m.openLatest()
	case "quit", "exit", "q":
		m.quitting = true
		return tea.Quit
	}
	m.add(&entry{kind: entryUser, text: text})
	if m.reg == nil {
		m.add(&entry{kind: entryError, err: core.Errorf(core.CodeInternal, "no tool registry")})
		return nil
	}
	tool, ok := m.reg.BySlash(name)
	if !ok {
		m.add(&entry{kind: entryInfo, text: m.unknownCommand(name)})
		return nil
	}
	raw, err := m.parse(tool, args)
	if err != nil {
		m.add(&entry{kind: entryError, err: withUsageHint(err, tool)})
		return nil
	}
	return m.startCall(tool, core.Call{Tool: tool.Name, Args: raw}, strings.TrimPrefix(text, "/"))
}

// withUsageHint adds the slash usage to a usage error without a hint.
func withUsageHint(err error, t core.Tool) error {
	var ce *core.Error
	if errors.As(err, &ce) && ce.Hint == "" {
		c := *ce
		usage := "/" + t.Slash
		for _, p := range t.Positional {
			usage += " <" + p + ">"
		}
		c.Hint = usage
		return &c
	}
	return err
}

// unknownCommand is `/opne?  /open`: the typo, then the closest commands
// in ink (DESIGN.md 5.2 Unknown command).
func (m *Model) unknownCommand(name string) string {
	matches := suggestCommands(m.cmds, name)
	var b strings.Builder
	b.WriteString(m.st.warn.Render("/" + name + "?"))
	for _, c := range matches[:min(3, len(matches))] {
		b.WriteString("  " + m.st.text.Render("/"+c.slash))
	}
	return b.String()
}

// choiceList is a `LABEL  a  b  ▌c  d` row: the names in ink, the current
// one bold behind the selection bar.
func (m *Model) choiceList(label string, names []string, cur string) string {
	var b strings.Builder
	b.WriteString(m.st.label.Render(label))
	for _, n := range names {
		b.WriteString("  ")
		if strings.EqualFold(n, cur) {
			b.WriteString(m.st.accent.Render(glyph.Sel) + m.st.sel.Render(n))
			continue
		}
		b.WriteString(" " + m.st.text.Render(n))
	}
	return b.String()
}

// runText routes free text: the deterministic intent parser first, then
// the agent when a model is configured, else friendly suggestions.
func (m *Model) runText(text string) tea.Cmd {
	m.add(&entry{kind: entryUser, text: text})
	if m.reg != nil && m.intent != nil {
		if call, ok := m.intent(text, m.reg); ok {
			if tool, ok := m.reg.Get(call.Tool); ok {
				return m.startCall(tool, call, text)
			}
		}
	}
	if m.opts.Agent != nil {
		return m.startAgent(text)
	}
	m.add(&entry{kind: entryInfo, text: m.noModelHint(text)})
	return nil
}

// noModelHint answers plain text the intent parser did not catch when no
// model is configured: the first time ever a gold `! NO MODEL  /help`
// tip, after that only the commands the words named, or a Dim NO MODEL.
func (m *Model) noModelHint(text string) string {
	// Offer read-only commands named by the words typed ("funding please").
	// Trade commands are never suggested from loose text.
	seen := map[string]bool{}
	var picks []string
	// The closest read-only command the loose intent parser finds comes
	// first ("best risk-adjusted vaults" -> /vaults --sort sharpe).
	var best string
	if m.reg != nil {
		if line, ok := agent.Suggest(text, m.reg); ok {
			best = line
			seen[strings.TrimPrefix(strings.Fields(line)[0], "/")] = true
		}
	}
	for _, w := range strings.Fields(strings.ToLower(text)) {
		if c, ok := findCommand(m.cmds, strings.Trim(w, "?!.,")); ok && c.tool != "" && c.group != "trade" && !seen[c.slash] {
			seen[c.slash] = true
			picks = append(picks, "/"+c.slash)
		}
	}
	sort.Strings(picks)
	if best != "" {
		picks = append([]string{best}, picks...)
	}
	picks = picks[:min(3, len(picks))]
	var parts []string
	if !m.seen.Seen(tipNoModel) {
		m.seen.Mark(tipNoModel)
		parts = append(parts, m.st.warn.Render(glyph.Note+" NO MODEL")+"  "+m.st.text.Render("/help model"))
	} else if len(picks) == 0 {
		parts = append(parts, m.st.dim.Render("NO MODEL"))
	}
	for _, p := range picks {
		parts = append(parts, m.st.text.Render(p))
	}
	return strings.Join(parts, "   ")
}

// skeletonRows guesses the result height so the transcript barely moves
// when the card lands.
func skeletonRows(t core.Tool) int {
	switch t.Name {
	case "get_candles":
		return 12
	case "list_vaults", "get_markets", "get_fills", "get_trades":
		return 8
	case "get_vault", "compare_vaults", "run_backtest":
		return 10
	case "get_orderbook":
		return 11
	}
	return 3
}

// startCall runs a tool in the background, showing a skeleton card.
func (m *Model) startCall(tool core.Tool, call core.Call, label string) tea.Cmd {
	e := m.add(&entry{kind: entryPending, title: label, tool: tool, call: call, start: m.now(), rows: skeletonRows(tool)})
	return m.runJob(tool, call, target{entry: e.id})
}

// runJob invokes call in a goroutine bound to the root context.
func (m *Model) runJob(tool core.Tool, call core.Call, tg target) tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	m.nextID++
	job := m.nextID
	m.jobs[job] = cancel
	invoke, env, now := m.invoke, m.env, m.now
	m.wg.Add(1)
	run := func() tea.Msg {
		defer m.wg.Done()
		start := now()
		res, err := invoke(ctx, env, call)
		if err == nil && res == nil {
			err = core.Errorf(core.CodeInternal, "%s returned no result", tool.Name)
		}
		if ctx.Err() != nil && err != nil {
			err = context.Canceled
		}
		return resultMsg{job: job, target: tg, res: res, err: err, dur: now().Sub(start)}
	}
	return tea.Batch(run, m.ensureSpinner())
}

func (m *Model) ensureSpinner() tea.Cmd {
	if m.spinning {
		return nil
	}
	m.spinning = true
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return spinMsg{} })
}

func (m *Model) onSpin() tea.Cmd {
	m.spinFrame++
	if len(m.jobs) == 0 && m.agentStop == nil {
		m.spinning = false
		return nil
	}
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return spinMsg{} })
}

func (m *Model) onResult(msg resultMsg) tea.Cmd {
	if cancel, ok := m.jobs[msg.job]; ok {
		cancel()
		delete(m.jobs, msg.job)
	}
	if msg.err == nil {
		m.latency = msg.dur
	}
	switch {
	case msg.target.pane != nil:
		p := msg.target.pane
		p.loading = false
		if msg.err != nil {
			p.err = msg.err
			return nil
		}
		p.err = nil
		p.setResult(msg.res)
		return nil
	case msg.target.push:
		if msg.err != nil {
			if top := m.topPane(); top != nil {
				top.err = msg.err
				top.loading = false
			}
			return nil
		}
		if top := m.topPane(); top != nil {
			top.loading = false
		}
		m.pushPane(newPane(msg.res, 0, m.toolFor(msg.res, ""), core.Call{}))
		return nil
	}
	e := m.entryByID(msg.target.entry)
	if e == nil {
		return nil
	}
	e.cache = ""
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			e.kind, e.state = entryInfo, "STOPPED"
			return nil
		}
		e.kind, e.err = entryError, msg.err
		return nil
	}
	if next, ok := m.singleVault(e, msg.res); ok {
		// One vault matched: a 1-row table under a full header is a
		// wireframe, so the entry becomes that vault's tearsheet card.
		e.kind, e.start = entryPending, m.now()
		return next
	}
	e.kind, e.result = entryCard, msg.res
	if m.openOnReady != "" {
		m.openOnReady = ""
		return m.openEntry(e)
	}
	return nil
}

// singleVault starts the tearsheet for a vault list that matched exactly
// one vault (/vaults --tag memes), in place of the entry's 1-row table.
func (m *Model) singleVault(e *entry, r *core.Result) (tea.Cmd, bool) {
	if r == nil || r.Card != core.CardVaults || e.tool.Name != "list_vaults" || m.reg == nil {
		return nil, false
	}
	vs, ok := r.Data.([]domain.VaultSummary)
	if p, isPtr := r.Data.(*[]domain.VaultSummary); isPtr && p != nil {
		vs, ok = *p, true
	}
	if !ok || len(vs) != 1 || vs[0].Address == "" {
		return nil, false
	}
	tool, ok := m.reg.Get("get_vault")
	if !ok {
		return nil, false
	}
	raw, _ := json.Marshal(map[string]any{"vault": vs[0].Address})
	call := core.Call{Tool: tool.Name, Args: raw}
	e.tool, e.call = tool, call
	return m.runJob(tool, call, target{entry: e.id}), true
}

// toolFor finds the registry tool behind a result (by explicit name, or by
// the card kind for results that arrive without a call).
func (m *Model) toolFor(r *core.Result, name string) core.Tool {
	if m.reg == nil {
		return core.Tool{}
	}
	if t, ok := m.reg.Get(name); ok {
		return t
	}
	if r == nil {
		return core.Tool{}
	}
	byCard := map[core.Card]string{
		core.CardChart: "get_candles", core.CardBook: "get_orderbook", core.CardVaults: "list_vaults",
		core.CardVault: "get_vault", core.CardArena: "arena", core.CardPaper: "paper",
	}
	t, _ := m.reg.Get(byCard[r.Card])
	return t
}

// interrupt cancels every running tool and agent turn (Esc).
func (m *Model) interrupt() bool {
	any := len(m.jobs) > 0 || m.agentStop != nil
	for id, cancel := range m.jobs {
		cancel()
		delete(m.jobs, id)
	}
	if m.agentStop != nil {
		m.agentStop()
		m.agentStop = nil
	}
	for _, e := range m.entries {
		e.endStream()
		if e.kind == entryPending {
			e.kind, e.state, e.cache = entryInfo, "STOPPED", ""
		}
	}
	return any
}

// startAgent runs one agent turn, streaming events into the transcript.
func (m *Model) startAgent(text string) tea.Cmd {
	m.agentRun++
	run := m.agentRun
	ctx, cancel := context.WithCancel(m.ctx)
	m.agentStop = cancel
	m.agentText = -1
	m.pendingFor = map[string][]int{}
	ch := make(chan tea.Msg, 64)
	m.agentCh = ch
	hist := append([]llm.Message(nil), m.agentHist...)
	ag := m.opts.Agent
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(ch)
		emit := func(ev agent.Event) {
			select {
			case ch <- agentEventMsg{run: run, ev: ev}:
			case <-ctx.Done():
			}
		}
		h, err := ag.Run(ctx, hist, text, emit)
		select {
		case ch <- agentDoneMsg{run: run, hist: h, err: err}:
		case <-ctx.Done():
		}
	}()
	return tea.Batch(waitOn(ch), m.ensureSpinner())
}

func (m *Model) onAgentEvent(msg agentEventMsg) tea.Cmd {
	if msg.run != m.agentRun {
		return nil
	}
	ev := msg.ev
	switch ev.Kind {
	case agent.EvText:
		e := m.entryByID(m.agentText)
		if e == nil {
			e = m.add(&entry{kind: entryAssistant})
			m.agentText = e.id
		}
		// Model output is third-party text: no terminal controls (S3).
		e.text += domain.CleanText(ev.Text)
		e.streaming = true
		e.cache = ""
	case agent.EvToolStart:
		m.endAgentText()
		label := ev.Tool
		if t, ok := m.reg.Get(ev.Tool); ok && t.Slash != "" {
			label = t.Slash + " " + compactArgs(ev.Args)
		}
		t, _ := m.reg.Get(ev.Tool)
		e := m.add(&entry{kind: entryPending, title: strings.TrimSpace(label), tool: t, start: m.now(), rows: skeletonRows(t)})
		m.pendingFor[ev.Tool] = append(m.pendingFor[ev.Tool], e.id)
	case agent.EvToolResult:
		m.endAgentText()
		var e *entry
		if q := m.pendingFor[ev.Tool]; len(q) > 0 {
			e = m.entryByID(q[0])
			m.pendingFor[ev.Tool] = q[1:]
		}
		if e == nil {
			e = m.add(&entry{})
		}
		e.cache = ""
		if ev.Err != nil || ev.Result == nil {
			err := ev.Err
			if err == nil {
				err = fmt.Errorf("%s returned no result", ev.Tool)
			}
			e.kind, e.err = entryError, err
		} else {
			e.kind, e.result = entryCard, ev.Result
		}
	case agent.EvConfirm:
		m.add(&entry{kind: entryInfo, text: m.st.warn.Render(glyph.Note + " CONFIRM BELOW")})
	case agent.EvError:
		m.endAgentText()
		if ev.Err != nil && !errors.Is(ev.Err, context.Canceled) {
			m.add(&entry{kind: entryError, err: ev.Err})
		}
	}
	return waitOn(m.agentCh)
}

func (m *Model) onAgentDone(msg agentDoneMsg) tea.Cmd {
	if msg.run != m.agentRun {
		return nil
	}
	if msg.hist != nil {
		m.agentHist = msg.hist
	}
	m.endAgentText()
	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
		m.add(&entry{kind: entryError, err: msg.err})
	}
	if m.agentStop != nil {
		m.agentStop()
		m.agentStop = nil
	}
	for _, ids := range m.pendingFor {
		for _, id := range ids {
			if e := m.entryByID(id); e != nil && e.kind == entryPending {
				e.kind, e.state, e.cache = entryInfo, "NO RESULT", ""
			}
		}
	}
	m.pendingFor = map[string][]int{}
	return nil
}

// endAgentText closes the reply being streamed (one final markdown render)
// and makes the next text delta start a new entry.
func (m *Model) endAgentText() {
	m.entryByID(m.agentText).endStream()
	m.agentText = -1
}

// compactArgs renders tool args JSON as "BTC 4h"-style values for labels.
func compactArgs(raw []byte) string {
	s := strings.Trim(string(raw), "{}")
	if s == "" || s == "null" {
		return ""
	}
	var parts []string
	for _, kv := range strings.Split(s, ",") {
		if i := strings.Index(kv, ":"); i >= 0 {
			parts = append(parts, strings.Trim(strings.TrimSpace(kv[i+1:]), `"`))
		}
	}
	return oneLine(strings.Join(parts, " "), 40)
}

// waitOn reads the next message from ch (nil when the channel closes).
func waitOn(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// wsStateMsg reports a WebSocket state change.
type wsStateMsg string

func waitWSState(ch <-chan string) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		s, ok := <-ch
		if !ok {
			return nil
		}
		return wsStateMsg(s)
	}
}

func (m *Model) setTheme(t theme.Theme) {
	m.th = t
	m.themeExplicit = true
	m.restyle()
}

func (m *Model) cycleTheme() {
	names := theme.Names()
	if len(names) == 0 {
		return
	}
	next := names[0]
	for i, n := range names {
		if n == m.th.Name {
			next = names[(i+1)%len(names)]
		}
	}
	if t, ok := theme.Get(next); ok {
		m.setTheme(t)
	}
}
