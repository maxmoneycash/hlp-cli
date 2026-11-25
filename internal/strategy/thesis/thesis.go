package thesis

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy"
)

// Leg is one coin or basket with a relative weight (default 1).
type Leg struct {
	Coin   string  `yaml:"coin,omitempty" json:"coin,omitempty"`
	Basket string  `yaml:"basket,omitempty" json:"basket,omitempty"`
	Weight float64 `yaml:"weight,omitempty" json:"weight,omitempty"`

	// Filter keeps only basket members that trend the right way at each
	// rebalance: "falling" (return over Lookback bars < 0) or "rising".
	// hlp.xyz's spxmeme shorts memecoins "that are already falling".
	Filter string `yaml:"filter,omitempty" json:"filter,omitempty"`
	// Lookback in bars for Filter (default 30).
	Lookback int `yaml:"lookback,omitempty" json:"lookback,omitempty"`
	// PerName gives every qualifying member this fixed weight instead of an
	// equal split of Weight (spxmeme: 0.075 per short). With PerName set the
	// leg's nominal weight is PerName x members.
	PerName float64 `yaml:"per_name,omitempty" json:"per_name,omitempty"`
}

// Spec is a thesis document.
type Spec struct {
	Name      string              `yaml:"name" json:"name"`
	Interval  domain.Interval     `yaml:"interval" json:"interval"`
	Rebalance string              `yaml:"rebalance" json:"rebalance"` // 1d, 1w, 1M, never
	Leverage  float64             `yaml:"leverage" json:"leverage"`
	Long      []Leg               `yaml:"long" json:"long"`
	Short     []Leg               `yaml:"short" json:"short"`
	Baskets   map[string][]string `yaml:"baskets,omitempty" json:"baskets,omitempty"`
}

// Builtin baskets (resolved against the live registry; missing coins are
// dropped with a warning). Keep in sync with hlp.xyz spxmeme where sensible.
var Builtin = map[string][]string{
	"memes":    {"DOGE", "FARTCOIN", "TRUMP", "PUMP", "kPEPE", "WIF", "kBONK"},
	"majors":   {"BTC", "ETH", "SOL", "HYPE"},
	"equities": {"xyz:SP500", "xyz:NVDA", "xyz:GOOGL"},
	"alts":     {"SOL", "AVAX", "SUI", "APT", "ARB", "OP", "TIA", "SEI"},
}

