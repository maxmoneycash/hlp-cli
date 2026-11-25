package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/fang/v2"
	"github.com/charmbracelet/colorprofile"
	xterm "github.com/charmbracelet/x/term"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/maxmoneycash/hlp/internal/agent"
	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/cli/mcp"
	"github.com/maxmoneycash/hlp/internal/config"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/llm"
	"github.com/maxmoneycash/hlp/internal/tui"
	"github.com/maxmoneycash/hlp/internal/tui/cards"
)

// BuildInfo is injected by main via -ldflags.
type BuildInfo struct {
	Version, Commit, Date string
}

// Global flags shared by every command.
type globals struct {
	json, ndjson, plain bool
	sel                 string
	limit               int
	color               string
	testnet             bool
	config              string
	theme               string
	command             string

	resultsOnly, raw, csv bool
	live                  bool
	maxEvents             int
	forDur                time.Duration
}

// IO is the process I/O; tests substitute buffers.
type IO struct {
	In             io.Reader
	Out, Err       io.Writer
	OutTTY, ErrTTY bool // stdout/stderr are terminals
	InTTY          bool
	Width          int // terminal columns (0 = unknown)
	Height         int // terminal rows (0 = unknown)
	Getenv         func(string) string
}

func stdIO() IO {
	w, h := 0, 0
	if xterm.IsTerminal(os.Stdout.Fd()) {
		if cols, rows, err := xterm.GetSize(os.Stdout.Fd()); err == nil {
			w, h = cols, rows
		}
	}
	return IO{
		In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
		OutTTY: IsTTY(os.Stdout), ErrTTY: IsTTY(os.Stderr), InTTY: IsTTY(os.Stdin),
		Width: w, Height: h, Getenv: os.Getenv,
	}
}

type app struct {
	io  IO
	g   *globals
	bi  BuildInfo
	reg *core.Registry
	// onWS, when set, receives WebSocket states (the TUI status bar).
	onWS func(string)
}

// Execute builds the command tree from the core registry and runs it.
// Returns the process exit code (0/1/2/3, see core.ExitCode).
func Execute(ctx context.Context, args []string, bi BuildInfo) int {
	return Run(ctx, args, bi, stdIO())
}

// Run is Execute with explicit I/O (tests, embedding).
func Run(ctx context.Context, args []string, bi BuildInfo, pio IO) int {
	if pio.Getenv == nil {
		pio.Getenv = os.Getenv
	}
	if pio.In == nil {
		pio.In = strings.NewReader("")
	}
	a := &app{io: pio, g: &globals{}, bi: bi, reg: core.Default()}
	root := a.rootCmd()
	root.SetArgs(args)
	// help and usage text go through a writer that trims trailing spaces
	// from every line, so `hlp --help | grep` and README pastes stay clean
	tw := &trimWriter{w: pio.Out}
	defer tw.Flush()
	root.SetOut(tw)
	root.SetErr(pio.Err)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &core.Error{Code: core.CodeUsage, Message: err.Error(), Hint: "see `hlp <command> --help`"}
	})
	err := fang.Execute(ctx, root,
		fang.WithVersion(bi.Version),
		fang.WithCommit(bi.Commit),
		fang.WithErrorHandler(func(_ io.Writer, _ fang.Styles, err error) {
			a.reportError(err)
		}),
	)
	return core.ExitCode(classify(err))
}

// classify turns cobra's plain usage errors into USAGE errors (exit 2).
func classify(err error) error {
	if err == nil {
		return nil
	}
	var e *core.Error
	if errors.As(err, &e) {
		return e
	}
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand", "invalid argument", "accepts ", "requires ", "flag needs"} {
		if strings.HasPrefix(msg, p) {
			return &core.Error{Code: core.CodeUsage, Message: msg, Hint: "run `hlp --help`"}
		}
	}
	return err
}

func (a *app) jsonErrors() bool {
	m, err := a.mode()
	return err != nil || m.JSON || !a.io.ErrTTY
}

func (a *app) reportError(err error) {
	err = classify(err)
	if a.jsonErrors() {
		WriteError(a.io.Err, err)
		return
	}
	writeHumanError(a.io.Err, err)
}

