package chart

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// rideOpts dresses a frame like setup-rush's ride refs: an open long with
// its target and stop, the zones from the entry bar, and a pattern mark.
func rideOpts(cs []domain.Candle, w, h int) CandleOpts {
	n := len(cs)
	entryBar := cs[n-9]
	entry := entryBar.C
	hi := entry
	for _, c := range cs[n-9:] {
		hi = max(hi, c.H)
	}
	target := hi * 1.0015 // not reached yet, like the ride refs
	risk := (target - entry) / 3
	return CandleOpts{
		Width: w, Height: h, VolRows: -1, Cursor: -1, Coin: "BTC", Interval: domain.I15m,
		Now: time.UnixMilli(cs[n-1].T).Add(9*time.Minute + 12*time.Second),
		Lines: []HLine{
			{Price: entry, Kind: LineEntry, From: entryBar.T},
			{Price: target, Kind: LineTP, Label: "target +$70", From: entryBar.T},
			{Price: entry - risk, Kind: LineStop, Label: "stop −$57", From: entryBar.T},
		},
		Markers: []Marker{{T: cs[n-12].T, From: cs[n-13].T, Buy: true, Label: "Bullish Belt Hold"}},
	}
}

// TestRushShots renders PNG previews of the setup-rush skins into
// $HLP_RUSH_SHOTS (skipped unless set). HLP_RUSH_SKINS and HLP_RUSH_STYLE
// narrow the set.
func TestRushShots(t *testing.T) {
	dir := os.Getenv("HLP_RUSH_SHOTS")
	if dir == "" {
		t.Skip("set HLP_RUSH_SHOTS=/dir")
	}
	cs := loadBTC(t)
	if p := os.Getenv("HLP_RUSH_CANDLES"); p != "" {
		cs = loadCandleFile(t, p)
	}
	w, _ := strconv.Atoi(os.Getenv("HLP_RUSH_W"))
	h, _ := strconv.Atoi(os.Getenv("HLP_RUSH_H"))
	if w == 0 {
		w, h = 72, 52
	}
	style := Style(os.Getenv("HLP_RUSH_STYLE"))
	zoom, _ := strconv.Atoi(os.Getenv("HLP_RUSH_ZOOM"))
	skins := []string{"terminal"}
	if s := os.Getenv("HLP_RUSH_SKINS"); s == "all" {
		skins = PaletteNames()
	} else if s != "" {
		skins = filepath.SplitList(s)
	}
	for _, sk := range skins {
		o := rideOpts(cs, w, h)
		o.Skin, o.Style, o.Zoom = sk, style, zoom
		if zoom == 0 {
			o.Zoom = 4
		}
		g, _ := Candles(cs, o, hlpTheme(t))
		p, _ := PaletteByName(sk)
		name := filepath.Join(dir, sk+"-"+string(style)+".png")
		f, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := newShot(10, 20, p.Theme(hlpTheme(t))).render(f, g); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

func loadCandleFile(t *testing.T, path string) []domain.Candle {
	t.Helper()
	old := samplePathOverride
	samplePathOverride = path
	defer func() { samplePathOverride = old }()
	return loadBTC(t)
}

// TestRushDocImages writes the README images (docs/images/chart-*.png) into
// $HLP_RUSH_DOCS (skipped unless set).
func TestRushDocImages(t *testing.T) {
	dir := os.Getenv("HLP_RUSH_DOCS")
	if dir == "" {
		t.Skip("set HLP_RUSH_DOCS=docs/images")
	}
	cs := loadBTC(t)
	th := hlpTheme(t)
	write := func(name string, o CandleOpts) {
		t.Helper()
		g, _ := Candles(cs, o, th)
		p, _ := PaletteByName(o.Skin)
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := newShot(10, 20, p.Theme(th)).render(f, g); err != nil {
			t.Fatal(err)
		}
	}
	hero := func(skin string, st Style) CandleOpts {
		o := rideOpts(cs, 100, 36)
		o.Skin, o.Style, o.Zoom, o.Header = skin, st, 4, true
		return o
	}
	write("chart-hero-terminal", hero("terminal", ""))
	for _, sk := range []string{"paper", "pro", "exchange", "candy", "crt", "neon", "amber", "blueprint", "ink", "graphite", "mint"} {
		write("chart-theme-"+sk, hero(sk, ""))
	}
	for _, st := range []Style{Solid, Hollow, OHLC, LineStyle, Heikin} {
		write("chart-style-"+string(st), hero("terminal", st))
	}
	inline := rideOpts(cs, 96, 16)
	inline.Zoom, inline.Lines, inline.Markers = 2, nil, nil
	write("chart-inline", inline)
	narrow := rideOpts(cs, 32, 16)
	narrow.Zoom, narrow.Lines, narrow.Markers = 2, nil, nil
	write("chart-narrow", narrow)
}
