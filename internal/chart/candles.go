package chart

import (
	"math"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

// Style is the candle glyph technique, including the setup-rush styles.
type Style string

const (
	// setup-rush styles (drawn with a skin, see Palette).
	Classic   Style = "classic" // default: each skin's own look (terminal: solid white up, solid red down)
	Solid     Style = "solid"   // filled bodies in the skin's bull/bear inks
	Hollow    Style = "hollow"  // 1-px outlines both ways
	OHLC      Style = "ohlc"    // setup-rush "bars": open tick left, close tick right
	LineStyle Style = "line"    // monotone curve of closes, fade under it, live tail

	// Terminal-native styles (the pre-skin look; Mono and ASCII also serve
	// no-color and 7-bit outputs).
	Blocks Style = "blocks" // eighth blocks + SGR-7 trick
	Lines  Style = "lines"  // ┃ │ ╽ ╿ half-cell
	Half   Style = "half"   // ▀/▄ pixels
	Mono   Style = "mono"   // no color: hollow ║ up, ┃ down
	ASCII  Style = "ascii"  // 7-bit: O # |
	Heikin Style = "heikin" // Heikin-Ashi transform (hollow up / solid down with a skin)
)

// Styles lists every candle style, default first.
var Styles = []Style{Classic, Solid, Hollow, OHLC, LineStyle, Heikin, Blocks, Lines, Half, Mono, ASCII}

// Valid reports whether s names a known style ("" counts as the default).
func (s Style) Valid() bool {
	if s == "" {
		return true
	}
	for _, v := range Styles {
		if s == v {
			return true
		}
	}
	return false
}

// legacy reports whether s is drawn by the pre-skin renderer even when a
// skin is set (single-column glyph styles and the no-color fallbacks).
func (s Style) legacy() bool {
	return s == Lines || s == Half || s == Mono || s == ASCII
}

// ParseStyle resolves a user-typed style name, accepting setup-rush's own
// names and common aliases ("bars" is OHLC, "ha" is Heikin, "candles" is
// the default). ok is false for an unknown name.
func ParseStyle(name string) (Style, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "", "default", "candles", "candle", "candlestick", "candlesticks":
		return Classic, true
	case "bars", "bar", "ohlc-bars":
		return OHLC, true
	case "ha", "heikin-ashi", "heikinashi", "heiken", "heiken-ashi":
		return Heikin, true
	case "filled", "fill":
		return Solid, true
	case "outline", "outlined":
		return Hollow, true
	case "lines-chart", "linechart", "area":
		return LineStyle, true
	}
	s := Style(n)
	return s, s.Valid()
}

// TagShape is the outline of the price tags on the axis.
type TagShape string

const (
	TagAngled    TagShape = "angled"    // default: top-right and bottom-left corners cut (◥ … ◣)
	TagFlat      TagShape = "flat"      // half-cell rounded ends (▐ … ▌), for fonts without triangles
	TagPowerline TagShape = "powerline" // Nerd Font arrows ( … )
)

// AutoStyle picks the style for an output: ascii
// when the locale is not UTF-8 (or TERM=dumb) or on the legacy Windows
// console (conhost without Windows Terminal, which mangles braille and
// eighth blocks: RELEASE R11), hollow/filled mono when there is no color
// (NO_COLOR, ASCII or piped NoTTY profiles), classic otherwise.
func AutoStyle(p colorprofile.Profile, utf8 bool) Style {
	return autoStyle(p, utf8, runtime.GOOS, os.Getenv("WT_SESSION"))
}

func autoStyle(p colorprofile.Profile, utf8 bool, goos, wtSession string) Style {
	switch {
	case !utf8, goos == "windows" && wtSession == "":
		return ASCII
	case p == colorprofile.ASCII || p == colorprofile.NoTTY:
		return Mono
	}
	return Classic
}

// Zooms lists the supported columns-per-candle strides, narrowest first.
var Zooms = []int{1, 2, 4, 6}

// ZoomIn returns the next wider stride (capped), ZoomOut the next narrower.
func ZoomIn(z int) int {
	z = normZoom(z)
	for _, v := range Zooms {
		if v > z {
			return v
		}
	}
	return z
}

// ZoomOut returns the next narrower stride (capped at 1).
func ZoomOut(z int) int {
	z = normZoom(z)
	for i := len(Zooms) - 1; i >= 0; i-- {
		if Zooms[i] < z {
			return Zooms[i]
		}
	}
	return z
}

// normZoom snaps any value to a supported stride (0 = default 2).
func normZoom(z int) int {
	switch {
	case z <= 0:
		return 2
	case z == 1:
		return 1
	case z <= 3:
		return 2
	case z <= 5:
		return 4
	}
	return 6
}

// zoomGeometry maps a stride to (body width, gap): 1→(1,0) 2→(1,1) 4→(3,1) 6→(5,1).
func zoomGeometry(z int) (bodyW, gap int) {
	switch normZoom(z) {
	case 1:
		return 1, 0
	case 4:
		return 3, 1
	case 6:
		return 5, 1
	}
	return 1, 1
}

// Overlay is a precomputed series aligned 1:1 with the candles passed to
// Candles (NaN = no value). Used for MA/EMA/VWAP and Pine plot() outputs.
type Overlay struct {
	Label  string
	Values []float64
	Color  int // index into theme.MA (0..2)
}

// HLineKind styles horizontal price lines.
type HLineKind string

const (
	LineEntry HLineKind = "entry"
	LineLiq   HLineKind = "liq"
	LineOrder HLineKind = "order"
	LineStop  HLineKind = "stop"
	LineTP    HLineKind = "tp"
)