func (a *app) mode() (Mode, error) {
	g := a.g
	m, err := ResolveMode(g.json, g.ndjson, g.plain, g.sel, g.limit, g.color, a.io.OutTTY, a.io.Getenv)
	if err != nil {
		return m, err
	}
	m.ResultsOnly, m.Raw, m.CSV = g.resultsOnly, g.raw, g.csv
	if m.ResultsOnly || m.Raw || m.CSV {
		m.JSON, m.Plain = true, false
	}
	if m.CSV && m.NDJSON {
		return m, &core.Error{Code: core.CodeUsage, Message: "--csv and --ndjson cannot be combined"}
	}
	m.Live, m.MaxEvents, m.For = g.live, g.maxEvents, g.forDur
	if m.MaxEvents == 0 {
		if v := strings.TrimSpace(a.io.Getenv("HLP_MAX_EVENTS")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return m, &core.Error{Code: core.CodeUsage, Message: "HLP_MAX_EVENTS must be a whole number, got " + strconv.Quote(v)}
			}
			m.MaxEvents = n
		}
	}
	if m.MaxEvents < 0 {
		return m, &core.Error{Code: core.CodeUsage, Message: "--max-events must be 0 (unbounded) or more"}
	}
	if m.For < 0 {
		return m, &core.Error{Code: core.CodeUsage, Message: "--for must be a positive duration, e.g. 30s or 5m"}
	}
	return m, nil
}

func (a *app) rootCmd() *cobra.Command {
	g := a.g
	root := &cobra.Command{
		Use:   "hlp",
		Short: "The Hyperliquid terminal for vaults",
		Long: "hlp: discover, vet, backtest and run Hyperliquid strategy vaults from a chat-style TUI\n" +
			"with candlestick charts, vault tearsheets and paper trading. Every command also speaks JSON and MCP.\n\n" +
			"Run `hlp` with no arguments for the interactive shell. No keys needed to start.",
		Example: "  hlp chart BTC 1h\n  hlp vaults --sort sharpe --limit 10\n  hlp vault \"Long HYPE & BTC\"\n" +
			"  hlp backtest examples/thesis/long-spy-short-memes.yaml\n  hlp vaults --json --select name,tvl | jq\n  hlp price btc --select mid -q\n" +
			"  hlp book btc --ndjson --max-events 3\n  claude mcp add hlp -- hlp mcp\n\n" + exitCodesHelp,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE:          a.runShell,
	}
	pf := root.PersistentFlags()
	pf.BoolVar(&g.json, "json", false, "Output JSON (automatic when stdout is not a TTY or HLP_JSON=1)")
	pf.BoolVar(&g.ndjson, "ndjson", false, "Stream newline-delimited JSON (one object per line)")
	pf.BoolVar(&g.plain, "plain", false, "Human layout without ANSI escapes")
	pf.StringVar(&g.sel, "select", "", "Fields to keep, comma separated, dot paths allowed (a,b.c)")
	pf.IntVar(&g.limit, "limit", 0, "Maximum rows")
	pf.BoolVar(&g.resultsOnly, "results-only", false, "Print data without the {data, meta} envelope")
	pf.BoolVarP(&g.raw, "raw", "q", false, "Print bare values (with --select of one field), else data only")
	pf.BoolVar(&g.csv, "csv", false, "Output the rows of data as CSV (nested fields as a.b)")
	pf.BoolVarP(&g.live, "live", "w", false, "Stream updates: redraw on a terminal, NDJSON when piped")
	pf.IntVar(&g.maxEvents, "max-events", 0, "Stop a stream after N results, exit 0 (env HLP_MAX_EVENTS)")
	pf.DurationVar(&g.forDur, "for", 0, "Stop a stream after this long, exit 0 (e.g. 30s, 5m)")
	pf.StringVar(&g.color, "color", "auto", "Color: auto, always or never")
	pf.BoolVar(&g.testnet, "testnet", false, "Use Hyperliquid testnet")
	pf.StringVar(&g.config, "config", "", "Config file (default ~/.config/hlp/config.toml)")
	pf.StringVar(&g.theme, "theme", "", "Theme name (see `hlp` then /theme)")
	root.Flags().StringVarP(&g.command, "command", "c", "", "run one line (\"/vaults\" or \"chart btc 4h\"); opens the shell on a TTY, prints JSON otherwise")

	root.AddGroup(
		&cobra.Group{ID: "market", Title: "Markets"},
		&cobra.Group{ID: "vault", Title: "Vaults"},
		&cobra.Group{ID: "account", Title: "Accounts (read-only, any address)"},
		&cobra.Group{ID: "strategy", Title: "Strategies"},
		&cobra.Group{ID: "trade", Title: "Trading (dry-run until the Phase-2 gate)"},
		&cobra.Group{ID: "meta", Title: "Agents and setup"},
	)
	root.SetHelpCommandGroupID("meta")
	root.SetCompletionCommandGroupID("meta")
	a.addToolCommands(root)
	root.AddCommand(a.pvaultCmd(), a.ticksCmd(), a.safeCmd(), a.mcpCmd(), a.versionCmd(), a.configCmd(), a.authCmd(), a.skillCmd())
	return root
}

