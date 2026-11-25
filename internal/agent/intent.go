package agent

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
)

// ParseIntent is the deterministic, LLM-free free-text parser that makes the
// chat shell useful with no model configured. It maps phrases to tool calls:
//
//	"chart btc 4h"              -> get_candles {coin:BTC, interval:4h}
//	"btc"                       -> get_candles {coin:BTC}
//	"how's hype"                -> get_candles {coin:HYPE} (+ funding summary)
//	"btc price", "price eth sol" -> get_price {coins:[BTC]} / {coins:[ETH,SOL]}
//	"book eth"                  -> get_orderbook {coin:ETH}
//	"top vaults by sharpe"      -> list_vaults {sort:sharpe}
//	"vaults under 20% drawdown" -> list_vaults {max-dd:20}
//	"vault growi"               -> get_vault {vault:"growi"}
//	"compare hlp growi"         -> compare_vaults {vaults:[hlp,growi]}
//	"funding"                   -> get_funding {}
//	"watch 0xabc..."            -> watch {address:0xabc...}
//	"buy $100 hype"             -> place_order {side:buy, coin:HYPE, usd:100, dry-run:true}
//	"close 50% btc", "nuke"     -> place_order / close_all previews (dry-run, double confirm)
//
// ok=false means "not understood": the TUI then hands the text to the LLM if
// one is configured, otherwise shows suggestions.
//
// Every call it returns names a tool present in r (core.Default() when r is
// nil) and its args decode strictly into that tool's input struct. Order,
// cancel, deposit and withdraw phrases always carry "dryRun": true and never
// "yes". Close phrases map to the "close_position" tool when the registry
// provides it.
func ParseIntent(text string, r *core.Registry) (call core.Call, ok bool) {
	return ParseIntentWith(text, r, IntentOptions{})
}

// IntentOptions tune ParseIntentWith.
type IntentOptions struct {
	// IsCoin reports whether s (as typed) names a listed market. The TUI
	// passes the hyperliquid Registry's Resolve. It is consulted only for
	// bare phrases ("pengu", "kaito 4h"); keyword phrases ("chart foo")
	// accept any coin-shaped token and let the tool report unknown coins.
	IsCoin func(s string) bool
}

// CloseTool is the tool name close phrases map to (core's close_position,
// slash /close, CLI `hlp close`).
const CloseTool = "close_position"

// ParseIntentWith is ParseIntent with options.
func ParseIntentWith(text string, r *core.Registry, o IntentOptions) (core.Call, bool) {
	if r == nil {
		r = core.Default()
	}
	p := &intentParser{opts: o}
	tool, args, ok := p.parse(text)
	if !ok {
		return core.Call{}, false
	}
	return callFor(r, tool, args)
}

func callFor(r *core.Registry, tool string, args map[string]any) (core.Call, bool) {
	if _, exists := r.Get(tool); !exists {
		return core.Call{}, false
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return core.Call{}, false
	}
	return core.Call{Tool: tool, Args: raw}, true
}

type tok struct {
	o string // as typed (outer punctuation trimmed)
	w string // lower-case normalized
}

type intentParser struct {
	opts IntentOptions
	// loose skips the free-form-question gates: Suggest uses it to find the
	// closest command for a question the strict parser hands to the model.
	loose bool
}

// questionLeads open a free-form question or request for advice. The parser
// never answers those deterministically: "which vaults have the best
// risk-adjusted returns?" goes to the model (or, with none, to Suggest).
// "what"/"how" are fillers, so "what's btc doing?" still charts.
var questionLeads = set("which", "why", "where", "who", "whom", "when", "should", "shall", "explain", "recommend",
	"suggest", "advise", "describe", "summarize", "summarise", "analyze", "analyse")

// proseWords never appear in a vault name query; their presence means the
// leftover words are a sentence, not a name.
var proseWords = set("have", "has", "had", "which", "why", "where", "who", "when", "should", "would", "will",
	"risk", "risky", "risk-adjusted", "adjusted", "good", "safe", "safest", "safer", "steady", "shallow", "park",
	"returns", "return", "invest", "investment", "worth", "better", "worse", "recommend", "ones",
	"been", "being", "they", "them", "their", "it", "its", "if", "or", "but", "so", "because")