// HLine is a horizontal level with an axis tag (╌ line, colored tag).
type HLine struct {
	Price float64   `json:"price"`
	Label string    `json:"label"`
	Kind  HLineKind `json:"kind"`
	// From is the open time of the candle the line starts at (a position's
	// entry bar); zero spans the whole plot. With a skin, target and stop
	// zones start there too.
	From int64 `json:"from,omitempty"`
}

// Marker is a buy/sell (or Pine plotshape) marker above/below a candle.
// With a skin, a Marker with a Label is a pattern mark: a bracket under the
// lows (Buy) or over the highs, with the pattern's name, as setup-rush
// draws its setups.
type Marker struct {
	T     int64 // candle open time it attaches to
	Buy   bool  // ▲ below the low (buy) / ▼ above the high (sell)
	Label string
	From  int64 // first candle of a pattern span; 0 = the single candle T
}

// CandleOpts fully determines a candle chart frame.
type CandleOpts struct {
	Width, Height int
	Style         Style
	Zoom          int       // columns per candle: 1, 2 (default), 4, 6
	VolRows       int       // volume pane rows; -1 = auto (~18% of the plot, at least 3; 0 when Height < 16)
	VolumeUSD     bool      // label volume as USD notional v*(o+c)/2
	MAs           []int     // SMA periods computed inside (max 3 overlays total)
	EMAs          []int     // EMA periods computed inside
	Overlays      []Overlay // extra precomputed overlays
	Lines         []HLine
	Markers       []Marker
	Cursor        int  // candles back from newest visible; -1 = off
	Offset        int  // pan: newest candles hidden on the right
	Log           bool // log price scale
	ScaleOverlays bool // include overlays in autoscale (default false)
	Header        bool // OHLC/MA readout row
	// NoTitle drops the coin and interval from the header (and the
	// watermark): the card or pane around the chart already names them.
	NoTitle bool
	// QuietTag draws the live price on the axis as muted text with no
	// plate: a card's header already shows the price as its one focal
	// number, so the axis copy must not compete with it.
	QuietTag bool
	Coin     string
	Interval domain.Interval
	Location *time.Location // nil = UTC

	// TickBars draws bars that close on a trade count, volume or price
	// range instead of a clock (tick charts): the x axis is the bar index
	// and its labels are the first trade's time every few bars. Interval
	// is then only the label ("500T"), and there is no bar countdown.
	TickBars bool

	// Range is the hysteresis hook for live charts: pass the previous
	// frame's Viewport Lo/Hi and the price scale is kept while the visible
	// candles stay inside it and span at least 70% of it, so a ticking chart
	// does not "breathe". Zero (or Lo >= Hi) autoscales. Reset it on pan,
	// zoom, resize or a coin change.
	Range [2]float64
	// Now enables the bar countdown under the last-price tag (TradingView's
	// "time to close") while the newest candle is still open. Zero = off.
	// The line style also uses it to slide its head through the open bar.
	Now time.Time

	// Skin names a Palette (setup-rush's chart themes): "" is
	// DefaultPalette ("terminal"), SkinNone keeps the pre-skin rendering
	// that follows the theme.Theme argument only. An unknown name falls
	// back to the default skin.
	Skin string
	// TagShape is the axis tag outline with a skin ("" = TagAngled).
	TagShape TagShape

	// Into, when set, is a grid from an earlier frame that this frame
	// reuses: its cells are cleared and repainted, so Candles returns the
	// same pointer and allocates no new cell buffer (PERFORMANCE #8). A
	// pane keeps one grid and passes it back each frame; the returned grid
	// is only valid until the next call with the same Into. The grid also
	// remembers EMA state so a live tick does not replay the series. Pass
	// a different grid (or nil) when the series is replaced wholesale.
	Into *Grid
}

// Viewport describes which candles are visible and where, for mouse
// hit-testing and panning in the TUI.
type Viewport struct {
	First, Count int // index of the oldest visible candle in the input, count visible
	PlotX, PlotW int // plot area columns
	PlotY, PlotH int
	Stride       int
	Lo, Hi       float64 // price range

	BodyW, Gap int  // candle body width and gap (Stride = BodyW + Gap)
	Edge       int  // column just right of the newest candle's slot
	Log        bool // the price scale is logarithmic
	AxisX      int  // column of the price axis line (with a skin: the first axis column)

	// Offset is the pan actually applied: CandleOpts.Offset clamped so the
	// window stays full of candles (panning past the loaded history stops
	// at the oldest candle instead of leaving the left side blank).
	// MaxOffset is the largest pan that keeps the window full; callers
	// clamp their pan state to it.
	Offset, MaxOffset int
}

// panOffset clamps a pan of offset candles over n candles when visible
// fit: never negative, and never so far that the oldest candle leaves the
// left edge with blank plot to its left (0 when every candle fits).
func panOffset(n, offset, visible int) int {
	return clampi(offset, 0, max(n-max(visible, 1), 0))
}

// IndexAt maps a column to a candle index in the input slice (-1 if none).
// A gap column belongs to the candle on its right.
func (v Viewport) IndexAt(x int) int {
	if v.Count <= 0 || v.Stride <= 0 {
		return -1
	}
	left := v.Edge - v.Count*v.Stride
	if x < left || x >= v.Edge {
		return -1
	}
	return v.First + (x-left)/v.Stride
}

// CenterX returns the center column of input candle i (-1 if not visible).
func (v Viewport) CenterX(i int) int {
	k := i - v.First
	if k < 0 || k >= v.Count {
		return -1
	}
	return v.Edge - (v.Count-k)*v.Stride + v.Gap + v.BodyW/2
}

