package tui

import (
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"

	"github.com/maxmoneycash/hlp-cli/internal/core"
)

// command is one entry of the slash autocomplete and the command palette:
// a registry tool or one of the shell's local commands.
type command struct {
	slash   string   // without "/"
	aliases []string // also accepted
	args    string   // inline hint: "<coin> <interval>"
	summary string
	group   string
	tool    string // registry tool name ("" for local commands)
	// needsArgs is true when a positional argument is required by the
	// tool's input schema (so Enter fills "/name " instead of running).
	needsArgs bool
}

// localCommands are handled by the shell itself.
var localCommands = []command{
	{slash: "help", summary: "Keys and commands", group: "shell"},
	{slash: "clear", summary: "Clear the transcript", group: "shell"},
	{slash: "theme", args: "<name>", summary: "Switch theme or chart theme (terminal, paper, neon...); no name lists them", group: "shell"},
	{slash: "candles", args: "<style>", summary: "Candle style: classic, solid, hollow, ohlc, line, heikin...", group: "shell"},
	{slash: "dash", args: "<name> [coin]", summary: "Dashboards: markets, vaults, hlp, strategies, research (tab cycles)", group: "shell"},
	{slash: "open", summary: "Open the latest card full screen (ctrl+o)", group: "shell"},
	{slash: "quit", aliases: []string{"exit", "q"}, summary: "Leave hlp", group: "shell"},
}

// commandsFrom builds the command list from the registry (slash names and
// aliases) plus the local commands, sorted by name.
func commandsFrom(reg *core.Registry) []command {
	var out []command
	if reg != nil {
		for _, t := range reg.All() {
			if t.Slash == "" {
				continue
			}
			c := command{slash: t.Slash, aliases: t.Aliases, summary: t.Summary, group: t.Group, tool: t.Name}
			pos := t.Positional
			if t.Name == "place_order" {
				// the slash word (/buy, /sell, /long, /short) is the side
				pos = withoutSide(pos)
				c.aliases = nil
				c.summary = "Buy or go long (dry-run unless trading is enabled; risk caps apply)"
			}
			if len(pos) > 0 {
				c.args = positionalHint(t, pos)
				c.needsArgs = requiresPositional(t)
			}
			out = append(out, c)
			if t.Name == "place_order" {
				sell := c
				sell.slash, sell.summary = "sell", "Sell or go short (dry-run unless trading is enabled; risk caps apply)"
				out = append(out, sell)
			}
		}
	}
	out = append(out, localCommands...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].slash < out[j].slash })
	return out
}

// requiresPositional reports whether any positional field is required by
// the tool's input schema (no omitempty). Unknown schemas count as required.
func withoutSide(pos []string) []string {
	var out []string
	for _, p := range pos {
		if p != "side" {
			out = append(out, p)
		}
	}
	return out
}

// positionalHint renders positionals as "<required> [optional]" from the
// tool's input schema.
func positionalHint(t core.Tool, pos []string) string {
	req := map[string]bool{}
	if t.NewInput != nil {
		if sch, err := t.InputSchema(); err == nil && sch != nil {
			for _, r := range sch.Required {
				req[r] = true
			}
		} else {
			for _, p := range pos {
				req[p] = true
			}
		}
	}
	parts := make([]string, len(pos))
	for i, p := range pos {
		if req[p] {
			parts[i] = "<" + p + ">"
		} else {
			parts[i] = "[" + p + "]"
		}
	}
	return strings.Join(parts, " ")
}

// cutWords shortens s to at most width cells at a word boundary, with an
// ellipsis, so summaries never end mid-word ("all de…").
func cutWords(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	r := []rune(s)
	cut := min(len(r), max(width-1, 0))
	for i := cut; i > 0; i-- {
		if r[i-1] == ' ' && i-1 > 0 {
			return strings.TrimRight(string(r[:i-1]), " ,;:·") + "…"
		}
	}
	return string(r[:cut]) + "…"
}

func requiresPositional(t core.Tool) bool {
	if t.NewInput == nil {
		return false
	}
	sch, err := t.InputSchema()
	if err != nil || sch == nil {
		return true
	}
	req := map[string]bool{}
	for _, r := range sch.Required {
		req[r] = true
	}
	for _, p := range t.Positional {
		if req[p] {
			return true
		}
	}
	return false
}