var (
	reAddr      = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	reAddrish   = regexp.MustCompile(`^0x[0-9a-f]{3,}(…|\.\.\.)?$`)
	reCoin      = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$|^[0-9]+[a-z][a-z0-9]{0,14}$`)
	reDexCoin   = regexp.MustCompile(`^[a-z][a-z0-9]{0,9}:[a-z0-9]{1,15}$`)
	reSpotIdx   = regexp.MustCompile(`^@[0-9]{1,5}$`)
	rePair      = regexp.MustCompile(`^[a-z0-9]{1,15}/[a-z0-9]{1,10}$`)
	reMoney     = regexp.MustCompile(`^(\$)?([0-9]+(?:\.[0-9]+)?|\.[0-9]+)(k|m|mm|b)?(\$|usd|usdc|dollars?)?$`)
	rePercent   = regexp.MustCompile(`^([+-]?[0-9]+(?:\.[0-9]+)?)%$`)
	reLeverage  = regexp.MustCompile(`^(?:x([0-9]{1,3})|([0-9]{1,3})x)$`)
	reDuration  = regexp.MustCompile(`^([0-9]+)(m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days|w|wk|wks|week|weeks|mo|mon|month|months|y|yr|yrs|year|years)$`)
	reFile      = regexp.MustCompile(`(?i)\.(pine|ya?ml|json)$`)
	reIntervalW = map[string]domain.Interval{
		"hourly": domain.I1h, "daily": domain.I1d, "weekly": domain.I1w, "monthly": domain.I1M,
		"1min": domain.I1m, "3min": domain.I3m, "5min": domain.I5m, "15min": domain.I15m, "30min": domain.I30m,
		"60m": domain.I1h, "1hr": domain.I1h, "2hr": domain.I2h, "4hr": domain.I4h, "8hr": domain.I8h, "12hr": domain.I12h,
		"1hour": domain.I1h, "4hour": domain.I4h, "1day": domain.I1d, "3day": domain.I3d, "1week": domain.I1w,
		"1mo": domain.I1M, "1mon": domain.I1M, "1month": domain.I1M,
	}
)

// fillers are dropped when looking for the "content" of a phrase.
var fillers = set("please", "pls", "plz", "hey", "hi", "ok", "okay", "yo", "can", "could", "would", "you", "show", "me",
	"give", "get", "display", "open", "pull", "up", "the", "a", "an", "what", "whats", "hows", "how", "is", "are",
	"was", "doing", "looking", "look", "like", "now", "today", "right", "currently", "current", "of", "for", "on",
	"in", "at", "to", "i", "want", "see", "let", "lets", "us", "quick", "just", "some", "tell", "about", "there",
	"going", "do", "does", "this", "that", "with", "and", "any", "all", "info", "data", "stats", "latest")

// keywords that are never coins.
var keywords = set("chart", "charts", "candles", "candle", "price", "prices", "graph", "book", "orderbook", "ob", "depth",
	"l2", "bids", "asks", "trades", "tape", "prints", "funding", "fundings", "rates", "rate", "vault", "vaults",
	"compare", "vs", "versus", "watch", "spectate", "follow", "track", "buy", "sell", "long", "short", "close",
	"nuke", "flatten", "cancel", "deposit", "withdraw", "markets", "market", "account", "positions", "position",
	"pos", "portfolio", "balance", "equity", "fills", "fill", "history", "my", "mine", "hlp", "index", "indexes",
	"indices", "fund", "pvault", "discover", "tags", "theses", "strat", "strategy", "quant", "backtest", "bt", "pine", "paper", "arena", "schema",
	"asset", "id", "assetid", "top", "best", "by", "sorted", "sort", "highest", "lowest", "most", "biggest",
	"largest", "oldest", "under", "below", "over", "above", "than", "less", "more", "max", "min", "tvl", "apr",
	"sharpe", "drawdown", "dd", "age", "open", "curated", "usd", "usdc", "dollars", "worth", "limit", "tp", "sl",
	"stop", "loss", "take", "profit", "leverage", "lev", "reduce", "only", "into", "from", "ma", "ema", "bars",
	"heikin", "lines", "blocks", "ascii", "mono", "half", "hollow", "solid", "ohlc", "classic", "help", "hello", "thanks", "thank", "yes", "no", "gm",
	"gn", "lol", "status", "list", "start", "run", "check", "levels", "doing", "spot", "perps", "perp", "hip3",
	"gainers", "losers", "movers", "named", "called", "matching", "screener", "tearsheet", "vet", "older", "old",
	"days", "day", "weeks", "months", "years", "half", "everything", "every", "positioning", "exposure", "net",
	"holding", "holdings", "order", "orders", "last", "past", "since", "capital", "x", "px", "at", "quote", "quotes",
	"mid", "mids")

// knownCoins are accepted as bare one-word phrases ("btc", "hype 4h")
// without a registry. Others need IsCoin or a keyword ("chart pengu").
var knownCoins = set("btc", "eth", "sol", "hype", "doge", "xrp", "bnb", "ada", "avax", "link", "sui", "trx", "ton",
	"dot", "ltc", "bch", "near", "apt", "arb", "op", "tia", "sei", "inj", "pepe", "kpepe", "wif", "bonk", "kbonk",
	"fartcoin", "trump", "pump", "ena", "ondo", "aave", "uni", "mkr", "crv", "ldo", "jup", "pendle", "tao",
	"render", "fet", "wld", "popcat", "pengu", "virtual", "zro", "strk", "bera", "kaito", "purr", "hfun", "xmr",
	"zec", "spx", "spy", "sp500", "nvda", "tsla", "aapl", "msft", "amzn", "googl", "meta", "coin", "hood", "mstr",
	"gold", "silver", "xyz:sp500")

// strategyIDs are hlp.xyz quant strategy ids.
var strategyIDs = set("trend", "spxmeme", "bluechip", "dilution", "lotto", "memeshort")

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// tokenize splits text into tokens, trimming outer punctuation and joining
// "<number> <unit>" pairs ("4 hours" -> "4hours", "90 days" -> "90days").
func tokenize(text string) []tok {
	text = strings.NewReplacer("’", "'", "‘", "'", "“", `"`, "”", `"`, " ", " ").Replace(text)
	var out []tok
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == ';' }) {
		o := strings.Trim(f, `?!.;"()[]{}…`)
		if strings.HasPrefix(strings.ToLower(f), "0x") && (strings.HasSuffix(f, "...") || strings.HasSuffix(f, "…")) {
			o = strings.TrimRight(f, `?!;"()[]{}`) // "0xabc..." keeps its ellipsis
		}
		if o == "" {
			continue
		}
		w := strings.ToLower(o)
		w = strings.ReplaceAll(w, "'s", "s")
		w = strings.ReplaceAll(w, "'", "")
		out = append(out, tok{o: o, w: w})
	}
	// join number + unit
	var joined []tok
	for i := 0; i < len(out); i++ {
		if i+1 < len(out) && isInt(out[i].w) && isUnit(out[i+1].w) {
			joined = append(joined, tok{o: out[i].o + out[i+1].o, w: out[i].w + out[i+1].w})
			i++
			continue
		}
		joined = append(joined, out[i])
	}
	return joined
}

