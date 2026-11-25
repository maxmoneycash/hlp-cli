package pyne

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PYNE's `execute` envelope only carries the trailing plot samples, so the
// full trade list and per-bar equity are exported by appending this tail to
// strategy scripts. It is hlp's own code (plain Pine builtins), runs after
// the user's code on every bar and only writes log lines whose prefix
// carries a random per-run nonce ("hlp.<nonce>.bar|"), which Execute
// strips from Result.Logs. The script cannot know the nonce, so a script
// that logs "hlp.bar|..." itself cannot forge equity points or trades:
// such lines stay ordinary log text.
const instrumentTail = `

// ---- appended by hlp (PYNE result export; not part of the script) ----
var int hlpPyneSeen = 0
if strategy.closedtrades > hlpPyneSeen
    for hlpPyneI = hlpPyneSeen to strategy.closedtrades - 1
        log.info("hlp.trade|" + str.tostring(strategy.closedtrades.entry_bar_index(hlpPyneI)) + "|" + str.tostring(strategy.closedtrades.exit_bar_index(hlpPyneI)) + "|" + str.tostring(strategy.closedtrades.entry_time(hlpPyneI)) + "|" + str.tostring(strategy.closedtrades.exit_time(hlpPyneI)) + "|" + str.tostring(strategy.closedtrades.entry_price(hlpPyneI)) + "|" + str.tostring(strategy.closedtrades.exit_price(hlpPyneI)) + "|" + str.tostring(strategy.closedtrades.size(hlpPyneI)) + "|" + str.tostring(strategy.closedtrades.profit(hlpPyneI)) + "|" + str.tostring(strategy.closedtrades.commission(hlpPyneI)) + "|" + strategy.closedtrades.entry_id(hlpPyneI) + "|" + strategy.closedtrades.exit_id(hlpPyneI))
    hlpPyneSeen := strategy.closedtrades
hlpPyneOpenComm = 0.0
if strategy.opentrades > 0
    for hlpPyneJ = 0 to strategy.opentrades - 1
        hlpPyneOpenComm += strategy.opentrades.commission(hlpPyneJ)
log.info("hlp.bar|" + str.tostring(bar_index) + "|" + str.tostring(time) + "|" + str.tostring(strategy.equity) + "|" + str.tostring(strategy.position_size) + "|" + str.tostring(strategy.position_avg_price) + "|" + str.tostring(strategy.netprofit) + "|" + str.tostring(strategy.openprofit) + "|" + str.tostring(strategy.closedtrades) + "|" + str.tostring(strategy.wintrades) + "|" + str.tostring(hlpPyneOpenComm))
`

// prefixes are the export line prefixes of one run.
type prefixes struct{ trade, bar string }

func newPrefixes(nonce string) prefixes {
	return prefixes{trade: "hlp." + nonce + ".trade|", bar: "hlp." + nonce + ".bar|"}
}

// newNonce returns 16 random hex characters.
func newNonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; fall back to time.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

var strategyDeclRE = regexp.MustCompile(`(?m)^strategy\s*\(`)

// isStrategy reports whether src declares strategy() at top level.
func isStrategy(src string) bool { return strategyDeclRE.MatchString(src) }

// instrument appends the export tail, its log prefixes keyed by nonce.
func instrument(src, nonce string) string {
	p := newPrefixes(nonce)
	tail := strings.NewReplacer(`"hlp.trade|"`, `"`+p.trade+`"`, `"hlp.bar|"`, `"`+p.bar+`"`).Replace(instrumentTail)
	return strings.TrimRight(src, " \t\r\n") + tail
}

// closeFillsRE finds settings under which PYNE fills orders at the close
// of the bar that placed them (strategy(process_orders_on_close=true),
// calc_on_order_fills=true, strategy.close/close_all(immediately=true)).
var closeFillsRE = regexp.MustCompile(`\b(?:process_orders_on_close|calc_on_order_fills|immediately)\s*=\s*true\b`)

// fillsOnClose reports whether src (comments ignored) uses close fills.
func fillsOnClose(src string) bool {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return closeFillsRE.MatchString(b.String())
}

// num parses a Pine str.tostring number ("NaN" -> NaN).
func num(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "nan") || s == "na" {
		return math.NaN()
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return v
}

func numInt(s string) int64 {
	v := num(s)
	if math.IsNaN(v) {
		return -1
	}
	return int64(v)
}

type barRow struct {
	idx                                         int
	t                                           int64
	equity, pos, avgPx, net, open, closed, wins float64
	openComm                                    float64 // entry commission of open trades
}

// parseBar reads one bar row (rest = the line without its prefix). Rows
// recorded before the open-commission field have 9 fields.
func parseBar(rest string) (barRow, bool) {
	f := strings.Split(rest, "|")
	if len(f) != 9 && len(f) != 10 {
		return barRow{}, false
	}
	b := barRow{
		idx: int(numInt(f[0])), t: numInt(f[1]), equity: num(f[2]), pos: num(f[3]), avgPx: num(f[4]),
		net: num(f[5]), open: num(f[6]), closed: num(f[7]), wins: num(f[8]),
	}
	if len(f) == 10 {
		if v := num(f[9]); !math.IsNaN(v) {
			b.openComm = v
		}
	}
	return b, true
}

func parseTrade(rest string) (Trade, bool) {
	// entry_id may not contain "|" (exit_id takes the remainder).
	f := strings.SplitN(rest, "|", 11)
	if len(f) != 11 {
		return Trade{}, false
	}
	return Trade{
		EntryBar: int(numInt(f[0])), ExitBar: int(numInt(f[1])),
		EntryTime: numInt(f[2]), ExitTime: numInt(f[3]),
		EntryPx: num(f[4]), ExitPx: num(f[5]), Size: math.Abs(num(f[6])),
		Profit: num(f[7]), Commission: num(f[8]),
		EntryID: f[9], ExitID: f[10],
	}, true
}