// Parse decodes YAML or JSON, applies defaults and checks the shape (legs,
// weights, interval, rebalance). Coin resolution is Validate's job.
func Parse(b []byte) (Spec, error) {
	var s Spec
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return s, errors.New("thesis: empty document")
	}
	var err error
	if b[0] == '{' {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		err = dec.Decode(&s)
	} else {
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		err = dec.Decode(&s)
	}
	if err != nil {
		return Spec{}, fmt.Errorf("thesis: %w", err)
	}
	s.defaults()
	if err := s.check(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

func (s *Spec) defaults() {
	if s.Interval == "" {
		s.Interval = domain.I1d
	}
	if s.Rebalance == "" {
		s.Rebalance = "1w"
	}
	if s.Name == "" {
		s.Name = s.autoName()
	}
	if s.Leverage == 0 {
		s.Leverage = s.nominal()
	}
}

var inlineBasketRE = regexp.MustCompile(`^(long|short)\d+$`)

func (s Spec) autoName() string {
	part := func(side string, legs []Leg) string {
		var ns []string
		for _, l := range legs {
			// an inline basket ("short DOGE FARTCOIN" -> basket short1) is
			// named after its coins, not its generated id
			if cs, ok := s.Baskets[l.Basket]; ok && inlineBasketRE.MatchString(l.Basket) && len(cs) > 0 {
				ns = append(ns, strings.Join(cs, "+"))
				continue
			}
			ns = append(ns, l.label())
		}
		if len(ns) == 0 {
			return ""
		}
		return side + " " + strings.Join(ns, " ")
	}
	var bits []string
	if p := part("Long", s.Long); p != "" {
		bits = append(bits, p)
	}
	if p := part("Short", s.Short); p != "" {
		bits = append(bits, p)
	}
	return strings.Join(bits, " / ")
}

func (l Leg) label() string {
	if l.Coin != "" {
		return l.Coin
	}
	return l.Basket
}

func (l Leg) weight() float64 {
	if l.Weight == 0 {
		return 1
	}
	return l.Weight
}

func (s Spec) check() error {
	if len(s.Long)+len(s.Short) == 0 {
		return errors.New("thesis: needs at least one long or short leg")
	}
	if !s.Interval.Valid() {
		return fmt.Errorf("thesis: invalid interval %q", s.Interval)
	}
	if _, err := rebalanceKeyFunc(s.Rebalance); err != nil {
		return err
	}
	if s.Leverage < 0 || math.IsNaN(s.Leverage) || math.IsInf(s.Leverage, 0) {
		return fmt.Errorf("thesis: leverage must be a positive number, got %v", s.Leverage)
	}
	if s.Leverage > 50 {
		return fmt.Errorf("thesis: leverage %.4gx is above Hyperliquid's 50x maximum", s.Leverage)
	}
	for _, side := range [][]Leg{s.Long, s.Short} {
		for _, l := range side {
			switch {
			case l.Coin == "" && l.Basket == "":
				return errors.New("thesis: each leg needs coin or basket")
			case l.Coin != "" && l.Basket != "":
				return fmt.Errorf("thesis: leg %q sets both coin and basket", l.Coin)
			case l.Weight < 0 || l.PerName < 0:
				return fmt.Errorf("thesis: leg %s: weights are magnitudes; use the long/short lists for sign", l.label())
			case l.Filter != "" && l.Filter != "falling" && l.Filter != "rising":
				return fmt.Errorf("thesis: leg %s: filter must be falling or rising, got %q", l.label(), l.Filter)
			case l.Lookback < 0:
				return fmt.Errorf("thesis: leg %s: lookback must be >= 0", l.label())
			}
			if l.Basket != "" && len(s.members(l)) == 0 {
				return fmt.Errorf("thesis: unknown basket %q (built-in: %s)", l.Basket, strings.Join(BasketNames(), ", "))
			}
		}
	}
	return nil
}

// BasketNames lists the built-in basket names, sorted.
func BasketNames() []string {
	out := make([]string, 0, len(Builtin))
	for k := range Builtin {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// members returns the coins of a leg (custom baskets override built-ins).
func (s Spec) members(l Leg) []string {
	if l.Coin != "" {
		return []string{l.Coin}
	}
	if m, ok := s.Baskets[l.Basket]; ok {
		return m
	}
	return Builtin[l.Basket]
}

// legNominal is the leg's unscaled gross weight.
func (s Spec) legNominal(l Leg) float64 {
	if l.PerName > 0 {
		return l.PerName * float64(len(s.members(l)))
	}
	return l.weight()
}

// nominal is the sum of unscaled leg weights.
func (s Spec) nominal() float64 {
	n := 0.0
	for _, l := range s.Long {
		n += s.legNominal(l)
	}
	for _, l := range s.Short {
		n += s.legNominal(l)
	}
	return n
}

// scale maps nominal weights to Leverage.
func (s Spec) scale() float64 {
	n := s.nominal()
	if n == 0 || s.Leverage == 0 {
		return 1
	}
	return s.Leverage / n
}

// Coins returns every coin the thesis may trade, longs first, in document
// order without duplicates.
func (s Spec) Coins() []string {
	seen := map[string]bool{}
	var out []string
	for _, side := range [][]Leg{s.Long, s.Short} {
		for _, l := range side {
			for _, c := range s.members(l) {
				if !seen[c] {
					seen[c] = true
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// Weights returns the normalized signed target weights: legs scaled so the
// sum of |leg weight| equals Leverage, longs positive, shorts negative,
// basket members equal-weighted within their leg (or PerName each). A coin
// in several legs nets out. Filters are ignored here (every member counts);
// the running Strategy applies them at each rebalance.
func (s Spec) Weights() map[string]float64 {
	return s.weightsFor(nil)
}

// weightsFor computes weights; keep(coin, leg) == false drops a member.
// Dropped members of an equal-split leg give their share to the others.
func (s Spec) weightsFor(keep func(coin string, l Leg) bool) map[string]float64 {
	k := s.scale()
	out := map[string]float64{}
	add := func(legs []Leg, sign float64) {
		for _, l := range legs {
			var ms []string
			for _, c := range s.members(l) {
				if keep == nil || keep(c, l) {
					ms = append(ms, c)
				}
			}
			if len(ms) == 0 {
				continue
			}
			per := l.weight() / float64(len(ms))
			if l.PerName > 0 {
				per = l.PerName
			}
			for _, c := range ms {
				out[c] += sign * per * k
			}
		}
	}
	add(s.Long, 1)
	add(s.Short, -1)
	for c, w := range out {
		w = math.Round(w*1e12) / 1e12
		if w == 0 {
			delete(out, c)
			continue
		}
		out[c] = w
	}
	return out
}

// Validate resolves coins (resolve func from the registry) and returns
// warnings (dropped coins, HIP-3 legs vs legacy vaults, missing history).
// It fails when a single-coin leg cannot be resolved or a short leg is a
// spot market.
func (s Spec) Validate(resolve func(string) (domain.Market, error)) (warnings []string, err error) {
	_, w, err := s.Resolved(resolve)
	return w, err
}

// Resolved returns a copy of s with every coin replaced by its canonical
// wire name ("SPY" -> "xyz:SP500"), basket members that do not resolve or
// are delisted dropped (as custom baskets), plus the warnings Validate
// reports.
func (s Spec) Resolved(resolve func(string) (domain.Market, error)) (Spec, []string, error) {
	if resolve == nil {
		return s, nil, errors.New("thesis: nil resolver")
	}
	out := s
	out.Long = append([]Leg(nil), s.Long...)
	out.Short = append([]Leg(nil), s.Short...)
	out.Baskets = map[string][]string{}
	for k, v := range s.Baskets {
		out.Baskets[k] = append([]string(nil), v...)
	}
	var warns []string
	hip3 := map[string]bool{}
	cache := map[string]*domain.Market{}
	look := func(c string) (*domain.Market, error) {
		if m, ok := cache[c]; ok {
			return m, nil
		}
		m, err := resolve(c)
		if err != nil {
			cache[c] = nil
			return nil, err
		}
		cache[c] = &m
		return &m, nil
	}
	fix := func(legs []Leg, short bool) error {
		for i, l := range legs {
			if l.Coin != "" {
				m, err := look(l.Coin)
				if err != nil || m == nil {
					return fmt.Errorf("thesis: cannot resolve %q: %v", l.Coin, err)
				}
				if m.Delisted {
					return fmt.Errorf("thesis: %s is delisted", m.Coin)
				}
				if short && m.Kind == domain.Spot {
					return fmt.Errorf("thesis: cannot short spot market %s; use its perp", m.Coin)
				}
				if m.Kind == domain.Spot {
					warns = append(warns, fmt.Sprintf("%s is a spot market: no leverage, no funding", m.Coin))
				}
				if m.Kind == domain.HIP3 {
					hip3[m.Coin] = true
				}
				legs[i].Coin = m.Coin
				continue
			}
			var kept []string
			for _, c := range s.members(l) {
				m, err := look(c)
				switch {
				case err != nil || m == nil:
					warns = append(warns, fmt.Sprintf("basket %s: %s not listed on Hyperliquid; dropped", l.Basket, c))
					continue
				case m.Delisted:
					warns = append(warns, fmt.Sprintf("basket %s: %s is delisted; dropped", l.Basket, m.Coin))
					continue
				case m.Kind == domain.Spot && short:
					warns = append(warns, fmt.Sprintf("basket %s: %s is spot and cannot be shorted; dropped", l.Basket, m.Coin))
					continue
				case m.Kind == domain.HIP3:
					hip3[m.Coin] = true
				}
				kept = append(kept, m.Coin)
			}
			if len(kept) == 0 {
				return fmt.Errorf("thesis: basket %s has no tradable coins", l.Basket)
			}
			out.Baskets[l.Basket] = kept
		}
		return nil
	}
	if err := fix(out.Long, false); err != nil {
		return s, warns, err
	}
	if err := fix(out.Short, true); err != nil {
		return s, warns, err
	}
	if len(out.Baskets) == 0 {
		out.Baskets = nil
	}
	if len(hip3) > 0 {
		cs := make([]string, 0, len(hip3))
		for c := range hip3 {
			cs = append(cs, c)
		}
		sort.Strings(cs)
		warns = append(warns, fmt.Sprintf("HIP-3 legs (%s) cannot trade inside a legacy HyperCore vault (validator perps only); run this thesis in a HyperEVM strategy vault", strings.Join(cs, ", ")))
	}
	if s.Leverage > 5 {
		warns = append(warns, fmt.Sprintf("gross leverage %.2fx is high: a %.0f%% adverse move wipes the account", s.Leverage, 100/s.Leverage))
	}
	return out, warns, nil
}

// Strategy returns the rebalancing strategy.
func (s Spec) Strategy() strategy.Strategy {
	keyf, err := rebalanceKeyFunc(s.Rebalance)
	if err != nil {
		keyf, _ = rebalanceKeyFunc("1w")
	}
	return &runner{spec: s, key: keyf}
}

// runner rebalances to Spec weights at each rebalance boundary.
type runner struct {
	spec    Spec
	key     func(ms int64) int64
	lastKey int64
	started bool
}

func (r *runner) Spec() strategy.Spec {
	iv := r.spec.Interval
	if iv == "" {
		iv = domain.I1d
	}
	warm := 1
	for _, side := range [][]Leg{r.spec.Long, r.spec.Short} {
		for _, l := range side {
			if l.Filter != "" {
				warm = max(warm, lookback(l)+1)
			}
		}
	}
	return strategy.Spec{Name: r.spec.Name, Kind: "thesis", Universe: r.spec.Coins(), Interval: iv,
		Warmup: warm, Rebalance: rebalanceDuration(r.spec.Rebalance)}
}

func (r *runner) Init(strategy.Context) error { r.started = false; return nil }

func (r *runner) OnBar(ctx strategy.Context) error {
	k := r.key(ctx.Now())
	if r.started && k == r.lastKey {
		return nil
	}
	r.started, r.lastKey = true, k
	var skipped []string
	w := r.spec.weightsFor(func(c string, l Leg) bool {
		bars := ctx.Bars(c)
		if len(bars) == 0 {
			skipped = append(skipped, c)
			return false
		}
		if l.Filter == "" {
			return true
		}
		n := lookback(l)
		if len(bars) <= n {
			return false
		}
		ret := bars[len(bars)-1].C/bars[len(bars)-1-n].C - 1
		if l.Filter == "falling" {
			return ret < 0
		}
		return ret > 0
	})
	if len(skipped) > 0 {
		sort.Strings(skipped)
		ctx.Logf("not listed yet: %s", strings.Join(skipped, ", "))
	}
	ctx.Logf("rebalance: %s", FormatWeights(w))
	return ctx.SetTargets(w)
}

func lookback(l Leg) int {
	if l.Lookback > 0 {
		return l.Lookback
	}
	return 30
}

// FormatWeights renders weights as "xyz:SP500 +1.500 DOGE -0.075 ...",
// longs first, by size.
func FormatWeights(w map[string]float64) string {
	cs := make([]string, 0, len(w))
	for c := range w {
		cs = append(cs, c)
	}
	sort.Slice(cs, func(i, j int) bool {
		if w[cs[i]] != w[cs[j]] {
			return w[cs[i]] > w[cs[j]]
		}
		return cs[i] < cs[j]
	})
	var b strings.Builder
	for i, c := range cs {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s %+.3f", c, w[c])
	}
	if b.Len() == 0 {
		return "flat"
	}
	return b.String()
}

// rebalanceKeyFunc maps a timestamp to its rebalance period. Weekly periods
// follow Unix weeks, which start Thursday 00:00 UTC (hlp.xyz's spxmeme
// also rebalances on Thursdays). 1M is the calendar month (UTC).
func rebalanceKeyFunc(r string) (func(int64) int64, error) {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "never", "none", "0", "hold":
		return func(int64) int64 { return 0 }, nil
	case "bar", "always", "every":
		return func(t int64) int64 { return t }, nil
	}
	if r == "1M" || strings.EqualFold(r, "monthly") || strings.EqualFold(r, "1mo") {
		return func(t int64) int64 {
			u := time.UnixMilli(t).UTC()
			return int64(u.Year()*12 + int(u.Month()))
		}, nil
	}
	switch strings.ToLower(r) {
	case "daily":
		r = "1d"
	case "weekly":
		r = "1w"
	}
	d := domain.Interval(r).Duration()
	if d == 0 {
		return nil, fmt.Errorf("thesis: invalid rebalance %q (use 1h..1w, 1M or never)", r)
	}
	ms := d.Milliseconds()
	return func(t int64) int64 { return floorDiv(t, ms) }, nil
}

func rebalanceDuration(r string) time.Duration {
	switch strings.ToLower(r) {
	case "never", "none", "0", "hold", "bar", "always", "every":
		return 0
	case "daily":
		return 24 * time.Hour
	case "weekly":
		return 7 * 24 * time.Hour
	case "monthly", "1mo":
		return 30 * 24 * time.Hour
	}
	return domain.Interval(r).Duration()
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// ---- one-line form ----

// ParseText parses the one-line form: "long xyz:SP500 1.5x, short DOGE FARTCOIN, rebalance 1w".
//
// Clauses are separated by commas, semicolons, " and " or newlines:
//
//	long <coins|basket> [w x|w%]       one leg; several coins form an equal-weight basket
//	short [falling|rising] <...> [w x] [each w x] [lookback n]
//	rebalance 1d|1w|1M|never   interval 4h   leverage 2   2x   name "My thesis"
//
// A bare word that names a basket (memes, majors, ...) is a basket leg;
// "basket:name" forces it.
func ParseText(text string) (Spec, error) {
	var s Spec
	clauses := splitClauses(text)
	if len(clauses) == 0 {
		return s, errors.New("thesis: empty text")
	}
	inline := 0
	for _, cl := range clauses {
		toks := strings.Fields(cl)
		if len(toks) == 0 {
			continue
		}
		head := strings.ToLower(toks[0])
		args := toks[1:]
		switch head {
		case "long", "short", "buy", "sell":
			leg, coins, err := parseLeg(args)
			if err != nil {
				return Spec{}, fmt.Errorf("thesis: %q: %w", cl, err)
			}
			if len(coins) == 1 && !leg.isBasketToken(coins[0], s) {
				leg.Coin = coins[0]
			} else if len(coins) == 1 {
				leg.Basket = strings.TrimPrefix(coins[0], "basket:")
			} else {
				inline++
				name := fmt.Sprintf("%s%d", strings.ToLower(sideWord(head)), inline)
				if s.Baskets == nil {
					s.Baskets = map[string][]string{}
				}
				s.Baskets[name] = coins
				leg.Basket = name
			}
			if head == "long" || head == "buy" {
				s.Long = append(s.Long, leg)
			} else {
				s.Short = append(s.Short, leg)
			}
		case "rebalance", "every":
			if len(args) != 1 {
				return Spec{}, fmt.Errorf("thesis: %q: rebalance takes one value", cl)
			}
			s.Rebalance = args[0]
		case "interval", "on", "bars":
			if len(args) != 1 {
				return Spec{}, fmt.Errorf("thesis: %q: interval takes one value", cl)
			}
			s.Interval = domain.Interval(args[0])
		case "leverage", "lev", "gross":
			if len(args) != 1 {
				return Spec{}, fmt.Errorf("thesis: %q: leverage takes one value", cl)
			}
			v, ok := parseMult(args[0])
			if !ok {
				v2, err := strconv.ParseFloat(args[0], 64)
				if err != nil {
					return Spec{}, fmt.Errorf("thesis: bad leverage %q", args[0])
				}
				v = v2
			}
			s.Leverage = v
		case "name":
			s.Name = strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cl), toks[0])), `"'`)
		default:
			if v, ok := parseMult(toks[0]); ok && len(toks) == 1 {
				s.Leverage = v
				continue
			}
			return Spec{}, fmt.Errorf("thesis: cannot parse %q (start clauses with long, short, rebalance, interval, leverage or name)", cl)
		}
	}
	s.defaults()
	if err := s.check(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

func sideWord(h string) string {
	if h == "buy" || h == "long" {
		return "long"
	}
	return "short"
}

func (l Leg) isBasketToken(tok string, s Spec) bool {
	if strings.HasPrefix(tok, "basket:") {
		return true
	}
	if _, ok := s.Baskets[tok]; ok {
		return true
	}
	_, ok := Builtin[tok]
	return ok
}

func parseLeg(args []string) (Leg, []string, error) {
	var l Leg
	var coins []string
	for i := 0; i < len(args); i++ {
		a := strings.Trim(args[i], " ")
		la := strings.ToLower(a)
		switch {
		case la == "falling" || la == "rising":
			l.Filter = la
		case la == "each" || la == "per-name":
			if i+1 >= len(args) {
				return l, nil, errors.New("each needs a weight")
			}
			v, ok := parseMult(args[i+1])
			if !ok {
				return l, nil, fmt.Errorf("bad weight %q", args[i+1])
			}
			l.PerName = v
			i++
		case la == "lookback":
			if i+1 >= len(args) {
				return l, nil, errors.New("lookback needs a bar count")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				return l, nil, fmt.Errorf("bad lookback %q", args[i+1])
			}
			l.Lookback = n
			i++
		default:
			if v, ok := parseMult(a); ok {
				l.Weight = v
				continue
			}
			coins = append(coins, normalizeCoin(a))
		}
	}
	if len(coins) == 0 {
		return l, nil, errors.New("no coins")
	}
	return l, coins, nil
}

// normalizeCoin upper-cases an all-lowercase coin ("btc" -> "BTC",
// "xyz:sp500" -> "xyz:SP500"); mixed case such as kPEPE and basket names
// are kept.
func normalizeCoin(t string) string {
	if _, ok := Builtin[t]; ok || strings.HasPrefix(t, "basket:") || t != strings.ToLower(t) {
		return t
	}
	if i := strings.Index(t, ":"); i > 0 {
		return t[:i+1] + strings.ToUpper(t[i+1:])
	}
	return strings.ToUpper(t)
}

// parseMult parses "1.5x", "x1.5" or "150%".
func parseMult(t string) (float64, bool) {
	t = strings.TrimSpace(t)
	var num string
	div := 1.0
	switch {
	case strings.HasSuffix(t, "x") || strings.HasSuffix(t, "X"):
		num = t[:len(t)-1]
	case strings.HasPrefix(t, "x"):
		num = t[1:]
	case strings.HasSuffix(t, "%"):
		num, div = t[:len(t)-1], 100
	default:
		return 0, false
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v / div, true
}

func splitClauses(s string) []string {
	s = strings.ReplaceAll(s, ";", ",")
	s = strings.ReplaceAll(s, "\n", ",")
	s = strings.ReplaceAll(s, "|", ",")
	var out []string
	for _, p := range strings.Split(s, ",") {
		for _, q := range splitAnd(p) {
			if q = strings.TrimSpace(q); q != "" {
				out = append(out, q)
			}
		}
	}
	return out
}

// splitAnd splits on the word "and" only when it starts a new long/short
// clause ("long BTC and short DOGE").
func splitAnd(p string) []string {
	toks := strings.Fields(p)
	var out []string
	cur := []string{}
	for i, t := range toks {
		if strings.EqualFold(t, "and") && i+1 < len(toks) {
			n := strings.ToLower(toks[i+1])
			if n == "long" || n == "short" || n == "rebalance" || n == "leverage" {
				out = append(out, strings.Join(cur, " "))
				cur = cur[:0]
				continue
			}
		}
		cur = append(cur, t)
	}
	return append(out, strings.Join(cur, " "))
}

// String renders the one-line form; ParseText(s.String()) yields an
// equivalent spec (same legs, weights, rebalance, interval, leverage).
func (s Spec) String() string {
	var parts []string
	if s.Name != "" && s.Name != s.autoName() {
		parts = append(parts, "name "+s.Name)
	}
	leg := func(side string, l Leg) string {
		b := []string{side}
		if l.Filter != "" {
			b = append(b, l.Filter)
		}
		if l.Coin != "" {
			b = append(b, l.Coin)
		} else if ms, ok := s.Baskets[l.Basket]; ok {
			b = append(b, ms...)
		} else {
			b = append(b, l.Basket)
		}
		if l.Weight != 0 && l.Weight != 1 {
			b = append(b, fmtMult(l.Weight))
		}
		if l.PerName > 0 {
			b = append(b, "each", fmtMult(l.PerName))
		}
		if l.Lookback > 0 {
			b = append(b, "lookback", strconv.Itoa(l.Lookback))
		}
		return strings.Join(b, " ")
	}
	for _, l := range s.Long {
		parts = append(parts, leg("long", l))
	}
	for _, l := range s.Short {
		parts = append(parts, leg("short", l))
	}
	if s.Rebalance != "" {
		parts = append(parts, "rebalance "+s.Rebalance)
	}
	if s.Interval != "" && s.Interval != domain.I1d {
		parts = append(parts, "interval "+string(s.Interval))
	}
	if s.Leverage != 0 && math.Abs(s.Leverage-s.nominal()) > 1e-12 {
		parts = append(parts, "leverage "+fmtMult(s.Leverage))
	}
	return strings.Join(parts, ", ")
}

func fmtMult(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) + "x" }