func isInt(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

var units = set("m", "min", "mins", "minute", "minutes", "h", "hr", "hrs", "hour", "hours", "d", "day", "days",
	"w", "wk", "wks", "week", "weeks", "mo", "mon", "month", "months", "y", "yr", "yrs", "year", "years")

func isUnit(s string) bool { return units[s] }

func (p *intentParser) parse(text string) (string, map[string]any, bool) {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "/") {
		return "", nil, false
	}
	ts := tokenize(text)
	// Drop leading fillers ("please show me the ...").
	for len(ts) > 0 && fillers[ts[0].w] && !(ts[0].w == "open" && len(ts) > 1 && ts[1].w == "vaults") {
		ts = ts[1:]
	}
	if len(ts) == 0 {
		return "", nil, false
	}
	first := ts[0].w
	if !p.loose && questionLeads[first] {
		return "", nil, false
	}
	has := func(ws ...string) bool {
		for _, t := range ts {
			for _, w := range ws {
				if t.w == w {
					return true
				}
			}
		}
		return false
	}

	// Strategy files and theses come first: they contain words like
	// "long"/"short" that would otherwise read as orders.
	switch first {
	case "backtest", "bt":
		return p.backtest(ts[1:], text)
	case "pine":
		return p.pine(ts[1:])
	case "paper":
		return p.paper(ts[1:])
	case "arena":
		return p.arena(ts[1:])
	case "schema", "schemas":
		args := map[string]any{}
		if len(ts) > 1 {
			args["tool"] = ts[1].w
		}
		return "schema", args, true
	}
	if first == "check" && len(ts) > 1 && reFile.MatchString(ts[1].o) {
		return "pine_check", map[string]any{"file": ts[1].o}, true
	}
	if len(ts) == 1 && strings.HasSuffix(first, ".pine") {
		return "pine_check", map[string]any{"file": ts[0].o}, true
	}
	if (first == "long" || first == "short") && (has("short") && has("long") || has("rebalance")) {
		return "run_backtest", map[string]any{"thesis": thesisText(text)}, true
	}

	// Trading phrases (always dry-run previews).
	switch first {
	case "nuke", "flatten", "panic":
		return CloseTool, map[string]any{"all": true, "dryRun": true}, true
	case "close", "exit":
		return p.close(ts[1:])
	case "buy", "sell", "long", "short", "ape":
		return p.order(first, ts[1:])
	case "cancel":
		return p.cancel(ts[1:])
	case "deposit", "withdraw":
		return p.transfer(first, ts[1:])
	}

	switch first {
	case "watch", "spectate", "follow", "track", "stalk":
		rest := ts[1:]
		if len(rest) == 0 {
			return "", nil, false
		}
		return "watch", map[string]any{"address": addressOrName(rest)}, true
	case "compare":
		return p.compare(ts[1:])
	case "index", "indexes", "indices":
		args := map[string]any{}
		if len(ts) > 1 {
			args["slug"] = ts[1].w
		}
		return "list_indexes", args, true
	case "fund", "pvault":
		rest := ts[1:]
		if len(rest) > 0 && (rest[0].w == "epochs" || rest[0].w == "epoch" || rest[0].w == "history") {
			args := map[string]any{}
			if len(rest) > 1 {
				args["vault"] = rest[1].o
			}
			return "get_strategy_vault_epochs", args, true
		}
		if len(rest) > 1 && (rest[0].w == "user" || rest[0].w == "depositor") && reAddr.MatchString(rest[1].w) {
			return "get_strategy_vault_user", map[string]any{"user": rest[1].o}, true
		}
		args := map[string]any{}
		if len(rest) > 0 {
			args["vault"] = rest[0].o
		}
		return "get_strategy_vault", args, true
	case "discover", "tags", "theses":
		args := map[string]any{}
		var tags []string
		for _, t := range ts[1:] {
			if t.w != "vaults" && t.w != "vault" {
				tags = append(tags, t.w)
			}
		}
		if len(tags) > 0 {
			args["tag"] = tags
		}
		return "discover_vaults", args, true
	case "strat", "strategy", "quant":
		if len(ts) > 1 && reCoin.MatchString(ts[1].w) {
			return "get_strategy", map[string]any{"id": ts[1].w}, true
		}
		return "", nil, false
	}
	if len(ts) == 1 && strategyIDs[first] && first != "trend" {
		return "get_strategy", map[string]any{"id": first}, true
	}

	// A bare address is an account lookup.
	if len(ts) == 1 && reAddr.MatchString(first) {
		return "get_account", map[string]any{"address": ts[0].o}, true
	}

	// HLP positioning ("what is hlp long", "hlp").
	if has("hlp") && !has("vault", "vaults", "compare", "vs", "versus", "deposit", "withdraw", "screener") {
		rest := content(ts, "hlp")
		onlyPositioning := true
		for _, t := range rest {
			switch t.w {
			case "long", "short", "positioning", "positions", "position", "exposure", "net", "book", "holding",
				"holdings", "pos", "longs", "shorts", "bias":
			default:
				onlyPositioning = false
			}
		}
		if onlyPositioning {
			return "hlp_positioning", map[string]any{}, true
		}
	}

	// "a vs b" with vault words is a comparison.
	if has("vs", "versus") && has("vault", "vaults") {
		return p.compare(withoutWords(ts, "vault", "vaults"))
	}

	// Account and fills for an address or the configured account.
	if has("fills", "fill") || (has("trades", "history") && has("my", "mine")) || (has("trades") && firstAddr(ts) != "") {
		return p.fills(ts)
	}
	if has("account", "positions", "position", "pos", "portfolio", "balance", "equity", "pnl") {
		args := map[string]any{}
		if a := firstAddr(ts); a != "" {
			args["address"] = a
		}
		return "get_account", args, true
	}

	// Vault screener and tearsheet.
	if has("vaults", "screener") || (has("vault") && has("top", "best", "highest", "biggest", "largest", "oldest", "sorted") && !has("compare")) {
		return p.vaults(ts)
	}
	if first == "vault" || first == "tearsheet" || first == "vet" {
		rest := ts[1:]
		if len(rest) > 0 && (rest[0].w == "for" || rest[0].w == "of" || rest[0].w == "on") {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return "", nil, false
		}
		return "get_vault", map[string]any{"vault": addressOrName(rest)}, true
	}
	if n := len(ts); n >= 2 && ts[n-1].w == "vault" {
		name := ts[:n-1]
		if len(name) > 0 && name[0].w == "the" {
			name = name[1:]
		}
		if len(name) > 0 {
			return "get_vault", map[string]any{"vault": addressOrName(name)}, true
		}
	}
	if has("due") && has("diligence") {
		rest := withoutWords(ts, "due", "diligence", "on", "for", "of", "do", "run")
		if len(rest) > 0 {
			return "get_vault", map[string]any{"vault": addressOrName(rest)}, true
		}
	}

	// Markets.
	if has("markets", "market", "gainers", "losers", "movers") || (first == "top" && len(ts) <= 3 && has("volume", "oi")) {
		return p.markets(ts)
	}
	if has("funding", "fundings") {
		return p.funding(ts)
	}
	if has("book", "orderbook", "ob", "depth", "l2", "bids", "asks") {
		return p.coinTool("get_orderbook", ts, func(args map[string]any, t tok) bool {
			if n, err := strconv.Atoi(t.w); err == nil && n > 0 && n <= 20 {
				args["depth"] = n
				return true
			}
			if strings.HasSuffix(t.w, "levels") {
				if n, err := strconv.Atoi(strings.TrimSuffix(t.w, "levels")); err == nil && n > 0 && n <= 20 {
					args["depth"] = n
					return true
				}
			}
			return false
		})
	}
	if has("trades", "tape", "prints") {
		return p.coinTool("get_trades", ts, nil)
	}
	if has("asset", "assetid", "asset-id") || (has("id") && len(ts) <= 4) {
		return p.coinTool("get_asset_id", ts, nil)
	}
	// "btc price", "price of eth and sol", "quote hype": a one-line quote.
	// Anything chart-shaped (chart, candles, graph, an interval, a style,
	// MAs) stays on get_candles.
	if has("price", "prices", "quote", "quotes", "mid", "mids") && !p.chartish(ts) {
		if tool, args, ok := p.price(ts); ok {
			return tool, args, ok
		}
	}
	return p.chart(ts)
}

// chartish reports whether a phrase asks for a picture rather than a number.
func (p *intentParser) chartish(ts []tok) bool {
	for _, t := range ts {
		switch t.w {
		case "chart", "charts", "graph", "plot", "candles", "candle", "candlestick", "candlesticks", "bars", "ma", "ema", "sma", "zoom":
			return true
		}
		if _, ok := chartStyles[t.w]; ok {
			return true
		}
		if parseInterval(t) != "" {
			return true
		}
	}
	return false
}

// price parses "price btc eth", "btc and eth prices" into get_price.
func (p *intentParser) price(ts []tok) (string, map[string]any, bool) {
	var coins []string
	for _, t := range ts {
		switch t.w {
		case "price", "prices", "quote", "quotes", "mid", "mids", "and", "&", "vs", "+":
			continue
		}
		if fillers[t.w] {
			continue
		}
		c, ok := coinOf(t)
		if !ok {
			return "", nil, false
		}
		coins = append(coins, c)
	}
	if len(coins) == 0 {
		return "", nil, false
	}
	return "get_price", map[string]any{"coins": coins}, true
}