// env builds the shared Env for one command.
func (a *app) env(ctx context.Context) (*core.Env, error) {
	o := core.EnvOptions{ConfigPath: a.g.config, Theme: a.g.theme, Version: a.bi.Version, OnWSState: a.onWS}
	if a.g.testnet {
		o.Network = domain.Testnet
	}
	if dir := a.io.Getenv("HLP_FIXTURES"); dir != "" {
		o.FixturesDir = dir
	}
	o.Profile = a.profile()
	env, err := core.NewEnv(ctx, o)
	if err != nil {
		return nil, err
	}
	// The confirm card and prompt go to stderr, so a person at a terminal
	// can confirm even when stdout is JSON or piped (audit S4). With no
	// terminal there is no confirmer and require_confirm refuses.
	if a.io.InTTY && a.io.ErrTTY {
		env.Confirm = a.confirm
	}
	return env, nil
}

func (a *app) profile() colorprofile.Profile {
	switch strings.ToLower(a.g.color) {
	case "never":
		return colorprofile.NoTTY
	case "always":
		return colorprofile.TrueColor
	}
	if !a.io.OutTTY {
		return colorprofile.NoTTY
	}
	if f, ok := a.io.Out.(*os.File); ok {
		return colorprofile.Detect(f, os.Environ())
	}
	return colorprofile.Env(os.Environ())
}

// confirm is the CLI Confirmer: render the preview card and ask y/N on the
// terminal. Only installed when stdin and stderr are both TTYs.
func (a *app) confirm(_ context.Context, p core.OrderPreview) (bool, error) {
	card := cards.Render(&core.Result{Card: core.CardOrder, Title: "Confirm", Data: &p}, cards.Opts{Width: a.width(), Plain: true})
	fmt.Fprintln(a.io.Err, card)
	badge := strings.ToUpper(string(p.Network))
	in := bufio.NewReader(a.io.In)
	ask := func(prompt string) (string, error) {
		fmt.Fprint(a.io.Err, prompt)
		line, err := in.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.ToLower(strings.TrimSpace(line)), nil
	}
	ans, err := ask(fmt.Sprintf("[%s] proceed? [y/N] ", badge))
	if err != nil || (ans != "y" && ans != "yes") {
		return false, err
	}
	// Flattening every position asks a second, typed question.
	if p.Action == "close_all" {
		ans, err = ask(fmt.Sprintf("[%s] this closes %s. Type \"close all\" to confirm: ", badge, p.Size))
		return err == nil && ans == "close all", err
	}
	return true, nil
}

func (a *app) width() int {
	if a.io.Width > 0 {
		return a.io.Width
	}
	if v, err := strconv.Atoi(a.io.Getenv("COLUMNS")); err == nil && v > 0 {
		return v
	}
	return 100
}

// globalFlagNames are persistent flags; tool inputs with these names reuse them.
var globalFlagNames = map[string]bool{"json": true, "ndjson": true, "plain": true, "select": true, "limit": true, "color": true, "testnet": true, "config": true, "theme": true, "help": true, "version": true,
	"results-only": true, "raw": true, "csv": true, "live": true, "max-events": true, "for": true}

// addToolCommands creates one cobra command per tool from Tool.Command
// ("vault compare" nests under "vault"), with flags generated from the
// input struct (json names, help from the jsonschema tag).
func (a *app) addToolCommands(root *cobra.Command) {
	byPath := map[string]*cobra.Command{"": root}
	tools := a.reg.All()
	for pass := 1; pass <= 3; pass++ {
		for _, t := range tools {
			if handWritten[t.Name] {
				continue
			}
			parts := strings.Fields(t.Command)
			if len(parts) != pass {
				continue
			}
			parentPath := strings.Join(parts[:len(parts)-1], " ")
			parent := byPath[parentPath]
			if parent == nil {
				parent = &cobra.Command{Use: parts[len(parts)-2], Short: groupShort(parts[len(parts)-2]), GroupID: t.Group}
				byPath[parentPath] = parent
				root.AddCommand(parent)
			}
			cmd := a.toolCmd(t, parts[len(parts)-1])
			if parent == root {
				cmd.GroupID = t.Group
			}
			parent.AddCommand(cmd)
			byPath[t.Command] = cmd
		}
	}
}