// PriceAt maps a plot row to the price at that row's center (NaN outside).
func (v Viewport) PriceAt(y int) float64 {
	if v.PlotH <= 0 || y < v.PlotY || y >= v.PlotY+v.PlotH || v.Hi <= v.Lo {
		return math.NaN()
	}
	f := (float64(v.PlotY+v.PlotH-1-y) + 0.5) / float64(v.PlotH)
	if v.Log && v.Lo > 0 {
		return math.Exp(math.Log(v.Lo) + f*(math.Log(v.Hi)-math.Log(v.Lo)))
	}
	return v.Lo + f*(v.Hi-v.Lo)
}

const (
	rightPad     = 2  // empty columns between the newest candle and the axis
	minChartW    = 40 // below this a sparkline replaces the chart
	minChartH    = 12
	minSkinW     = 24 // a skin keeps candles down to this width (port spec 3.3)
	minSkinH     = 8
	pricePad     = 0.06
	keepRangeMin = 0.70
)

// Candles renders a full chart frame: header, price plot, overlays, lines,
// markers, volume pane, right price axis with last-price tag, time axis,
// optional crosshair. Below ~40x12 it degrades to a one-line sparkline.
//
// Every frame is a pure function of its inputs, so it can be golden-tested
// and re-rendered on any change (≈0.5 ms at 200x60).
func Candles(cs []domain.Candle, o CandleOpts, th theme.Theme) (*Grid, Viewport) {
	o.Width, o.Height = max(o.Width, 1), max(o.Height, 1)
	if !o.Style.Valid() {
		o.Style = ""
	}
	sk := resolveSkin(o)
	if sk == nil {
		// The pre-skin renderer knows the terminal-native styles only.
		switch o.Style {
		case "", Classic, Solid, Hollow, LineStyle:
			o.Style = Blocks
		case OHLC:
			o.Style = Lines
		}
	} else {
		th = sk.p.Theme(th)
		switch o.Style {
		case "":
			o.Style = Classic
		case Blocks:
			o.Style = Solid
		}
	}
	if len(cs) == 0 {
		return emptyChart(o, th, sk), Viewport{}
	}
	if sk != nil {
		if o.Width < minSkinW || o.Height < minSkinH {
			return sparkChart(cs, o, th), Viewport{}
		}
		r := newCandleRender(cs, o, th)
		r.sk = sk
		r.layoutRush()
		r.paintRush()
		return r.g, r.viewport()
	}
	if o.Width < minChartW || o.Height < minChartH {
		return sparkChart(cs, o, th), Viewport{}
	}
	r := newCandleRender(cs, o, th)
	r.layout()
	r.paint()
	return r.g, r.viewport()
}

// candleRender holds one frame's layout state.
type candleRender struct {
	maxOffset int // largest pan that keeps the window full
	o         CandleOpts
	th        theme.Theme
	cs        charset
	real      []domain.Candle // as given
	draw      []domain.Candle // what is drawn (Heikin-Ashi transformed when asked)
	ovs       []overlaySeries
	g         *Grid

	bodyW, gap, stride   int
	headerRows, volRows  int
	plotTop, plotH       int
	volTop, axisRow      int
	plotW, axisW, avail  int
	start, end, n        int
	lo, hi               float64 // padded price range
	flo, fhi             float64 // range in scale space (log when o.Log)
	log                  bool
	ticks                []float64
	dec                  int // last price, crosshair and header readouts
	tickDec              int // axis tick labels: what the tick step needs
	loc                  *time.Location
	ivDur                time.Duration
	reserved             map[int]bool // axis rows already holding a tag
	lastRow, cursorIndex int

	// skin state (setup-rush look); nil = pre-skin rendering
	sk        *skin
	timeRows  int     // 1 time-label row under the plot, or 0
	band      int     // volume band rows inside the plot (overlay)
	noAxis    bool    // narrow: the live tag sits inside the plot
	step      float64 // grid step
	headX     int     // line style: the head's column
	liveRow   int     // row of the live tag
	labelRows map[int][][2]int
}

func newCandleRender(cs []domain.Candle, o CandleOpts, th theme.Theme) *candleRender {
	r := &candleRender{o: o, th: th, real: cs, draw: cs, cs: unicodeChars, cursorIndex: -1}
	if o.Style == ASCII {
		r.cs = asciiChars
	}
	if o.Style == Heikin {
		r.draw = HeikinAshi(cs)
	}
	r.bodyW, r.gap = zoomGeometry(o.Zoom)
	if o.Style == Lines || o.Style == Mono || o.Style == ASCII {
		// Single-column glyph styles keep their spacing but draw one column.
		r.bodyW, r.gap = 1, normZoom(o.Zoom)-1
	}
	r.stride = r.bodyW + r.gap
	r.g = NewGridFrom(o.Into, o.Width, o.Height)
	r.ivDur = o.Interval.Duration()
	if r.ivDur == 0 && !o.TickBars {
		r.ivDur = inferInterval(cs)
	}
	r.loc = o.Location
	if r.loc == nil || r.ivDur >= day {
		r.loc = time.UTC // daily+ candles open at 00:00 UTC: align and label in UTC
	}
	if o.Style != ASCII {
		r.ovs = r.buildOverlays()
	}
	return r
}