// content returns ts without fillers and the given words.
func content(ts []tok, drop ...string) []tok {
	d := set(drop...)
	var out []tok
	for _, t := range ts {
		if fillers[t.w] || d[t.w] {
			continue
		}
		out = append(out, t)
	}
	return out
}

func withoutWords(ts []tok, drop ...string) []tok {
	d := set(drop...)
	var out []tok
	for _, t := range ts {
		if !d[t.w] {
			out = append(out, t)
		}
	}
	return out
}

func joinO(ts []tok) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = t.o
	}
	return strings.Join(parts, " ")
}

func firstAddr(ts []tok) string {
	for _, t := range ts {
		if reAddrish.MatchString(t.w) {
			return t.o
		}
	}
	return ""
}

// addressOrName returns an address token if present, else the phrase.
func addressOrName(ts []tok) string {
	if a := firstAddr(ts); a != "" {
		return a
	}
	return joinO(ts)
}

// coinOf normalizes a coin token: BTC, xyz:NVDA, @107, HYPE/USDC, $BTC.
func coinOf(t tok) (string, bool) {
	w := strings.TrimPrefix(t.w, "$")
	switch {
	case reDexCoin.MatchString(w):
		dex, c, _ := strings.Cut(w, ":")
		return dex + ":" + strings.ToUpper(c), true
	case reSpotIdx.MatchString(w):
		return w, true
	case rePair.MatchString(w):
		return strings.ToUpper(w), true
	case w == "s&p" || w == "s&p500":
		return "S&P", true
	case reCoin.MatchString(w) && !keywords[w] && !fillers[w] && parseInterval(t) == "" && !isUnitWord(w):
		if reDuration.MatchString(w) {
			return "", false
		}
		if strings.HasPrefix(w, "k") && len(w) > 1 && knownCoins[w[1:]] && w != "kaito" {
			return "k" + strings.ToUpper(w[1:]), true // kPEPE, kBONK
		}
		return strings.ToUpper(w), true
	}
	return "", false
}

func isUnitWord(w string) bool { return units[w] }

// strictCoin is coinOf for bare phrases: the coin must be known.
func (p *intentParser) strictCoin(t tok) (string, bool) {
	c, ok := coinOf(t)
	if !ok {
		return "", false
	}
	w := strings.TrimPrefix(t.w, "$")
	if strings.Contains(w, ":") || strings.HasPrefix(w, "@") || strings.Contains(w, "/") || strings.HasPrefix(t.w, "$") {
		return c, true
	}
	if p.opts.IsCoin != nil {
		return c, p.opts.IsCoin(t.o) || p.opts.IsCoin(c)
	}
	return c, knownCoins[w]
}

func parseInterval(t tok) domain.Interval {
	if t.o == "1M" {
		return domain.I1M
	}
	w := t.w
	if iv := domain.Interval(w); iv != domain.I1M && iv.Valid() {
		return iv
	}
	if iv, ok := reIntervalW[w]; ok {
		return iv
	}
	if m := reDuration.FindStringSubmatch(w); m != nil {
		n, _ := strconv.Atoi(m[1])
		var unit string
		switch m[2][0] {
		case 'm':
			if strings.HasPrefix(m[2], "mo") {
				unit = "M"
			} else {
				unit = "m"
			}
		case 'h':
			unit = "h"
		case 'd':
			unit = "d"
		case 'w':
			unit = "w"
		default:
			return ""
		}
		iv := domain.Interval(strconv.Itoa(n) + unit)
		if iv.Valid() {
			return iv
		}
	}
	return ""
}

// durationDays parses "90days", "90d", "6months", "1y" into days.
func durationDays(w string) (int, bool) {
	m := reDuration.FindStringSubmatch(w)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	switch {
	case strings.HasPrefix(m[2], "mo"):
		return n * 30, true
	case m[2][0] == 'd':
		return n, true
	case m[2][0] == 'w':
		return n * 7, true
	case m[2][0] == 'y':
		return n * 365, true
	}
	return 0, false
}

// durationHours parses "24h", "48hours", "3days" into hours.
func durationHours(w string) (int, bool) {
	m := reDuration.FindStringSubmatch(w)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	switch {
	case m[2][0] == 'h':
		return n, true
	case m[2][0] == 'd':
		return n * 24, true
	case m[2][0] == 'w':
		return n * 24 * 7, true
	}
	return 0, false
}

// money parses $100, 100$, 1.5k, 100usd. usd reports an explicit dollar marker.
func money(w string) (v float64, usd bool, ok bool) {
	m := reMoney.FindStringSubmatch(w)
	if m == nil {
		return 0, false, false
	}
	v, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return 0, false, false
	}
	switch m[3] {
	case "k":
		v *= 1e3
	case "m", "mm":
		v *= 1e6
	case "b":
		v *= 1e9
	}
	return v, m[1] != "" || m[4] != "", true
}

func percent(w string) (float64, bool) {
	m := rePercent.FindStringSubmatch(w)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	return v, err == nil
}

func isUSDWord(w string) bool {
	return w == "usd" || w == "usdc" || w == "dollars" || w == "dollar" || w == "bucks"
}

// num formats a float for JSON without noise (100 not 100.0000001).
func num(v float64) any {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return int64(v)
	}
	return v
}

// ---------------------------------------------------------------- trading