// handWritten tools have their own cobra commands (pvaultCmd: `hlp fund`
// with --rpc) instead of generated ones.
var handWritten = map[string]bool{"get_strategy_vault": true, "get_strategy_vault_epochs": true, "get_strategy_vault_user": true}

// groupShort is the help line of a parent command with subcommands.
func groupShort(name string) string {
	switch name {
	case "pine":
		return "Pine Script: check a file, or run a strategy on one market"
	case "vault":
		return "Vault tearsheet, compare, deposit and withdraw"
	case "vaults":
		return "Vault screener and the curated registry (discover)"
	}
	return "Subcommands of " + name
}

func (a *app) toolCmd(t core.Tool, use string) *cobra.Command {
	tool := t
	short := tool.Summary
	if tool.Mutating {
		short += " [mutating]"
	}
	cmd := &cobra.Command{
		Use:     use + positionalUse(tool),
		Short:   short,
		Long:    strings.TrimSpace(tool.Summary + "\n\n" + tool.Description),
		Aliases: commandAliases(tool),
		Args:    cobra.ArbitraryArgs,
		Example: examplesFor(tool),
	}
	a.hideGlobals(cmd, tool)
	fields := core.Fields(tool)
	slices := map[string]*[]string{}
	for _, f := range fields {
		if globalFlagNames[f.Flag] {
			continue
		}
		desc := safeUsage(f.Flag, f.Description)
		// Flags are the kebab-case of the camelCase JSON names; a
		// camelCase spelling (contract 1's --acceptPineforgeLicense) stays
		// as a hidden deprecated alias.
		names := []string{f.Flag}
		if f.Name != f.Flag {
			names = append(names, f.Name)
		}
		for i, name := range names {
			switch f.Kind {
			case reflect.Bool:
				cmd.Flags().Bool(name, false, desc)
			case reflect.Slice:
				v := new([]string)
				slices[name] = v
				cmd.Flags().StringArrayVar(v, name, nil, desc+" (comma list or repeat)")
			default:
				cmd.Flags().String(name, "", desc)
			}
			if i > 0 {
				_ = cmd.Flags().MarkHidden(name)
				_ = cmd.Flags().MarkDeprecated(name, "use --"+f.Flag)
			}
		}
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		argv := append([]string{}, args...)
		if side, ok := sideAliases[c.CalledAs()]; ok && tool.Name == "place_order" {
			argv = append([]string{side}, argv...) // hlp buy HYPE --usd 100
		}
		var flagArgs []string
		c.Flags().Visit(func(fl *pflag.Flag) {
			if globalFlagNames[fl.Name] {
				return
			}
			if v, ok := slices[fl.Name]; ok {
				for _, x := range *v {
					flagArgs = append(flagArgs, "--"+fl.Name+"="+x)
				}
				return
			}
			flagArgs = append(flagArgs, "--"+fl.Name+"="+fl.Value.String())
		})
		has := func(name string) bool {
			for _, f := range fields {
				if f.Name == name || f.Flag == name {
					return true
				}
			}
			return false
		}
		if has("limit") && a.g.limit > 0 {
			flagArgs = append(flagArgs, "--limit="+strconv.Itoa(a.g.limit))
		}
		if a.g.live {
			if !tool.Streaming {
				return &core.Error{Code: core.CodeUsage, Message: tool.Command + " does not stream", Hint: "-w/--live works on chart, candles, book, trades, price, account, watch and hlp"}
			}
			if has("live") {
				flagArgs = append(flagArgs, "--live=true")
			}
		}
		mode, err := a.mode()
		if err != nil {
			return err
		}
		if has("width") && !c.Flags().Changed("width") && !mode.JSON {
			flagArgs = append(flagArgs, "--width="+strconv.Itoa(min(a.width(), 400)))
		}
		raw, err := core.ParseArgs(tool, append(flagArgs, append([]string{"--"}, argv...)...))
		if err != nil {
			return err
		}
		return a.execute(c.Context(), core.Call{Tool: tool.Name, Args: raw}, mode)
	}
	return cmd
}

// sideAliases let `hlp buy|sell|long|short ...` carry the order side.
var sideAliases = map[string]string{"buy": "buy", "sell": "sell", "long": "buy", "short": "sell"}

// exitCodesHelp closes the root help.
const exitCodesHelp = "Exit codes: 0 ok · 1 runtime error · 2 usage · 3 auth, guard or confirm · 4 retryable (rate limit, network, 5xx)"

