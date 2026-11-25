package apps

import (
	"math"

	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/chartsvg"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/pvault"
)

// Keys added to structuredContent.data for a rendering host only.
const (
	KeySVG      = "svg"      // get_candles: the chart card
	KeySparkSVG = "sparkSvg" // get_vault, get_strategy_vault: the equity curve
)

// SkinFor resolves the chart skin for views: the configured chart theme
// when it names a skin, else terminal.
func SkinFor(configured string) string {
	if p, ok := chart.PaletteByName(configured); ok {
		return p.Name
	}
	return chart.DefaultPalette
}

// Enrich adds the server-rendered SVG to structuredContent.data for a
// bound tool. Call it only when the host renders UI (HostRendersUI), so
// agents never pay the bytes. It renders from the typed result (before
// compaction), so the picture keeps every candle and point; it does nothing
// when data was reshaped into something that is not an object.
func Enrich(tool string, res *core.Result, structured map[string]any, skin string) {
	if res == nil || structured == nil {
		return
	}
	data, ok := structured["data"].(map[string]any)
	if !ok {
		return
	}
	switch tool {
	case "get_candles":
		cd, ok := res.Data.(*core.ChartData)
		if !ok || cd == nil {
			return
		}
		data[KeySVG] = chartsvg.Candles(cd.Candles, chartsvg.Options{
			Skin: skin, Coin: cd.Coin, Interval: string(cd.Interval), Lines: cd.Lines,
			Style: viewStyle(cd.Style),
		})
	case "get_vault":
		var d *domain.VaultDetail
		switch v := res.Data.(type) {
		case domain.VaultDetail:
			d = &v
		case *domain.VaultDetail:
			d = v
		}
		if d == nil {
			return
		}
		curve, base := d.NAV, 1.0
		if d.Tearsheet != nil && len(d.Tearsheet.Curve) > 1 {
			curve = d.Tearsheet.Curve
		}
		if len(curve) < 2 {
			curve, base = d.Summary.Spark, math.NaN()
		}
		if len(curve) > 1 {
			data[KeySparkSVG] = chartsvg.Spark(curve, chartsvg.SparkOptions{Width: 560, Height: 120, Skin: skin, Baseline: base, Area: true})
		}
	case "get_strategy_vault":
		t, ok := res.Data.(*pvault.Tearsheet)
		if !ok || t == nil || len(t.History) < 2 {
			return
		}
		s := make(domain.Series, 0, len(t.History))
		for _, h := range t.History {
			if h.SharePrice > 0 {
				s = append(s, domain.Point{T: h.T, V: h.SharePrice})
			}
		}
		if len(s) > 1 {
			data[KeySparkSVG] = chartsvg.Spark(s, chartsvg.SparkOptions{Width: 560, Height: 120, Skin: skin, Baseline: 1, Area: true})
		}
	}
}

// viewStyle keeps the candle styles the SVG renderer draws; terminal-only
// styles (blocks, ascii, ...) fall back to classic.
func viewStyle(s string) string {
	switch s {
	case "solid", "hollow":
		return s
	}
	return ""
}