func (p *intentParser) order(side string, ts []tok) (string, map[string]any, bool) {
	args := map[string]any{"dryRun": true}
	switch side {
	case "buy", "long", "ape":
		args["side"] = "buy"
	default:
		args["side"] = "sell"
	}
	var coin string
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		next := func() (tok, bool) {
			if i+1 < len(ts) {
				return ts[i+1], true
			}
			return tok{}, false
		}
		switch t.w {
		case "of", "worth", "in", "on", "with", "market", "leverage", "lev", "a", "an", "some", "for", "my":
			if t.w == "lev" || t.w == "leverage" {
				if n, ok := next(); ok {
					if l, err := strconv.Atoi(strings.TrimSuffix(n.w, "x")); err == nil && l > 0 {
						args["leverage"] = l
						i++
					}
				}
			}
			continue
		case "at", "@", "limit", "px", "price":
			if n, ok := next(); ok {
				if v, _, ok := money(n.w); ok {
					args["px"] = trimNum(strings.TrimPrefix(strings.TrimSuffix(n.w, "$"), "$"), v)
					i++
				}
			}
			continue
		case "tp", "target":
			if n, ok := next(); ok {
				args["tp"] = signedTarget(n.w, "+")
				i++
			}
			continue
		case "sl", "stop":
			if n, ok := next(); ok && (n.w == "loss" || n.w == "at") {
				i++
			}
			if n, ok := next(); ok {
				args["sl"] = signedTarget(n.w, "-")
				i++
			}
			continue
		case "take":
			if n, ok := next(); ok && n.w == "profit" {
				i++
				if n2, ok := next(); ok {
					args["tp"] = signedTarget(n2.w, "+")
					i++
				}
			}
			continue
		case "reduce-only", "ro", "reduceonly":
			args["reduceOnly"] = true
			continue
		case "reduce":
			if n, ok := next(); ok && n.w == "only" {
				args["reduceOnly"] = true
				i++
			}
			continue
		}
		if strings.HasPrefix(t.w, "@") && len(t.w) > 1 && !reSpotIdx.MatchString(t.w) {
			if v, _, ok := money(t.w[1:]); ok {
				args["px"] = trimNum(t.w[1:], v)
				continue
			}
		}
		if m := reLeverage.FindStringSubmatch(t.w); m != nil {
			l, _ := strconv.Atoi(m[1] + m[2])
			if l > 0 {
				args["leverage"] = l
			}
			continue
		}
		if v, usd, ok := money(t.w); ok {
			if !usd {
				if n, ok := next(); ok && isUSDWord(n.w) {
					usd = true
					i++
				}
			}
			if usd {
				args["usd"] = num(v)
			} else {
				args["size"] = trimNum(t.w, v)
			}
			continue
		}
		if isUSDWord(t.w) {
			continue
		}
		if coin == "" {
			if c, ok := coinOf(t); ok {
				coin = c
				continue
			}
		}
		return "", nil, false // unexpected word: let the LLM handle it
	}
	_, hasSize := args["size"]
	_, hasUSD := args["usd"]
	if coin == "" || (!hasSize && !hasUSD) {
		return "", nil, false
	}
	args["coin"] = coin
	return "place_order", args, true
}

// trimNum keeps a typed decimal ("0.10" -> "0.1"); suffixed numbers expand.
func trimNum(typed string, v float64) string {
	if _, err := strconv.ParseFloat(typed, 64); err == nil {
		s := strings.TrimLeft(typed, "+")
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
		if strings.HasPrefix(s, ".") {
			s = "0" + s
		}
		return s
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// signedTarget turns "5%" into "+5%" (tp) or "-5%" (sl); prices pass through.
func signedTarget(w, sign string) string {
	if v, ok := percent(w); ok {
		if strings.HasPrefix(w, "+") || strings.HasPrefix(w, "-") {
			return w
		}
		return sign + strconv.FormatFloat(v, 'f', -1, 64) + "%"
	}
	if v, _, ok := money(w); ok {
		return trimNum(strings.Trim(w, "$"), v)
	}
	return w
}

func (p *intentParser) close(ts []tok) (string, map[string]any, bool) {
	args := map[string]any{"dryRun": true}
	rest := withoutWords(ts, "my", "position", "positions", "of", "out", "the", "on", "please")
	if len(rest) == 0 {
		return "", nil, false
	}
	var coin string
	for _, t := range rest {
		switch {
		case t.w == "all" || t.w == "everything" || t.w == "every":
			args["all"] = true
		case t.w == "half":
			args["pct"] = 50
		case t.w == "quarter":
			args["pct"] = 25
		default:
			if v, ok := percent(t.w); ok && v > 0 && v <= 100 {
				args["pct"] = num(v)
				continue
			}
			c, ok := coinOf(t)
			if !ok || coin != "" {
				return "", nil, false
			}
			coin = c
		}
	}
	if coin != "" {
		args["coin"] = coin
		delete(args, "all")
	}
	if coin == "" && args["all"] == nil {
		return "", nil, false
	}
	return CloseTool, args, true
}

func (p *intentParser) cancel(ts []tok) (string, map[string]any, bool) {
	args := map[string]any{"dryRun": true}
	all := false
	for _, t := range ts {
		switch t.w {
		case "all", "every", "everything", "orders", "order", "my", "on", "for", "the", "open":
			if t.w == "all" || t.w == "every" || t.w == "everything" {
				all = true
			}
			continue
		}
		if oid, err := strconv.ParseUint(strings.TrimPrefix(t.w, "#"), 10, 64); err == nil {
			args["oid"] = oid
			continue
		}
		if c, ok := coinOf(t); ok && args["coin"] == nil {
			args["coin"] = c
			continue
		}
		return "", nil, false
	}
	if args["coin"] == nil && args["oid"] == nil {
		_ = all
		return "", nil, false // "cancel" alone or "cancel all" with no market: ask
	}
	return "cancel_order", args, true
}

func (p *intentParser) transfer(kind string, ts []tok) (string, map[string]any, bool) {
	args := map[string]any{"dryRun": true}
	var name []tok
	for _, t := range ts {
		switch t.w {
		case "into", "in", "to", "from", "out", "of", "the", "vault":
			continue
		}
		if v, _, ok := money(t.w); ok && args["usd"] == nil {
			args["usd"] = num(v)
			continue
		}
		if isUSDWord(t.w) {
			continue
		}
		name = append(name, t)
	}
	if args["usd"] == nil || len(name) == 0 {
		return "", nil, false
	}
	args["vault"] = addressOrName(name)
	if kind == "deposit" {
		return "vault_deposit", args, true
	}
	return "vault_withdraw", args, true
}

// ---------------------------------------------------------------- strategy

var (
	reThesisLead = regexp.MustCompile(`(?i)^\s*(?:(?:please|pls|can you|could you)\s+)?(?:backtest|bt)\s+(?:(?:the|a|my)\s+)?(?:thesis\s*:?\s+)?`)
	reThesisOpt  = regexp.MustCompile(`(?i)[\s,]*\b(?:over|last|past|for)\s+(?:the\s+)?(?:last\s+|past\s+)?\d+\s*(?:d|days?|w|wks?|weeks?|mo|mons?|months?|y|yrs?|years?)\b|[\s,]*\b(?:with\s+)?capital\s+\$?[0-9][0-9.,]*[kmb]?\b|[\s,]*(?:with\s+)?\$[0-9][0-9.,]*[kmb]?(?:\s+capital)?\b`)
)

// thesisText keeps the user's thesis as typed (commas separate legs) minus
// the command word and the backtest options parsed separately.
func thesisText(text string) string {
	t := reThesisLead.ReplaceAllString(text, "")
	t = reThesisOpt.ReplaceAllString(t, "")
	return strings.Trim(strings.TrimSpace(t), ",;.")
}

func (p *intentParser) backtest(ts []tok, raw string) (string, map[string]any, bool) {
	args := map[string]any{}
	var rest []tok
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		if reFile.MatchString(t.o) && args["file"] == nil {
			args["file"] = t.o
			continue
		}
		if (t.w == "over" || t.w == "last" || t.w == "past" || t.w == "for") && i+1 < len(ts) {
			if d, ok := durationDays(ts[i+1].w); ok {
				args["days"] = d
				i++
				continue
			}
		}
		if t.w == "capital" && i+1 < len(ts) {
			if v, _, ok := money(ts[i+1].w); ok {
				args["capital"] = num(v)
				i++
				continue
			}
		}
		if v, usd, ok := money(t.w); ok && usd {
			args["capital"] = num(v)
			continue
		}
		rest = append(rest, t)
	}
	if args["file"] != nil {
		for i := 0; i < len(rest); i++ {
			t := rest[i]
			if t.w == "on" || t.w == "with" || t.w == "using" || t.w == "at" {
				continue
			}
			if iv := parseInterval(t); iv != "" && args["interval"] == nil {
				args["interval"] = string(iv)
				continue
			}
			if d, ok := durationDays(t.w); ok && args["days"] == nil {
				args["days"] = d
				continue
			}
			if c, ok := coinOf(t); ok && args["coin"] == nil {
				args["coin"] = c
				continue
			}
			return "", nil, false
		}
		return "run_backtest", args, true
	}
	if len(rest) == 0 {
		return "", nil, false
	}
	if raw == "" {
		raw = joinO(rest)
	}
	args["thesis"] = thesisText(raw)
	if args["thesis"] == "" {
		return "", nil, false
	}
	return "run_backtest", args, true
}

func (p *intentParser) pine(ts []tok) (string, map[string]any, bool) {
	if len(ts) == 0 {
		return "", nil, false
	}
	switch ts[0].w {
	case "check", "lint", "validate", "parse":
		if len(ts) > 1 {
			return "pine_check", map[string]any{"file": ts[1].o}, true
		}
		return "", nil, false
	case "run", "backtest", "test":
		return p.backtest(ts[1:], "")
	}
	if reFile.MatchString(ts[0].o) {
		if len(ts) == 1 {
			return "pine_check", map[string]any{"file": ts[0].o}, true
		}
		return p.backtest(ts, "")
	}
	return "", nil, false
}

func (p *intentParser) paper(ts []tok) (string, map[string]any, bool) {
	args := map[string]any{}
	action := ""
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		switch t.w {
		case "start", "run", "trade", "begin", "launch":
			action = "start"
			continue
		case "status", "show":
			action = "status"
			continue
		case "stop", "kill", "end", "halt":
			action = "stop"
			continue
		case "list", "ls", "sessions", "all":
			action = "list"
			continue
		case "on", "with", "for", "the", "session":
			continue
		}
		if reFile.MatchString(t.o) || strings.HasPrefix(t.w, "llm:") {
			args["file"] = t.o
			if action == "" {
				action = "start"
			}
			continue
		}
		if v, usd, ok := money(t.w); ok && usd {
			args["capital"] = num(v)
			continue
		}
		if action == "start" || args["file"] != nil {
			if c, ok := coinOf(t); ok && args["coin"] == nil {
				args["coin"] = c
				continue
			}
		}
		if args["id"] == nil {
			args["id"] = t.o
			continue
		}
		return "", nil, false
	}
	if action == "" {
		if args["id"] != nil {
			action = "status"
		} else {
			action = "list"
		}
	}
	if action == "start" && args["file"] == nil {
		return "", nil, false
	}
	args["action"] = action
	return "paper", args, true
}