// matchCommands ranks commands for a partial name: exact name or alias
// first, then prefix matches (shortest first, so "/va" offers vault before
// vaults), then fuzzy matches on names, aliases and summaries.
func matchCommands(cmds []command, q string) []command {
	q = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q), "/"))
	if q == "" {
		return cmds
	}
	type scored struct {
		c     command
		score int
	}
	seen := map[string]bool{}
	var ranked []scored
	add := func(c command, s int) {
		if seen[c.slash] {
			return
		}
		seen[c.slash] = true
		ranked = append(ranked, scored{c, s})
	}
	for _, c := range cmds {
		if c.slash == q {
			add(c, 1_000_000)
		}
		for _, a := range c.aliases {
			if a == q {
				add(c, 900_000)
			}
		}
	}
	// Prefix matches on the command's own name rank above prefix matches
	// on an alias ("/f": /fills, /fund… before /close via its alias).
	var prefix, aliasPrefix []command
	for _, c := range cmds {
		if strings.HasPrefix(c.slash, q) {
			prefix = append(prefix, c)
			continue
		}
		for _, a := range c.aliases {
			if strings.HasPrefix(a, q) {
				aliasPrefix = append(aliasPrefix, c)
				break
			}
		}
	}
	byLen := func(xs []command) {
		sort.SliceStable(xs, func(i, j int) bool {
			if len(xs[i].slash) != len(xs[j].slash) {
				return len(xs[i].slash) < len(xs[j].slash)
			}
			return xs[i].slash < xs[j].slash
		})
	}
	byLen(prefix)
	byLen(aliasPrefix)
	for i, c := range append(prefix, aliasPrefix...) {
		add(c, 500_000-i)
	}
	// Fuzzy over names and aliases only (summaries are too long to fuzz:
	// every letter appears somewhere). Summaries match by whole substring
	// once the query is long enough to mean something ("/sharpe").
	hay := make([]string, len(cmds))
	for i, c := range cmds {
		hay[i] = strings.ToLower(c.slash + " " + strings.Join(c.aliases, " "))
	}
	for _, m := range fuzzy.Find(q, hay) {
		add(cmds[m.Index], m.Score)
	}
	if len(q) >= 4 {
		for _, c := range cmds {
			if strings.Contains(strings.ToLower(c.summary), q) {
				add(c, -1)
			}
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	out := make([]command, len(ranked))
	for i, r := range ranked {
		out[i] = r.c
	}
	return out
}

// isPrefixMatch reports whether typed is a prefix of the command's name or
// an alias (Enter only auto-completes those; fuzzy guesses need tab).
func isPrefixMatch(c command, typed string) bool {
	typed = strings.ToLower(typed)
	if strings.HasPrefix(c.slash, typed) {
		return true
	}
	for _, a := range c.aliases {
		if strings.HasPrefix(a, typed) {
			return true
		}
	}
	return false
}

// findCommand resolves a typed slash name (exact name or alias).
func findCommand(cmds []command, name string) (command, bool) {
	name = strings.ToLower(name)
	for _, c := range cmds {
		if c.slash == name {
			return c, true
		}
		for _, a := range c.aliases {
			if a == name {
				return c, true
			}
		}
	}
	return command{}, false
}

// splitArgs splits a command line like a shell: whitespace separated,
// with "double" or 'single' quotes grouping words.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	in := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if in || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// argHint returns the positional hints not yet typed, for ghost text after
// the cursor: "/chart " -> "<coin> <interval>", "/chart BTC " -> "<interval>".
// Nothing is hinted while a word is still being typed.
func argHint(c command, typedArgs int, trailingSpace bool) string {
	if c.args == "" || !trailingSpace {
		return ""
	}
	parts := strings.Fields(c.args)
	if typedArgs >= len(parts) {
		return ""
	}
	return strings.Join(parts[typedArgs:], " ")
}

// suggestCommands returns likely intended commands for a mistyped name:
// close spellings first (optimal string alignment distance, so "opne"
// finds "open"), then prefix and fuzzy matches.
func suggestCommands(cmds []command, name string) []command {
	name = strings.ToLower(name)
	type scored struct {
		c command
		d int
	}
	var close []scored
	limit := max(1, len(name)/3+1)
	for _, c := range cmds {
		best := osaDistance(name, c.slash)
		for _, a := range c.aliases {
			best = min(best, osaDistance(name, a))
		}
		if best <= limit {
			close = append(close, scored{c, best})
		}
	}
	sort.SliceStable(close, func(i, j int) bool { return close[i].d < close[j].d })
	seen := map[string]bool{}
	var out []command
	for _, s := range close {
		seen[s.c.slash] = true
		out = append(out, s.c)
	}
	for _, c := range matchCommands(cmds, name) {
		if !seen[c.slash] {
			seen[c.slash] = true
			out = append(out, c)
		}
	}
	return out
}

// osaDistance is the optimal string alignment edit distance (insert,
// delete, substitute, swap adjacent).
func osaDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}