// hideGlobals keeps help short: output-shaping flags (--select, --limit,
// --ndjson, --csv, --raw, --results-only) are hidden on mutating tools, and
// the stream flags (--live, --max-events, --for) on tools that do not
// stream. A hidden local flag bound to the same variable shadows the global
// one, so the long form still parses; the short -q and -w do not exist
// there (a usage error, exit 2).
func (a *app) hideGlobals(cmd *cobra.Command, t core.Tool) {
	g, f := a.g, cmd.Flags()
	hide := func(name string) { _ = f.MarkHidden(name) }
	if t.Mutating {
		f.StringVar(&g.sel, "select", "", "Fields to keep (hidden: previews are small)")
		f.IntVar(&g.limit, "limit", 0, "Maximum rows (hidden on mutating tools)")
		f.BoolVar(&g.ndjson, "ndjson", false, "Stream newline-delimited JSON (hidden on mutating tools)")
		f.BoolVar(&g.csv, "csv", false, "Output CSV (hidden on mutating tools)")
		f.BoolVar(&g.resultsOnly, "results-only", false, "Print data only (hidden on mutating tools)")
		f.BoolVar(&g.raw, "raw", false, "Print bare values (hidden on mutating tools)")
		for _, n := range []string{"select", "limit", "ndjson", "csv", "results-only", "raw"} {
			hide(n)
		}
	}
	if !t.Streaming || t.Stream == nil || t.Mutating {
		f.IntVar(&g.maxEvents, "max-events", 0, "Stop a stream after N results (this command does not stream)")
		f.DurationVar(&g.forDur, "for", 0, "Stop a stream after this long (this command does not stream)")
		f.BoolVar(&g.live, "live", false, "Stream updates (this command does not stream)")
		hide("max-events")
		hide("for")
		hide("live")
	}
}

// examplesFor is a generated command's EXAMPLES section: Tool.Examples,
// else the few hand-written ones below.
func examplesFor(t core.Tool) string {
	if len(t.Examples) > 0 {
		return "  " + strings.Join(t.Examples, "\n  ")
	}
	return toolExamples[t.Name]
}

// commandAliases are CLI aliases: slash-style aliases that are single words.
// toolExamples are the EXAMPLES sections of generated tool commands.
var toolExamples = map[string]string{
	"get_strategy":    "  hlp strat\n  hlp strat trend\n  hlp strat spxmeme --json --select live.gross",
	"list_indexes":    "  hlp index\n  hlp index mavericks",
	"discover_vaults": "  hlp vaults discover\n  hlp vaults discover memes\n  hlp vaults --tag memes",
}

func commandAliases(t core.Tool) []string {
	var out []string
	last := t.Command[strings.LastIndex(t.Command, " ")+1:]
	for _, al := range append([]string{t.Slash}, t.Aliases...) {
		if al != "" && al != last && !strings.Contains(al, " ") {
			out = append(out, al)
		}
	}
	return out
}

func positionalUse(t core.Tool) string {
	var b strings.Builder
	for _, p := range t.Positional {
		fmt.Fprintf(&b, " [%s]", p)
	}
	return b.String()
}

// wantsStream decides whether a CLI call streams: --ndjson or --live
// (-w, or live in the args) on a streaming tool; and, only when a person is
// watching a terminal, `watch`, `paper start` and an entrant arena. Piped
// JSON otherwise gets one snapshot and exits (AGENT-UX #3).
func wantsStream(t core.Tool, raw json.RawMessage, m Mode, tty bool) bool {
	if !t.Streaming || t.Stream == nil || t.Mutating {
		return false
	}
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	switch {
	case m.NDJSON, m.Live, args["live"] == true:
		return true
	case !tty || m.JSON:
		return false
	case t.Name == "watch":
		return true
	case t.Name == "paper":
		return strings.EqualFold(fmt.Sprint(args["action"]), "start")
	case t.Name == "arena":
		// entrants or an arena file stream alike (a vault-only file emits
		// once and ends)
		ents, _ := args["entrants"].([]any)
		file, _ := args["file"].(string)
		return len(ents) > 0 || file != ""
	}
	return false
}