func (p *intentParser) arena(ts []tok) (string, map[string]any, bool) {
	args := map[string]any{}
	var entrants, vaults []string
	vaultMode := false
	var cur []tok
	flush := func() {
		if len(cur) > 0 {
			vaults = append(vaults, addressOrName(cur))
			cur = nil
		}
	}
	for _, t := range ts {
		switch {
		case t.w == "vault" || t.w == "vaults":
			vaultMode = true
		case t.w == "vs" || t.w == "versus" || t.w == "and" || t.w == "against":
			if vaultMode {
				flush()
			}
		case reFile.MatchString(t.o) || strings.HasPrefix(t.w, "llm:"):
			entrants = append(entrants, t.o)
		default:
			if v, usd, ok := money(t.w); ok && usd {
				args["capital"] = num(v)
				continue
			}
			if vaultMode {
				cur = append(cur, t)
				continue
			}
			return "", nil, false
		}
	}
	flush()
	if len(entrants) > 0 {
		args["entrants"] = entrants
	}
	if len(vaults) > 0 {
		args["vaults"] = vaults
	}
	return "arena", args, true
}

// ---------------------------------------------------------------- vaults

func (p *intentParser) compare(ts []tok) (string, map[string]any, bool) {
	ts = withoutWords(ts, "vault", "vaults", "the")
	seps := set("vs", "versus", "and", "with", "to", "against", "&&")
	hasSep := false
	for _, t := range ts {
		if seps[t.w] {
			hasSep = true
		}
	}
	var names []string
	if hasSep {
		var cur []tok
		for _, t := range ts {
			if seps[t.w] {
				if len(cur) > 0 {
					names = append(names, addressOrName(cur))
				}
				cur = nil
				continue
			}
			cur = append(cur, t)
		}
		if len(cur) > 0 {
			names = append(names, addressOrName(cur))
		}
	} else {
		for _, t := range ts {
			names = append(names, t.o)
		}
	}
	if len(names) < 2 || len(names) > 6 {
		return "", nil, false
	}
	return "compare_vaults", map[string]any{"vaults": names}, true
}

// vaultSorts maps words to list_vaults sort keys.
var vaultSorts = map[string]string{
	"sharpe": "sharpe", "tvl": "tvl", "aum": "tvl", "size": "tvl", "biggest": "tvl", "largest": "tvl", "deposits": "tvl",
	"apr": "apr", "apy": "apr", "yield": "apr", "drawdown": "maxdd", "dd": "maxdd", "maxdd": "maxdd",
	"age": "age", "oldest": "age", "7d": "ret7d", "week": "ret7d", "weekly": "ret7d", "30d": "ret30d",
	"month": "ret30d", "monthly": "ret30d", "return": "retall", "returns": "retall", "performance": "retall",
	"alltime": "retall", "all-time": "retall", "pnl": "retall", "performing": "retall",
}

// isProse reports whether leftover words read as a sentence rather than a
// short vault name ("ai", "growi", "long hype").
func isProse(q []tok) bool {
	if len(q) > 2 {
		return true
	}
	for _, t := range q {
		if proseWords[t.w] || questionLeads[t.w] {
			return true
		}
	}
	return false
}