// tagCompletion completes a curated-registry tag being typed as the last
// word of "/discover <tag>", "/tags <tag>" or "/vaults ... --tag <tag>"
// (also --tag=<tag> and --not-tag). It returns the full line with the tag
// completed, the untyped remainder for a ghost hint, and ok.
func tagCompletion(env *core.Env, line string) (string, string, bool) {
	if env == nil || env.Vaults == nil || !strings.HasPrefix(line, "/") || strings.HasSuffix(line, " ") {
		return "", "", false
	}
	f := strings.Fields(strings.TrimPrefix(line, "/"))
	if len(f) < 2 {
		return "", "", false
	}
	head, last, prev := strings.ToLower(f[0]), f[len(f)-1], strings.ToLower(f[len(f)-2])
	prefix := ""
	switch {
	case strings.HasPrefix(last, "--tag="), strings.HasPrefix(last, "--not-tag="):
		prefix = last[strings.Index(last, "=")+1:]
	case prev == "--tag" || prev == "--not-tag":
		prefix = last
	case (head == "discover" || head == "tags") && !strings.HasPrefix(last, "-"):
		prefix = last
	default:
		return "", "", false
	}
	if prefix == "" {
		return "", "", false
	}
	for _, t := range env.Vaults.SuggestTags(strings.ToLower(prefix)) {
		if strings.HasPrefix(t, strings.ToLower(prefix)) && t != strings.ToLower(prefix) {
			rest := t[len(prefix):]
			return line + rest, rest, true
		}
	}
	return "", "", false
}

// captions are the autocomplete and palette captions: four words or fewer
// (DESIGN.md 5.1 rule 7). A command without one falls back to the head of
// its summary.
var captions = map[string]string{
	"account": "equity and positions", "arena": "head to head", "asset": "asset id",
	"backtest": "backtest a strategy", "book": "order book", "buy": "buy or long", "sell": "sell or short",
	"cancel": "cancel orders", "chart": "candles", "close": "close a position", "compare": "compare vaults",
	"deposit": "deposit to a vault", "discover": "curated vaults", "fills": "recent fills",
	"fund": "NAV and epochs", "fund-epochs": "settled epochs", "fund-user": "depositor position",
	"funding": "funding rates", "hlp": "HLP positioning", "index": "vault indexes", "markets": "markets",
	"paper": "paper trade", "pine": "pine scripts", "preview": "order risk check", "price": "price",
	"schema": "tool schemas", "status": "health check", "strat": "quant strategy", "trades": "recent trades",
	"vault": "tearsheet", "vaults": "screener", "watch": "follow live", "withdraw": "withdraw from a vault",
	"help": "keys and commands", "clear": "clear the transcript", "theme": "switch theme",
	"candles": "candle style", "open": "open the latest card", "quit": "leave",
}

// caption is c's caption of four words or fewer.
func caption(c command) string {
	if s, ok := captions[c.slash]; ok {
		return s
	}
	s := c.summary
	if i := strings.IndexAny(s, ":;,(.\u2014\u2013"); i > 0 {
		s = s[:i]
	}
	f := strings.Fields(s)
	if len(f) > 4 {
		f = f[:4]
	}
	s = strings.Join(f, " ")
	if s != "" {
		s = strings.ToLower(s[:1]) + s[1:]
	}
	return s
}