// execute runs a call and writes its output in the resolved mode.
func (a *app) execute(ctx context.Context, call core.Call, m Mode) error {
	env, err := a.env(ctx)
	if err != nil {
		return err
	}
	defer env.Close()
	switch {
	case m.NDJSON:
		// agents read progress as JSON lines on stderr (AGENT-UX #20)
		enc := json.NewEncoder(a.io.Err)
		enc.SetEscapeHTML(false)
		env.Progress = func(s string) {
			_ = enc.Encode(map[string]any{"progress": map[string]any{"stage": s}})
		}
		env.ProgressN = func(s string, done, total int) {
			_ = enc.Encode(map[string]any{"progress": map[string]any{"stage": s, "done": done, "total": total}})
		}
	case a.io.ErrTTY:
		// slow tools (cold backtests) report progress on one stderr line
		shown := false
		env.Progress = func(s string) {
			shown = true
			fmt.Fprint(a.io.Err, "\r\x1b[2K"+s)
		}
		defer func() {
			if shown {
				fmt.Fprint(a.io.Err, "\r\x1b[2K")
			}
		}()
	}
	ctx = core.WithSource(ctx, "cli")
	t, _ := a.reg.Lookup(call.Tool)
	if wantsStream(t, call.Args, m, a.io.OutTTY) {
		// --max-events and --for end a stream cleanly (exit 0)
		ctx, stop := context.WithCancelCause(ctx)
		defer stop(nil)
		if m.For > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeoutCause(ctx, m.For, errStreamBound)
			defer cancel()
		}
		first := true
		var werr error
		lastFrame := ""
		events := 0
		err := a.reg.RunStream(ctx, env, call, func(r *core.Result) {
			if werr != nil || (m.MaxEvents > 0 && events >= m.MaxEvents) {
				return
			}
			if !m.JSON && (m.Plain || !a.io.OutTTY) {
				// appended frames (no screen redraw): print only when
				// something other than the "as of" stamp changed
				key := frameKey(a.renderCard(env, r, m))
				if !first && key == lastFrame {
					return
				}
				lastFrame = key
			}
			werr = a.emit(env, r, m, !first)
			first = false
			events++
			if m.MaxEvents > 0 && events >= m.MaxEvents {
				stop(errStreamBound)
			}
		})
		if werr != nil {
			return werr
		}
		if err != nil && errors.Is(context.Cause(ctx), errStreamBound) {
			return nil
		}
		return err
	}
	res, err := a.reg.Invoke(ctx, env, call)
	if err != nil {
		return err
	}
	return a.emit(env, res, m, false)
}

// errStreamBound ends a stream at --max-events or --for.
var errStreamBound = errors.New("stream bound reached")

// emit writes one result: JSON envelope (one line, NDJSON-friendly) or a
// card; redraw clears the screen first for live TTY streams.
func (a *app) emit(env *core.Env, r *core.Result, m Mode, redraw bool) error {
	if m.JSON {
		return writeResult(a.io.Out, r, m)
	}
	out := a.renderCard(env, r, m)
	if redraw && !m.Plain {
		out = "\x1b[H\x1b[2J" + out
	}
	_, err := fmt.Fprintln(a.io.Out, strings.TrimRight(out, "\n"))
	return err
}

// renderCard renders r as the card emit prints. A card renderer that
// panics (a negative width at a narrow terminal, say) must not take the
// command down with it: the result is printed as JSON with a one-line
// notice instead.
func (a *app) renderCard(env *core.Env, r *core.Result, m Mode) (out string) {
	defer func() {
		if p := recover(); p != nil {
			var b strings.Builder
			fmt.Fprintf(&b, "hlp: could not draw the %s card at %d columns (%v); showing JSON\n", r.Card, a.width(), p)
			if err := WriteJSON(&b, r); err != nil {
				fmt.Fprintf(&b, "hlp: %v\n", err)
			}
			out = b.String()
		}
	}()
	shown := *r
	shown.Data = limitSlice(r.Data, m.Limit, r.Tail)
	if d, ok := shown.Data.(*core.ChartData); ok && d != nil && d.Height == 0 && a.io.OutTTY {
		if h := a.chartHeight(); h > 0 {
			cp := *d
			cp.Height = h
			shown.Data = &cp
		}
	}
	if r.Card == core.CardTicks {
		return a.renderTicks(env, &shown, m)
	}
	o := a.cardOpts(env, m)
	if r.Card == core.CardBook && a.io.OutTTY && o.Height == 0 {
		// The ladder fits the terminal so its title never scrolls off:
		// (rows-5)/2 levels a side (COMP #1 sized charts the same way).
		if rows := a.termRows(); rows > 0 {
			o.Height = max(rows-1, 8)
		}
	}
	return renderCardFn(&shown, o)
}

// renderCardFn is cards.Render (a test seam for the panic fallback).
var renderCardFn = cards.Render

// termRows is the terminal height (IO.Height, else $LINES; 0 = unknown).
func (a *app) termRows() int {
	if a.io.Height > 0 {
		return a.io.Height
	}
	if v, err := strconv.Atoi(a.io.Getenv("LINES")); err == nil {
		return v
	}
	return 0
}