func (p *intentParser) vaults(ts []tok) (string, map[string]any, bool) {
	args := map[string]any{}
	used := make([]bool, len(ts))
	mark := func(i int) {
		if i >= 0 && i < len(ts) {
			used[i] = true
		}
	}
	near := func(i, radius int, words ...string) bool {
		ws := set(words...)
		for j := i - radius; j <= i+radius; j++ {
			if j >= 0 && j < len(ts) && j != i && ws[ts[j].w] {
				return true
			}
		}
		return false
	}
	sortCue := false
	for i, t := range ts {
		switch t.w {
		case "vaults", "vault", "screener", "screen", "list", "named", "called", "matching", "whose", "name", "by", "sorted",
			"sort", "ranked", "rank", "with", "and", "than", "older", "old", "at", "least", "less", "more", "max", "min",
			"under", "below", "over", "above", "<", ">", "<=", ">=", "accepting", "taking", "including", "include":
			mark(i)
			if t.w == "by" || t.w == "sorted" || t.w == "sort" || t.w == "ranked" || t.w == "rank" {
				sortCue = true
			}
		case "top", "best", "highest", "most":
			mark(i)
			sortCue = true
			if t.w == "best" && args["sort"] == nil {
				args["sort"] = "sharpe"
			}
			if i+1 < len(ts) {
				if n, err := strconv.Atoi(ts[i+1].w); err == nil && n > 0 {
					args["limit"] = n
					mark(i + 1)
				}
			}
		case "open":
			args["open"] = true
			mark(i)
		case "curated":
			args["curated"] = true
			mark(i)
		case "hlp":
			mark(i)
			if i > 0 && (ts[i-1].w == "without" || ts[i-1].w == "excluding" || ts[i-1].w == "no" || ts[i-1].w == "ex") {
				mark(i - 1)
			} else {
				args["hlp"] = true
			}
		}
	}
	for i, t := range ts {
		if used[i] {
			continue
		}
		if v, ok := percent(t.w); ok && near(i, 3, "drawdown", "dd", "maxdd") {
			args["maxDd"] = num(math.Abs(v))
			mark(i)
			continue
		}
		if t.w == "drawdown" || t.w == "dd" || t.w == "maxdd" {
			if i+1 < len(ts) {
				if v, err := strconv.ParseFloat(ts[i+1].w, 64); err == nil {
					args["maxDd"] = num(math.Abs(v))
					mark(i)
					mark(i + 1)
					continue
				}
			}
		}
		if d, ok := durationDays(t.w); ok && near(i, 2, "older", "old", "age", "than", "least", "over") {
			args["minAge"] = d
			mark(i)
			continue
		}
		if v, usd, ok := money(t.w); ok && (usd || strings.ContainsAny(t.w, "kmb") || near(i, 2, "tvl", "aum", "size")) {
			if v >= 1000 || near(i, 2, "tvl", "aum", "size") {
				args["minTvl"] = num(v)
				mark(i)
				continue
			}
		}
		if n, err := strconv.Atoi(t.w); err == nil && n > 0 && i+1 < len(ts) && ts[i+1].w == "vaults" {
			args["limit"] = n
			mark(i)
			continue
		}
		if s, ok := vaultSorts[t.w]; ok {
			if (t.w == "tvl" || t.w == "aum" || t.w == "size") && args["minTvl"] != nil {
				mark(i)
				continue
			}
			if sortCue || t.w == "biggest" || t.w == "largest" || t.w == "oldest" || t.w == "performing" {
				args["sort"] = s
			}
			mark(i)
			continue
		}
	}
	// "risk-adjusted returns" ranks by Sharpe, not by raw return.
	for i, t := range ts {
		if t.w == "risk-adjusted" || (t.w == "adjusted" && i > 0 && ts[i-1].w == "risk") {
			args["sort"] = "sharpe"
			mark(i)
			if t.w == "adjusted" {
				mark(i - 1)
			}
		}
	}
	// "7d return" / "30d returns": the period wins over plain "return".
	for i, t := range ts {
		if (t.w == "7d" || t.w == "30d") && used[i] && (sortCue || args["sort"] == "retall") {
			args["sort"] = vaultSorts[t.w]
		}
	}
	var q []tok
	for i, t := range ts {
		if used[i] || fillers[t.w] || t.w == "without" || t.w == "excluding" || t.w == "days" {
			continue
		}
		q = append(q, t)
	}
	if len(q) > 0 {
		named := false
		for _, t := range ts {
			if t.w == "named" || t.w == "called" || t.w == "matching" || t.w == "whose" {
				named = true
			}
		}
		if !named && isProse(q) {
			// The leftovers are a sentence, not a vault name: this is a
			// question for the model, not a name search ("0 vaults").
			if !p.loose {
				return "", nil, false
			}
		} else {
			args["query"] = joinO(q)
		}
	}
	return "list_vaults", args, true
}

// ---------------------------------------------------------------- account

func (p *intentParser) fills(ts []tok) (string, map[string]any, bool) {
	args := map[string]any{}
	if a := firstAddr(ts); a != "" {
		args["address"] = a
	}
	for _, t := range content(ts, "fills", "fill", "my", "mine", "trades", "history", "recent", "last", "past") {
		if reAddrish.MatchString(t.w) {
			continue
		}
		if h, ok := durationHours(t.w); ok {
			args["hours"] = h
			continue
		}
		if c, ok := coinOf(t); ok && args["coin"] == nil {
			args["coin"] = c
			continue
		}
		return "", nil, false
	}
	return "get_fills", args, true
}

// ---------------------------------------------------------------- market

func (p *intentParser) markets(ts []tok) (string, map[string]any, bool) {
	args := map[string]any{}
	var q []tok
	for i, t := range ts {
		switch t.w {
		case "markets", "market", "all", "list", "top", "show", "by", "sorted", "with", "most", "highest", "biggest":
			if t.w == "top" && i+1 < len(ts) {
				if n, err := strconv.Atoi(ts[i+1].w); err == nil {
					args["limit"] = n
				}
			}
			continue
		case "spot":
			args["kind"] = "spot"
		case "perp", "perps", "perpetuals":
			args["kind"] = "perp"
		case "hip3", "hip-3", "builder", "stocks", "equities", "tradfi":
			args["kind"] = "hip3"
		case "outcome", "outcomes", "prediction", "predictions":
			args["kind"] = "outcome"
		case "gainers", "losers", "movers", "change", "movement", "moving", "pumping", "dumping":
			args["sort"] = "change"
		case "funding":
			args["sort"] = "funding"
		case "oi", "interest":
			args["sort"] = "oi"
		case "volume", "active", "busiest", "traded":
			args["sort"] = "volume"
		case "name", "alphabetical", "az":
			args["sort"] = "name"
		case "open":
			if i+1 < len(ts) && ts[i+1].w == "interest" {
				continue
			}
			q = append(q, t)
		default:
			if n, err := strconv.Atoi(t.w); err == nil && i > 0 && ts[i-1].w == "top" {
				_ = n
				continue
			}
			if fillers[t.w] {
				continue
			}
			if i+1 < len(ts) && (ts[i+1].w == "markets" || ts[i+1].w == "market") && reCoin.MatchString(t.w) && len(t.w) <= 6 && !knownCoins[t.w] {
				args["dex"] = t.w // "xyz markets"
				continue
			}
			q = append(q, t)
		}
	}
	if len(q) > 0 {
		args["query"] = joinO(q)
	}
	return "get_markets", args, true
}