// inferInterval guesses the bar length from the median spacing of open times.
func inferInterval(cs []domain.Candle) time.Duration {
	if len(cs) < 2 {
		return time.Minute
	}
	var d []int64
	for i := 1; i < len(cs) && i < 64; i++ {
		if dt := cs[i].T - cs[i-1].T; dt > 0 {
			d = append(d, dt)
		}
	}
	if len(d) == 0 {
		return time.Minute
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return time.Duration(d[len(d)/2]) * time.Millisecond
}

// layout solves the visible window, price range, ticks and axis width. The
// axis width depends on the labels, which depend on the range, which depends
// on how many candles fit, so it iterates (it converges in two passes).
func (r *candleRender) layout() {
	o := r.o
	if o.Header {
		r.headerRows = 1
	}
	const timeRows = 2
	r.volRows = o.VolRows
	if r.volRows < 0 {
		r.volRows = 0
		if o.Height >= 16 {
			// about 18% of the plot, so price keeps the room on short panes
			avail := o.Height - r.headerRows - timeRows
			r.volRows = max(3, int(math.Round(float64(avail)*0.18)))
		}
	}
	r.plotH = o.Height - r.headerRows - r.volRows - timeRows
	if r.plotH < 4 {
		r.volRows = 0
		r.plotH = o.Height - r.headerRows - timeRows
	}
	r.plotTop = r.headerRows
	r.volTop = r.plotTop + r.plotH
	r.axisRow = r.volTop + r.volRows

	offset := clampi(o.Offset, 0, len(r.draw)-1)
	r.axisW = 10
	for pass := 0; pass < 3; pass++ {
		r.plotW = o.Width - r.axisW
		r.avail = r.plotW - rightPad
		n := max(r.avail/r.stride, 1)
		offset = panOffset(len(r.draw), o.Offset, n)
		r.end = len(r.draw) - offset
		r.start = max(r.end-n, 0)
		r.n = r.end - r.start
		r.solveRange()
		w := 0
		for _, t := range r.ticks {
			w = max(w, len(FormatPrice(t, r.tickDec)))
		}
		w = max(w, len(FormatPrice(r.real[r.end-1].C, r.dec)))
		for _, hl := range o.Lines {
			if hl.Price > 0 {
				w = max(w, len(FormatPrice(hl.Price, r.dec)))
			}
		}
		if r.volRows > 0 {
			w = max(w, len(r.volLabel(r.maxVol())))
		}
		w = max(w+3, 7) // "├ " + label + 1 pad
		if w == r.axisW {
			break
		}
		r.axisW = w
	}
	r.plotW = o.Width - r.axisW
	r.avail = r.plotW - rightPad
	r.o.Offset = offset // the applied pan (Viewport.Offset)
	r.maxOffset = panOffset(len(r.draw), len(r.draw), max(r.avail/r.stride, 1))
}

// solveRange computes the padded price range over the visible window.
func (r *candleRender) solveRange() {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, c := range r.draw[r.start:r.end] {
		lo, hi = math.Min(lo, c.L), math.Max(hi, c.H)
	}
	if r.o.ScaleOverlays {
		for _, ov := range r.ovs {
			for i := r.start; i < r.end; i++ {
				if v := ov.at(i); !math.IsNaN(v) {
					lo, hi = math.Min(lo, v), math.Max(hi, v)
				}
			}
		}
	}
	r.log = r.o.Log && lo > 0
	pricePad := pricePad
	if r.sk != nil {
		pricePad = skinPad
		// Keep a position's plan in view, as setup-rush's camera does:
		// entry, target and stop join the range when they are near it
		// (a far liquidation price does not squash the candles).
		span := hi - lo
		for _, hl := range r.o.Lines {
			if (hl.Kind == LineEntry || hl.Kind == LineTP || hl.Kind == LineStop) &&
				hl.Price >= lo-0.35*span && hl.Price <= hi+0.35*span {
				lo, hi = math.Min(lo, hl.Price), math.Max(hi, hl.Price)
			}
		}
	}
	keep := r.o.Range
	if keep[0] < keep[1] && lo >= keep[0] && hi <= keep[1] && (hi-lo) >= keepRangeMin*(keep[1]-keep[0])*(1-2*pricePad) {
		r.lo, r.hi = keep[0], keep[1]
	} else if r.log {
		llo, lhi := math.Log(lo), math.Log(hi)
		pad := (lhi - llo) * pricePad
		if pad == 0 {
			pad = 0.001
		}
		r.lo, r.hi = math.Exp(llo-pad), math.Exp(lhi+pad)
	} else {
		pad := (hi - lo) * pricePad
		if pad == 0 {
			pad = math.Max(math.Abs(hi)*0.001, 1e-9)
		}
		r.lo, r.hi = lo-pad, hi+pad
	}
	r.flo, r.fhi = r.scale(r.lo), r.scale(r.hi)
	if r.log && r.hi/r.lo > 2 {
		r.ticks = LogTicks(r.lo, r.hi, max(r.plotH/3, 2))
		r.dec = PriceDecimals(r.real[r.end-1].C, 0)
		r.tickDec = 0
		for _, t := range r.ticks {
			r.tickDec = max(r.tickDec, min(PriceDecimals(t, 0), 8))
		}
		r.dec = max(r.dec, r.tickDec)
		return
	}
	var step float64
	if r.sk != nil {
		r.ticks, step = rushTicks(r.lo, r.hi, r.plotH, r.o.Height <= cardMaxH)
	} else {
		r.ticks, step = PriceTicks(r.lo, r.hi, r.plotH, 4)
	}
	r.step = step
	r.tickDec = StepDecimals(step)
	r.dec = PriceDecimals(r.real[r.end-1].C, r.tickDec)
}

func (r *candleRender) scale(p float64) float64 {
	if r.log {
		return math.Log(p)
	}
	return p
}

// units maps a price to sub-cell units from the plot bottom (perCell per row).
func (r *candleRender) units(p float64, perCell int) int {
	if r.fhi <= r.flo {
		return 0
	}
	if r.log && p <= 0 {
		return 0
	}
	f := (r.scale(p) - r.flo) / (r.fhi - r.flo) * float64(r.plotH*perCell)
	if math.IsNaN(f) {
		return 0
	}
	return clampi(int(math.Round(f)), 0, r.plotH*perCell)
}

func (r *candleRender) rowY(row int) int { return r.plotTop + r.plotH - 1 - row }

// priceRow returns the screen row of price p, and whether p is inside the plot.
func (r *candleRender) priceRow(p float64) (int, bool) {
	if r.fhi <= r.flo || (r.log && p <= 0) {
		return 0, false
	}
	f := (r.scale(p) - r.flo) / (r.fhi - r.flo) * float64(r.plotH*8)
	inside := f >= 0 && f <= float64(r.plotH*8)
	u := clampi(int(math.Round(f)), 0, r.plotH*8)
	return r.rowY(min(u/8, r.plotH-1)), inside
}

// colOf is the body-left column of visible candle k (0 = oldest visible).
func (r *candleRender) colOf(k int) int    { return r.avail - (r.n-k)*r.stride + r.gap }
func (r *candleRender) centerOf(k int) int { return r.colOf(k) + r.bodyW/2 }

func (r *candleRender) dirColor(c domain.Candle) theme.Color {
	if c.Up() {
		return r.th.Up
	}
	return r.th.Down
}

func (r *candleRender) paint() {
	r.reserved = map[int]bool{}
	if r.o.Style != ASCII {
		r.paintGrid()
	}
	r.paintTimeAxis()
	r.paintCandles()
	r.paintOverlays()
	r.paintVolume()
	r.paintPriceAxis()
	r.paintCrosshair()
	r.paintLastPrice()
	r.paintHLines()
	r.paintMarkers()
	r.paintTickLabels()
	if r.o.Header {
		r.paintHeader()
	}
}

// paintGrid draws the faint horizontal gridlines at each price tick.
func (r *candleRender) paintGrid() {
	for _, t := range r.ticks {
		y, ok := r.priceRow(t)
		if !ok {
			continue
		}
		for x := 0; x < r.plotW; x++ {
			r.g.Set(x, y, r.cs.gridH, r.th.Grid, LGrid)
		}
	}
}

// paintTimeAxis draws the bottom axis line, wall-clock aligned labels with
// a tick mark, and a faint vertical gridline up through the price plot.
func (r *candleRender) paintTimeAxis() {
	for x := 0; x < r.plotW; x++ {
		r.g.Set(x, r.axisRow, r.cs.axisH, r.th.AxisLine, LAxis)
	}
	step := TimeStep(r.ivDur, r.stride, 9)
	lastEnd := -2
	for _, m := range r.timeMarks(step, 9) {
		k, t, lbl := m.k, m.t, m.label
		cx := r.centerOf(k)
		x0 := cx - len(lbl)/2
		if x0 <= lastEnd+1 || x0 < 0 || x0+len(lbl) > r.plotW {
			continue
		}
		r.g.Set(cx, r.axisRow, r.cs.tickDown, r.th.AxisLine, LAxis)
		col := r.th.AxisText
		if (t.Hour() == 0 && t.Minute() == 0 && step < 30*day && !r.o.TickBars) || m.major {
			col = r.th.Text // day boundaries read one step brighter
		}
		lastEnd = x0 + r.g.Text(x0, r.axisRow+1, lbl, col, LLabel) - 1
		if r.o.Style != ASCII {
			for y := r.plotTop; y < r.volTop; y++ {
				r.g.Set(cx, y, r.cs.gridV, r.th.Grid, LGrid)
			}
		}
	}
}

func (r *candleRender) paintCandles() {
	cp := &candlePainter{g: r.g, style: r.o.Style, bodyW: r.bodyW, plotH: r.plotH,
		units: r.units, rowY: r.rowY, th: r.th}
	for k := 0; k < r.n; k++ {
		c := r.draw[r.start+k]
		cp.draw(r.colOf(k), c, r.dirColor(c))
	}
}

// paintOverlays draws MA/EMA/plot lines in braille behind the candles.
func (r *candleRender) paintOverlays() {
	if len(r.ovs) == 0 || r.n == 0 {
		return
	}
	b := newBraille(r.plotW, r.plotH)
	xs := make([]int, r.n)
	ys := make([]float64, r.n)
	H := float64(4*r.plotH - 1)
	for _, ov := range r.ovs {
		b.reset()
		for k := 0; k < r.n; k++ {
			xs[k] = r.centerOf(k)*2 + 1
			v := ov.at(r.start + k)
			if math.IsNaN(v) || (r.log && v <= 0) {
				ys[k] = math.NaN()
				continue
			}
			ys[k] = (r.fhi - r.scale(v)) / (r.fhi - r.flo) * H
		}
		b.polyline(xs, ys)
		if r.sk != nil && r.band > 0 {
			// behind the volume band, as the canvas z-order puts it
			clear(b.bits[(r.plotH-r.band)*b.w:])
		}
		b.flush(r.g, 0, r.plotTop, ov.col)
	}
}

func (r *candleRender) maxVol() float64 {
	vmax := 0.0
	for _, c := range r.real[r.start:r.end] {
		vmax = math.Max(vmax, r.vol(c))
	}
	return vmax
}

func (r *candleRender) vol(c domain.Candle) float64 {
	if r.o.VolumeUSD {
		return c.V * (c.O + c.C) / 2
	}
	return c.V
}

func (r *candleRender) volLabel(v float64) string {
	if r.o.VolumeUSD {
		return "$" + CompactNum(v)
	}
	return CompactNum(v)
}

// paintVolume draws eighth-block volume bars, tinted toward the background
// so volume sits visually behind price.
func (r *candleRender) paintVolume() {
	if r.volRows <= 0 {
		return
	}
	vmax := r.maxVol()
	if vmax <= 0 {
		return
	}
	up, down := tint(r.th, r.th.Up, r.th.VolMix), tint(r.th, r.th.Down, r.th.VolMix)
	if r.th.VolMix <= 0 {
		up, down = tint(r.th, r.th.Up, 0.42), tint(r.th, r.th.Down, 0.42)
	}
	for k := 0; k < r.n; k++ {
		c := r.real[r.start+k]
		col := up
		if !c.Up() {
			col = down
		}
		v := r.vol(c)
		u := int(math.Round(v / vmax * float64(r.volRows*8)))
		if v > 0 && u == 0 {
			u = 1
		}
		for row := 0; row < r.volRows; row++ {
			f := clampi(u-8*row, 0, 8)
			if f == 0 {
				break
			}
			ch := lowerBlocks[f]
			if r.o.Style == ASCII {
				ch = '#'
				if f < 4 {
					ch = '.'
				}
			}
			for bx := 0; bx < r.bodyW; bx++ {
				r.g.Set(r.colOf(k)+bx, r.volTop+r.volRows-1-row, ch, col, LVolume)
			}
		}
	}
	r.g.Text(r.plotW+2, r.volTop, r.volLabel(vmax), r.th.Dim, LLabel)
}

// paintPriceAxis draws the right axis line and the corner joint.
func (r *candleRender) paintPriceAxis() {
	for y := r.plotTop; y < r.axisRow; y++ {
		r.g.Set(r.plotW, y, r.cs.axisV, r.th.AxisLine, LAxis)
	}
	r.g.Set(r.plotW, r.axisRow, r.cs.corner, r.th.AxisLine, LAxis)
}

// tag paints a filled axis label with SGR 7: the cell shows fill as its
// background and text in bg (zero bg = the terminal's own background, which
// stays right on transparent and light terminals and survives no-color).
func (r *candleRender) tag(x, y int, s string, w int, fill, text theme.Color) {
	for len(s) < w {
		s += " "
	}
	i := 0
	for _, ch := range s {
		if c := r.g.Set(x+i, y, ch, fill, LLabel); c != nil {
			c.BG, c.Reverse, c.Bold = text, true, true
		}
		i++
	}
}

// paintLastPrice draws the dotted last-price line and its filled axis tag,
// plus the bar countdown under it while the newest candle is open.
func (r *candleRender) paintLastPrice() {
	last := r.real[r.end-1]
	col := r.dirColor(last)
	y, _ := r.priceRow(last.C)
	if r.o.Style == Heikin {
		col = r.dirColor(r.draw[r.end-1])
	}
	r.lastRow = y
	line := tint(r.th, col, 0.6)
	for x := 0; x < r.plotW; x++ {
		if c := r.g.At(x, y); c != nil && c.Layer <= LGrid {
			r.g.Set(x, y, r.cs.last, line, LOverlay)
		}
	}
	r.tag(r.plotW, y, " "+FormatPrice(last.C, r.dec), r.axisW, col, theme.Color{})
	r.reserved[y] = true
	if r.o.Now.IsZero() || r.o.Offset > 0 || y+1 >= r.axisRow {
		return
	}
	closeAt := time.UnixMilli(last.T).Add(r.ivDur)
	if left := closeAt.Sub(r.o.Now); left > 0 && left <= r.ivDur {
		r.tag(r.plotW, y+1, " "+FormatCountdown(left), r.axisW, tint(r.th, col, 0.55), theme.Color{})
		r.reserved[y+1] = true
	}
}

// hlineColor maps a level kind to its theme color.
func (r *candleRender) hlineColor(k HLineKind) theme.Color {
	switch k {
	case LineLiq:
		return r.th.Error
	case LineStop:
		return r.th.Warn
	case LineTP:
		return r.th.Success
	case LineOrder:
		return r.th.Accent2
	}
	return r.th.Accent
}

// paintHLines draws entry/liq/order/stop/tp levels: a dashed line behind
// candles, a tag on the axis and the label at the line's left end. Levels
// outside the range pin their tag to the plot edge with an arrow.
func (r *candleRender) paintHLines() {
	for _, hl := range r.o.Lines {
		if hl.Price <= 0 || math.IsNaN(hl.Price) || math.IsInf(hl.Price, 0) {
			continue
		}
		col := r.hlineColor(hl.Kind)
		y, inside := r.priceRow(hl.Price)
		lbl := hl.Label
		if lbl == "" {
			lbl = string(hl.Kind)
		}
		if !inside {
			arrow := r.cs.up
			if r.scale(hl.Price) < r.flo {
				arrow = r.cs.down
			}
			lbl = string(arrow) + " " + lbl
		} else {
			for x := 0; x < r.plotW; x++ {
				if c := r.g.At(x, y); c != nil && c.Layer <= LOverlay {
					r.g.Set(x, y, r.cs.hline, tint(r.th, col, 0.7), LOverlay)
				}
			}
		}
		if !r.reserved[y] {
			tagCol := col
			if !inside {
				tagCol = tint(r.th, col, 0.55) // off-screen levels recede
			}
			r.tag(r.plotW, y, " "+FormatPrice(hl.Price, r.dec), r.axisW, tagCol, theme.Color{})
			r.reserved[y] = true
		}
		// The label goes on the left end only where it hides nothing.
		w := len([]rune(lbl)) + 2
		free := true
		for x := 0; x < w && free; x++ {
			if c := r.g.At(x, y); c == nil || c.Layer > LOverlay {
				free = false
			}
		}
		if free {
			r.g.Set(0, y, ' ', theme.Color{}, LLabel)
			r.g.Text(1, y, lbl, col, LLabel)
			r.g.Set(w-1, y, ' ', theme.Color{}, LLabel)
		}
	}
}

// paintMarkers draws ▲ under the low of buy candles and ▼ over the high of
// sell candles, stepping further out when the cell is taken.
func (r *candleRender) paintMarkers() {
	if len(r.o.Markers) == 0 {
		return
	}
	idx := make(map[int64]int, r.n)
	for k := 0; k < r.n; k++ {
		idx[r.draw[r.start+k].T] = k
	}
	for _, m := range r.o.Markers {
		k, ok := idx[m.T]
		if !ok {
			continue
		}
		c := r.draw[r.start+k]
		x := r.centerOf(k)
		if m.Buy {
			y, _ := r.priceRow(c.L)
			for yy := y + 1; yy < r.volTop; yy++ {
				if cell := r.g.At(x, yy); cell != nil && cell.Layer <= LOverlay {
					r.g.Set(x, yy, r.cs.up, r.th.Up, LCandle)
					break
				}
			}
			continue
		}
		y, _ := r.priceRow(c.H)
		for yy := y - 1; yy >= r.plotTop; yy-- {
			if cell := r.g.At(x, yy); cell != nil && cell.Layer <= LOverlay {
				r.g.Set(x, yy, r.cs.down, r.th.Down, LCandle)
				break
			}
		}
	}
}

// paintCrosshair tints the cursor candle's columns, draws dim guide lines
// through its center and close, and tags the axis with price and time.
func (r *candleRender) paintCrosshair() {
	if r.o.Cursor < 0 || r.o.Cursor >= r.n {
		return
	}
	k := r.n - 1 - r.o.Cursor
	r.cursorIndex = r.start + k
	c := r.real[r.cursorIndex]
	cx := r.centerOf(k)
	for y := r.plotTop; y < r.axisRow; y++ {
		for bx := 0; bx < r.bodyW; bx++ {
			if cell := r.g.At(r.colOf(k)+bx, y); cell != nil && cell.BG == (theme.Color{}) {
				cell.BG = r.th.Cursor
			}
		}
		if y < r.volTop {
			if cell := r.g.At(cx, y); cell != nil && cell.Layer <= LGrid {
				r.g.Set(cx, y, r.cs.gridV, r.th.Dim, LGrid)
			}
		}
	}
	y, _ := r.priceRow(c.C)
	for x := 0; x < r.plotW; x++ {
		if cell := r.g.At(x, y); cell != nil && cell.Layer <= LGrid {
			r.g.Set(x, y, r.cs.gridH, r.th.Dim, LOverlay)
		}
	}
	lastY, _ := r.priceRow(r.real[r.end-1].C)
	if y != lastY {
		r.tag(r.plotW, y, " "+FormatPrice(c.C, r.dec), r.axisW, r.th.AxisLine, r.th.Text)
		r.reserved[y] = true
	}
	format := "Mon 02 Jan 15:04"
	if r.ivDur >= day {
		format = "Mon 02 Jan 2006"
	}
	tl := " " + time.UnixMilli(c.T).In(r.loc).Format(format) + " "
	x0 := clampi(cx-len(tl)/2, 0, max(r.plotW-len(tl), 0))
	// Remove every time label the tag (plus one column of padding) would
	// touch, whole: never leave a fragment like "18:" beside it.
	blank := func(x int) bool {
		cell := r.g.At(x, r.axisRow+1)
		return cell == nil || cell.Ch == ' ' || cell.Ch == 0
	}
	lo, hi := max(x0-1, 0), min(x0+len(tl)+1, r.plotW)
	for lo > 0 && !blank(lo-1) && !blank(lo) {
		lo--
	}
	for hi < r.plotW && !blank(hi) && !blank(hi-1) {
		hi++
	}
	for x := lo; x < hi; x++ {
		if cell := r.g.At(x, r.axisRow+1); cell != nil {
			*cell = Cell{Ch: ' '}
		}
	}
	r.tag(x0, r.axisRow+1, tl, len(tl), r.th.AxisLine, r.th.Text)
}

// paintTickLabels writes the price labels on rows no tag has claimed.
func (r *candleRender) paintTickLabels() {
	for _, t := range r.ticks {
		y, ok := r.priceRow(t)
		if !ok || r.reserved[y] {
			continue
		}
		// keep a blank axis row above the volume pane's max label
		if r.volRows > 0 && y >= r.volTop-1 {
			continue
		}
		r.g.Set(r.plotW, y, r.cs.axisTick, r.th.AxisLine, LAxis)
		r.g.Text(r.plotW+2, y, FormatPrice(t, r.tickDec), r.th.AxisText, LLabel)
	}
}

// headerSeg is one readout segment: an optional dim key and a value.
type headerSeg struct {
	key, val string
	kc, vc   theme.Color
	prio     int
	bold     bool
}

// paintHeader draws the readout for the cursor candle (or the newest one).
// Segments are kept by priority (coin, interval, close, change, then O, H,
// L, V, overlays) but laid out in reading order, so a narrow pane keeps
// what a trader needs first. Only the change is colored by direction: the
// candles stay the one saturated thing on screen.
func (r *candleRender) paintHeader() {
	i := r.end - 1
	if r.cursorIndex >= 0 {
		i = r.cursorIndex
	}
	c := r.real[i]
	dirCol := r.dirColor(c)
	prev := c.O
	if i > 0 {
		prev = r.real[i-1].C
	}
	chg := 0.0
	if prev != 0 {
		chg = c.C/prev - 1
	}
	segs := []headerSeg{}
	if r.o.Coin != "" && !r.o.NoTitle {
		segs = append(segs, headerSeg{val: r.o.Coin, vc: r.th.Text, prio: 0, bold: true})
	}
	iv := string(r.o.Interval)
	if iv != "" && !r.o.NoTitle {
		segs = append(segs, headerSeg{val: iv, vc: r.th.AxisText, prio: 1})
	}
	segs = append(segs,
		headerSeg{key: "O", val: FormatPrice(c.O, r.dec), kc: r.th.Dim, vc: r.th.Text, prio: 20},
		headerSeg{key: "H", val: FormatPrice(c.H, r.dec), kc: r.th.Dim, vc: r.th.Text, prio: 5},
		headerSeg{key: "L", val: FormatPrice(c.L, r.dec), kc: r.th.Dim, vc: r.th.Text, prio: 6},
		headerSeg{key: "C", val: FormatPrice(c.C, r.dec), kc: r.th.Dim, vc: r.th.Text, prio: 2},
		headerSeg{val: FormatPercent(chg, 2), vc: dirCol, prio: 3},
		headerSeg{key: "V", val: r.volLabel(r.vol(c)), kc: r.th.Dim, vc: r.th.AxisText, prio: 21},
	)
	for k, ov := range r.ovs {
		if v := ov.at(i); !math.IsNaN(v) {
			// MA readouts outrank O and V: the legend is how a trader
			// tells the overlay lines apart.
			segs = append(segs, headerSeg{key: ov.label, val: FormatPrice(v, r.dec), kc: ov.col, vc: ov.col, prio: 7 + k})
		}
	}
	right := ""
	if r.o.Offset > 0 {
		right = string(r.cs.live) + " live"
	}
	avail := r.o.Width - 2
	if right != "" {
		avail -= len(right) + 2
	}
	width := func(s headerSeg) int {
		w := len([]rune(s.val))
		if s.key != "" {
			w += len(s.key) + 1
		}
		return w
	}
	// Choose by priority, then lay out in reading order.
	order := make([]int, len(segs))
	for k := range order {
		order[k] = k
	}
	sort.SliceStable(order, func(a, b int) bool { return segs[order[a]].prio < segs[order[b]].prio })
	keep := make([]bool, len(segs))
	used := 0
	for _, k := range order {
		w := width(segs[k])
		if used > 0 {
			w += 2
		}
		if used+w > avail {
			break
		}
		keep[k] = true
		used += w
	}
	x := 1
	for k, s := range segs {
		if !keep[k] {
			continue
		}
		if s.key != "" {
			x += r.g.Text(x, 0, s.key, s.kc, LLabel) + 1
		}
		x0 := x
		x += r.g.Text(x, 0, s.val, s.vc, LLabel)
		if s.bold {
			for xx := x0; xx < x; xx++ {
				r.g.At(xx, 0).Bold = true
			}
		}
		x += 2
	}
	if right != "" {
		r.g.Text(r.o.Width-1-len([]rune(right)), 0, right, r.th.Dim, LLabel)
	}
}

func (r *candleRender) viewport() Viewport {
	return Viewport{
		First: r.start, Count: r.n,
		PlotX: 0, PlotW: r.plotW, PlotY: r.plotTop, PlotH: r.plotH,
		Stride: r.stride, Lo: r.lo, Hi: r.hi,
		BodyW: r.bodyW, Gap: r.gap, Edge: r.avail, Log: r.log, AxisX: r.plotW,
		Offset: r.o.Offset, MaxOffset: r.maxOffset,
	}
}

// emptyChart is the frame shown before any candle arrives.
func emptyChart(o CandleOpts, th theme.Theme, sk *skin) *Grid {
	g := NewGridFrom(o.Into, o.Width, o.Height)
	if sk != nil {
		for i := range g.Cells {
			g.Cells[i].BG = sk.p.BG
		}
	}
	msg := "no candles"
	if o.Coin != "" {
		msg = o.Coin + " " + string(o.Interval) + "  " + msg
	}
	g.Text(max((o.Width-len(msg))/2, 0), o.Height/2, msg, th.Dim, LLabel)
	return g
}

// sparkChart is the degraded frame for tiny panes: coin, a sparkline of
// closes in the direction color, and the last price with its change.
func sparkChart(cs []domain.Candle, o CandleOpts, th theme.Theme) *Grid {
	g := NewGridFrom(o.Into, o.Width, o.Height)
	first, last := cs[0], cs[len(cs)-1]
	col := th.Up
	if last.C < first.O {
		col = th.Down
	}
	dec := PriceDecimals(last.C, 0)
	left := ""
	if o.Coin != "" {
		left = o.Coin + " "
	}
	right := " " + FormatPrice(last.C, dec)
	if first.O != 0 {
		right += " " + FormatPercent(last.C/first.O-1, 2)
	}
	sw := o.Width - len(left) - len(right)
	if sw < 4 {
		right = " " + FormatPrice(last.C, dec)
		sw = o.Width - len(left) - len(right)
	}
	if sw < 4 {
		left = ""
		sw = o.Width - len(right)
	}
	y := o.Height / 2
	x := g.Text(0, y, left, th.Text, LLabel)
	if sw > 0 {
		vals := Closes(cs)
		if len(vals) > sw*4 {
			vals = vals[len(vals)-sw*4:]
		}
		spark := Sparkline(vals, sw)
		if o.Style == ASCII {
			spark = asciiSpark(spark)
		}
		x += g.Text(x, y, spark, col, LLabel)
	}
	g.Text(x, y, right, th.Text, LLabel)
	return g
}

// asciiSpark maps eighth blocks to 7-bit glyphs.
func asciiSpark(s string) string {
	m := map[rune]rune{'▁': '_', '▂': '_', '▃': '.', '▄': '-', '▅': '-', '▆': '=', '▇': '#', '█': '#'}
	out := []rune(s)
	for i, r := range out {
		if v, ok := m[r]; ok {
			out[i] = v
		}
	}
	return string(out)
}
