package config

import (
	"slices"
	"testing"

	"github.com/maxmoneycash/hlp/internal/chart"
)

// TestChartNamesInSync keeps the chart.style and chart.theme enumerations
// equal to the chart package's styles and skins.
func TestChartNamesInSync(t *testing.T) {
	want := append(chart.PaletteNames(), chart.SkinNone)
	if !slices.Equal(ChartThemes, want) {
		t.Errorf("ChartThemes = %v, want %v", ChartThemes, want)
	}
	var styles []string
	for _, s := range chart.Styles {
		styles = append(styles, string(s))
	}
	if !slices.Equal(ChartStyles, styles) {
		t.Errorf("ChartStyles = %v, want %v", ChartStyles, styles)
	}
}