func (p *intentParser) funding(ts []tok) (string, map[string]any, bool) {
	args := map[string]any{}
	for i, t := range ts {
		switch t.w {
		case "funding", "fundings", "rate", "rates", "history", "highest", "lowest", "negative", "positive",
			"top", "most", "ranking", "rank", "apr", "annualized", "hourly", "current", "last", "past", "my":
			if t.w == "top" && i+1 < len(ts) {
				if n, err := strconv.Atoi(ts[i+1].w); err == nil && n > 0 {
					args["top"] = n
				}
			}
			continue
		}
		if fillers[t.w] {
			continue
		}
		if h, ok := durationHours(t.w); ok {
			args["hours"] = h
			continue
		}
		if n, err := strconv.Atoi(t.w); err == nil && i > 0 && ts[i-1].w == "top" {
			_ = n
			continue
		}
		if c, ok := coinOf(t); ok && args["coin"] == nil {
			args["coin"] = c
			continue
		}
		return "", nil, false
	}
	if args["coin"] == nil {
		delete(args, "hours")
	}
	return "get_funding", args, true
}

// coinTool parses "<keyword> <coin>" phrases for single-coin tools; extra
// handles tool-specific tokens.
func (p *intentParser) coinTool(tool string, ts []tok, extra func(map[string]any, tok) bool) (string, map[string]any, bool) {
	args := map[string]any{}
	for _, t := range ts {
		if keywords[t.w] || fillers[t.w] || t.w == "asset-id" {
			continue
		}
		if extra != nil && extra(args, t) {
			continue
		}
		if c, ok := coinOf(t); ok && args["coin"] == nil {
			args["coin"] = c
			continue
		}
		return "", nil, false
	}
	if args["coin"] == nil {
		return "", nil, false
	}
	return tool, args, true
}

var chartStyles = map[string]string{"heikin": "heikin", "heikin-ashi": "heikin", "ha": "heikin", "lines": "lines",
	"line": "line", "hollow": "hollow", "solid": "solid", "ohlc": "ohlc", "classic": "classic", "ashi": "", "blocks": "blocks", "ascii": "ascii", "mono": "mono", "half": "half", "candles": "", "candle": "",
	"candlestick": "", "candlesticks": ""}

func (p *intentParser) chart(ts []tok) (string, map[string]any, bool) {
	args := map[string]any{}
	keyword := false
	var mas, emas []int
	var coins []tok
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		switch t.w {
		case "chart", "charts", "graph", "price", "prices", "c", "plot":
			keyword = true
			continue
		case "ma", "sma", "mas", "ema", "emas":
			keyword = true
			for i+1 < len(ts) {
				n, err := strconv.Atoi(ts[i+1].w)
				if err != nil || n <= 0 || n > 1000 {
					break
				}
				if strings.HasPrefix(t.w, "ema") {
					emas = append(emas, n)
				} else {
					mas = append(mas, n)
				}
				i++
			}
			continue
		case "bars", "candles", "candle":
			if t.w != "bars" {
				keyword = true
			}
			continue
		case "zoom":
			if i+1 < len(ts) {
				if n, err := strconv.Atoi(ts[i+1].w); err == nil && (n == 1 || n == 2 || n == 4 || n == 6) {
					args["zoom"] = n
					i++
				}
			}
			continue
		}
		if s, ok := chartStyles[t.w]; ok {
			keyword = true
			if s != "" {
				args["style"] = s
			}
			continue
		}
		if strings.HasPrefix(t.w, "ma") || strings.HasPrefix(t.w, "ema") || strings.HasPrefix(t.w, "sma") {
			digits := strings.TrimLeft(t.w, "smae")
			if n, err := strconv.Atoi(digits); err == nil && n > 0 {
				if strings.HasPrefix(t.w, "ema") {
					emas = append(emas, n)
				} else {
					mas = append(mas, n)
				}
				keyword = true
				continue
			}
		}
		if iv := parseInterval(t); iv != "" && args["interval"] == nil {
			args["interval"] = string(iv)
			continue
		}
		if n, err := strconv.Atoi(t.w); err == nil && n > 0 && n <= 5000 && i+1 < len(ts) && (ts[i+1].w == "bars" || ts[i+1].w == "candles") {
			args["bars"] = n
			i++
			continue
		}
		if t.w == "last" && i+2 < len(ts) {
			if n, err := strconv.Atoi(ts[i+1].w); err == nil && (ts[i+2].w == "bars" || ts[i+2].w == "candles") {
				args["bars"] = n
				i += 2
				continue
			}
		}
		if fillers[t.w] || t.w == "chart" {
			continue
		}
		coins = append(coins, t)
	}
	if len(coins) != 1 {
		return "", nil, false
	}
	var coin string
	var ok bool
	if keyword {
		coin, ok = coinOf(coins[0])
	} else {
		coin, ok = p.strictCoin(coins[0])
	}
	if !ok {
		return "", nil, false
	}
	args["coin"] = coin
	if len(mas) > 0 {
		args["ma"] = mas
	}
	if len(emas) > 0 {
		args["ema"] = emas
	}
	return "get_candles", args, true
}

// Suggest returns the read-only slash command closest to a free-form
// question that ParseIntent declined ("which vaults have the best
// risk-adjusted returns over the last 90 days?" -> "/vaults --sort sharpe
// --min-age 90"), for shells with no model configured. It never suggests a
// mutating (trade, transfer) command. ok=false when nothing fits.
func Suggest(text string, r *core.Registry) (string, bool) {
	if r == nil {
		r = core.Default()
	}
	p := &intentParser{loose: true}
	tool, args, ok := p.parse(text)
	if !ok {
		return "", false
	}
	call, ok := callFor(r, tool, args)
	if !ok {
		return "", false
	}
	t, _ := r.Get(call.Tool)
	if t.Mutating || t.Slash == "" {
		return "", false
	}
	return SlashLine(t, args), true
}

// SlashLine formats args as a slash command for t: "/vaults --sort sharpe".
// Fields are emitted in declaration order so the line is stable.
func SlashLine(t core.Tool, args map[string]any) string {
	var b strings.Builder
	b.WriteString("/" + t.Slash)
	for _, f := range core.Fields(t) {
		v, ok := args[f.Name]
		if !ok {
			continue
		}
		var val string
		switch x := v.(type) {
		case bool:
			if !x {
				continue
			}
			b.WriteString(" --" + f.Flag)
			continue
		case []string:
			val = strings.Join(x, ",")
		case []any:
			parts := make([]string, len(x))
			for i, e := range x {
				parts[i] = fmtArg(e)
			}
			val = strings.Join(parts, ",")
		case []int:
			parts := make([]string, len(x))
			for i, e := range x {
				parts[i] = strconv.Itoa(e)
			}
			val = strings.Join(parts, ",")
		default:
			val = fmtArg(x)
		}
		if strings.ContainsAny(val, " \t\"'") {
			val = strconv.Quote(val)
		}
		b.WriteString(" --" + f.Flag + " " + val)
	}
	return b.String()
}

func fmtArg(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case json.Number:
		return x.String()
	}
	raw, _ := json.Marshal(v)
	return strings.Trim(string(raw), `"`)
}