// cardOpts are the CLI's card options: terminal width, local time with the
// zone named in footers (UTC with chart.utc), every row when piped.
func (a *app) cardOpts(env *core.Env, m Mode) cards.Opts {
	// An unset chart.theme means the shell's pairing (rush on its painted
	// ground); CLI output has no painted ground, so it keeps terminal.
	skin := env.Config.Chart.Theme
	if skin == "" {
		skin = chart.DefaultPalette
	}
	return cards.Opts{Width: a.width(), Theme: env.Theme, Profile: env.Profile, Plain: m.Plain,
		AllRows: !a.io.OutTTY || a.g.limit > 0, ChartSkin: skin, ChartStyle: chart.Style(env.Config.Chart.Style),
		Location: a.location(env), ZoneLabel: true}
}

// location is where CLI cards print times: UTC when config chart.utc is
// set, the local zone otherwise (the TUI does the same, PLAN D6).
func (a *app) location(env *core.Env) *time.Location {
	if env != nil && env.Config.Chart.UTC {
		return time.UTC
	}
	return time.Local
}

// chartHeight is the default chart height on a terminal: min(30, rows-4),
// so the chart, its header, context line and footer fit one screen.
func (a *app) chartHeight() int {
	rows := a.io.Height
	if rows <= 0 {
		if v, err := strconv.Atoi(a.io.Getenv("LINES")); err == nil {
			rows = v
		}
	}
	if rows <= 0 {
		return 0
	}
	return max(min(30, rows-4), 4)
}

// frameKey is a rendered frame minus its "as of" stamps, so a stream that
// only ticks the clock is not reprinted.
func frameKey(out string) string {
	return asOfRE.ReplaceAllString(out, "")
}

var asOfRE = regexp.MustCompile(`as of [^·\n]*`)

// runShell is `hlp` with no subcommand: the TUI on a terminal, or one line
// with -c (JSON when not on a terminal).
func (a *app) runShell(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	m, err := a.mode()
	if err != nil {
		return err
	}
	line := strings.TrimSpace(a.g.command)
	if line == "" && (!a.io.OutTTY || !a.io.InTTY) {
		return &core.Error{Code: core.CodeUsage, Message: "the interactive shell needs a terminal", Hint: "run a command (`hlp vaults`), use -c \"/vaults\", or `hlp --help`"}
	}
	if line != "" && (m.JSON || !a.io.InTTY) {
		call, err := a.parseLine(line)
		if err != nil {
			return err
		}
		return a.execute(ctx, call, m)
	}
	feed := tui.NewWSStateFeed()
	a.onWS = feed.OnState
	env, err := a.env(ctx)
	if err != nil {
		return err
	}
	defer env.Close()
	env.Confirm = nil // the TUI installs its own confirm card
	return tui.Run(ctx, tui.Options{Env: env, Registry: a.reg, Agent: a.buildAgent(ctx, env), Initial: line, WSFeed: feed})
}

// parseLine handles "/slash args" and free text through the intent parser.
func (a *app) parseLine(line string) (core.Call, error) {
	if strings.HasPrefix(line, "/") {
		return a.reg.ParseLine(line)
	}
	if call, ok := agent.ParseIntent(line, a.reg); ok {
		return call, nil
	}
	if call, err := a.reg.ParseLine(line); err == nil {
		return call, nil
	}
	hint := "use a slash command like \"/chart btc 4h\" or \"/vaults --sort sharpe\""
	if sug, ok := agent.Suggest(line, a.reg); ok {
		hint = "closest command: " + sug + " (configure [llm] to ask questions in plain words)"
	}
	return core.Call{}, &core.Error{Code: core.CodeUsage, Message: "could not understand " + strconv.Quote(line), Hint: hint}
}

// buildAgent returns the chat agent when a model is configured. Provider
// "auto" probes local servers (Ollama, LM Studio); "" means no LLM.
func (a *app) buildAgent(ctx context.Context, env *core.Env) *agent.Agent {
	var p llm.Provider
	switch strings.ToLower(env.Config.LLM.Provider) {
	case "":
		return nil
	case "auto":
		p = llm.Detect(ctx)
	default:
		var err error
		p, err = llm.New(env.Config.LLM, env.Secrets)
		if err != nil {
			fmt.Fprintln(a.io.Err, "hlp: LLM disabled: "+core.AsError(err).Message)
			return nil
		}
	}
	if p == nil {
		return nil
	}
	return &agent.Agent{Provider: p, Registry: a.reg, Env: env, MaxSteps: 8, System: agent.SystemPrompt(env)}
}

