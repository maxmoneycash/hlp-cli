package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

func series(n int) domain.Series {
	s := make(domain.Series, n)
	for i := range s {
		s[i] = domain.Point{T: int64(i) * 1000, V: float64(i)}
	}
	return s
}

func encodedLen(t *testing.T, v any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func TestCompactDropsAndDownsamplesSeries(t *testing.T) {
	d := domain.VaultDetail{
		Summary: domain.VaultSummary{Name: "Nice vault\x1b]52;c;MHhkZWFk\x07", Spark: series(1000)},
		NAV:     series(3000),
		Tearsheet: &domain.Tearsheet{
			Curve:      series(500),
			Benchmarks: []domain.Benchmark{{Name: "BTC", Curve: series(500)}},
		},
	}
	res := &Result{Card: CardVault, Data: d, Meta: domain.Meta{Source: "x", Warnings: []string{"orig"}}}
	out, err := Compact(res, CompactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out.Data)
	s := string(b)
	if strings.Contains(s, "\x1b") || strings.Contains(s, `\u001b`) || !strings.Contains(s, "Nice vault]52;c;MHhkZWFk") {
		t.Fatalf("control characters survived: %.200s", s)
	}
	m := out.Data.(map[string]any)
	if _, ok := m["nav"]; ok {
		t.Fatal("nav kept without series:true")
	}
	ts := m["tearsheet"].(map[string]any)
	if _, ok := ts["curve"]; ok {
		t.Fatal("tearsheet.curve kept")
	}
	if _, ok := ts["benchmarks"].([]any)[0].(map[string]any)["curve"]; ok {
		t.Fatal("benchmark curve kept")
	}
	if _, ok := m["summary"].(map[string]any)["spark"]; ok {
		t.Fatal("summary.spark kept without series:true")
	}
	if !out.Meta.Truncated || len(out.Meta.Warnings) < 2 || out.Meta.Warnings[0] != "orig" {
		t.Fatalf("meta: %+v", out.Meta)
	}
	if len(res.Meta.Warnings) != 1 {
		t.Fatal("Compact must not modify the input meta")
	}

	// series:true keeps the curves (downsampled).
	out, _ = Compact(res, CompactOptions{Series: true})
	nav := out.Data.(map[string]any)["nav"].([]any)
	if len(nav) != CompactMaxPoints {
		t.Fatalf("nav %d", len(nav))
	}
	if last := nav[len(nav)-1].(map[string]any)["v"].(json.Number); last != "2999" {
		t.Fatalf("last point lost: %v", last)
	}
	if spark := out.Data.(map[string]any)["summary"].(map[string]any)["spark"].([]any); len(spark) != CompactMaxPoints {
		t.Fatalf("spark %d", len(spark))
	}
}

func TestCompactKeepsScalarEquity(t *testing.T) {
	v, notes, err := CompactValue(map[string]any{"equity": 12.5, "pnl": -1, "nav": []int{1, 2}}, CompactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["equity"] == nil || m["pnl"] == nil || m["nav"] != nil || len(notes) != 1 {
		t.Fatalf("%v %v", m, notes)
	}
	// Small payloads come back untouched, without notes.
	_, notes, _ = CompactValue([]string{"a", "b"}, CompactOptions{})
	if len(notes) != 0 {
		t.Fatal(notes)
	}
}

func TestCompactSizeCap(t *testing.T) {
	type row struct {
		Time int64  `json:"time"`
		Coin string `json:"coin"`
		Note string `json:"note"`
	}
	rows := make([]row, 5000)
	for i := range rows {
		rows[i] = row{Time: int64(i), Coin: "BTC", Note: strings.Repeat("x", 40)}
	}
	res := &Result{Data: map[string]any{"fills": rows, "name": "keep"}}
	out, err := Compact(res, CompactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if n := encodedLen(t, out.Data); n > CompactMaxBytes {
		t.Fatalf("payload %d bytes", n)
	}
	m := out.Data.(map[string]any)
	fills := m["fills"].([]any)
	if len(fills) == 0 || len(fills) >= 5000 || m["name"] != "keep" {
		t.Fatalf("fills %d", len(fills))
	}
	// oldest-first rows keep the newest
	if last := fills[len(fills)-1].(map[string]any)["time"].(json.Number); last != "4999" {
		t.Fatalf("newest row dropped: %v", last)
	}
	if !out.Meta.Truncated || !strings.Contains(strings.Join(out.Meta.Warnings, " "), "data.fills kept") {
		t.Fatalf("warnings: %v", out.Meta.Warnings)
	}

	// A top-level list is cut from the head when it is not time-ordered.
	list := make([]map[string]any, 3000)
	for i := range list {
		list[i] = map[string]any{"rank": i, "name": strings.Repeat("v", 30)}
	}
	v, _, _ := CompactValue(list, CompactOptions{MaxBytes: 4 << 10})
	got := v.([]any)
	if encodedLen(t, got) > 4<<10 || got[0].(map[string]any)["rank"].(json.Number) != "0" {
		t.Fatalf("top-level cut: %d rows", len(got))
	}

	// One huge string is shortened, then fields go.
	v, notes, _ := CompactValue(map[string]any{"doc": strings.Repeat("é", 50000)}, CompactOptions{MaxBytes: 1 << 10})
	if encodedLen(t, v) > 1<<10 || len(notes) == 0 {
		t.Fatalf("string cap: %d %v", encodedLen(t, v), notes)
	}
}

func TestDownsample(t *testing.T) {
	a := make([]any, 10)
	for i := range a {
		a[i] = i
	}
	got := downsample(a, 4)
	if len(got) != 4 || got[0] != 0 || got[3] != 9 {
		t.Fatal(got)
	}
	if len(downsample(a, 20)) != 10 || downsample(a, 1)[0] != 9 {
		t.Fatal("edges")
	}
}