func (a *app) mcpCmd() *cobra.Command {
	var allow, noApps bool
	cmd := &cobra.Command{
		Use:     "mcp",
		Short:   "Serve hlp tools over MCP (stdio) for Claude Code, Cursor, Codex",
		Long:    "Serve every read-only hlp tool over the Model Context Protocol on stdio.\n\n  claude mcp add hlp -- hlp mcp\n\nTrading tools appear only with config mcp.allow_trading = true AND --allow-trading,\nand they stay dry-run in this build.",
		GroupID: "meta",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := a.env(cmd.Context())
			if err != nil {
				return err
			}
			defer env.Close()
			env.Confirm = nil
			return mcp.Serve(cmd.Context(), env, a.reg, mcp.Options{Version: a.bi.Version, AllowTrading: allow, NoApps: noApps})
		},
	}
	cmd.Flags().BoolVar(&allow, "allow-trading", false, "Expose the dry-run trading tools (also needs mcp.allow_trading in config)")
	cmd.Flags().BoolVar(&noApps, "no-apps", false, "Do not serve MCP Apps views (ui:// chart, vault and order views; confirm_order)")
	return cmd
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Print version, commit and build date",
		GroupID: "meta",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			m, err := a.mode()
			if err != nil {
				return err
			}
			v := map[string]string{"version": orDefault(a.bi.Version, "dev"), "commit": orDefault(a.bi.Commit, "none"), "date": orDefault(a.bi.Date, "unknown")}
			if m.JSON {
				return writeResult(a.io.Out, &core.Result{Data: v, Meta: domain.Meta{Source: "hlp", AsOf: time.Now().UTC()}}, m)
			}
			_, err = fmt.Fprintf(a.io.Out, "hlp %s (%s, %s)\n", v["version"], v["commit"], v["date"])
			return err
		},
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ConfigView is `hlp config` output: where the file is and what is in
// effect, with secrets reported only as set/unset.
type ConfigView struct {
	Path    string            `json:"path"`
	Exists  bool              `json:"exists"`
	Paths   map[string]string `json:"paths"`
	Config  map[string]any    `json:"config"`
	Secrets map[string]string `json:"secrets"`
}

func (a *app) configCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "config",
		Short:   "Show the config path and the effective config (secrets redacted)",
		GroupID: "meta",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := a.mode()
			if err != nil {
				return err
			}
			env, err := a.env(cmd.Context())
			if err != nil {
				return err
			}
			defer env.Close()
			v := ConfigView{
				Path:    env.Paths.ConfigFile,
				Paths:   map[string]string{"cache": env.Paths.CacheDir, "data": env.Paths.DataDir},
				Secrets: map[string]string{},
			}
			_, statErr := os.Stat(env.Paths.ConfigFile)
			v.Exists = statErr == nil
			b, err := toml.Marshal(env.Config)
			if err != nil {
				return err
			}
			if err := toml.Unmarshal(b, &v.Config); err != nil {
				return err
			}
			for _, name := range []string{config.SecretAgentKey, config.SecretAnthropicKey, config.SecretOpenAIKey} {
				s, err := env.Secrets.Get(name)
				switch {
				case err == nil && !s.Empty():
					v.Secrets[name] = s.String() // "<redacted>"
				default:
					v.Secrets[name] = "<unset>"
				}
			}
			if m.JSON {
				return writeResult(a.io.Out, &core.Result{Data: v, Meta: domain.Meta{Source: "config"}}, m)
			}
			exists := "not created yet (defaults in effect)"
			if v.Exists {
				exists = "found"
			}
			fmt.Fprintf(a.io.Out, "# %s (%s)\n# cache %s\n# data  %s\n", v.Path, exists, v.Paths["cache"], v.Paths["data"])
			for _, name := range []string{config.SecretAgentKey, config.SecretAnthropicKey, config.SecretOpenAIKey} {
				fmt.Fprintf(a.io.Out, "# secret %-14s %s\n", name, v.Secrets[name])
			}
			_, err = fmt.Fprintf(a.io.Out, "\n%s", b)
			return err
		},
	}
}

// trimWriter drops trailing spaces and tabs from every line it passes on.
type trimWriter struct {
	w   io.Writer
	buf []byte
}

func (t *trimWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimRight(t.buf[:i], " \t")
		if _, err := t.w.Write(append(line, '\n')); err != nil {
			return len(p), err
		}
		t.buf = t.buf[i+1:]
	}
	return len(p), nil
}

// Flush writes a final unterminated line.
func (t *trimWriter) Flush() {
	if len(t.buf) > 0 {
		_, _ = t.w.Write(bytes.TrimRight(t.buf, " \t"))
		t.buf = nil
	}
}
