package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestSplitFillsTotal(t *testing.T) {
	for _, tc := range []struct {
		total int
		w     []int
	}{{100, []int{5, 2}}, {97, []int{2, 3, 2}}, {7, []int{1, 1, 1}}, {3, []int{9, 1, 1}}} {
		got := split(tc.total, tc.w)
		sum := 0
		for _, v := range got {
			if v < 1 {
				t.Errorf("split(%d, %v) = %v: a part under 1", tc.total, tc.w, got)
			}
			sum += v
		}
		if sum != tc.total {
			t.Errorf("split(%d, %v) = %v sums to %d", tc.total, tc.w, got, sum)
		}
	}
}

// withDashboards swaps the built-in layouts for ones the fake registry can
// fill (chart, vaults, vault).
func withDashboards(t *testing.T, ds []dashSpec) {
	old := dashboards
	dashboards = ds
	t.Cleanup(func() { dashboards = old })
}

func TestDashFillsTheScreen(t *testing.T) {
	withDashboards(t, []dashSpec{
		{name: "one", title: "one", coin: "BTC", rows: []dashRow{
			{h: 3, tiles: []dashTile{{"chart {coin} 1h", 3}, {"vaults", 2}}},
			{h: 2, tiles: []dashTile{{"vault hlp", 1}, {"chart ETH", 1}}},
		}},
		{name: "two", title: "two", rows: []dashRow{{h: 1, tiles: []dashTile{{"vaults", 1}}}}},
	})
	f := &fakes{stream: &fakeStream{}}
	m := newTestModel(t, f, false)
	const W, H = 160, 44
	d := start(t, m, W, H)
	d.typeText("/dash one HYPE")
	d.send(key("enter"))
	d.drain()

	p := m.topPane()
	if p == nil || p.kind != paneDash || len(p.tiles) != 4 {
		t.Fatalf("top pane %+v, want a dashboard of 4 tiles", p)
	}
	for i, tl := range p.tiles {
		if tl.res == nil || tl.err != nil {
			t.Errorf("tile %d (%s): res %v err %v", i, tl.label, tl.res, tl.err)
		}
	}
	if p.tiles[0].label != "chart HYPE 1h" {
		t.Errorf("coin not substituted: %q", p.tiles[0].label)
	}
	lines := m.render()
	if len(lines) != H {
		t.Fatalf("%d lines, want %d", len(lines), H)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > W {
			t.Errorf("line %d is %d cells wide, over %d", i, w, W)
		}
	}
	s := d.screen()
	for _, want := range []string{"ONE HYPE", "HYPE", "1-9 open"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}

	// 1 opens the first tile full screen; esc comes back to the grid.
	d.send(key("1"))
	if top := m.topPane(); top == nil || top.kind != paneChart {
		t.Fatalf("1 opened %+v, want the chart", top)
	}
	d.send(key("esc"))
	if top := m.topPane(); top != p {
		t.Fatal("esc did not return to the dashboard")
	}

	// tab replaces the dashboard with the next one instead of stacking.
	d.send(tea.KeyPressMsg{Code: tea.KeyTab})
	d.drain()
	if len(m.panes) != 1 || m.topPane().dash.name != "two" {
		t.Fatalf("after tab: %d panes, top %q", len(m.panes), m.topPane().dash.name)
	}
}

func TestDashUnknownListsNames(t *testing.T) {
	withDashboards(t, []dashSpec{{name: "one", rows: []dashRow{{h: 1, tiles: []dashTile{{"vaults", 1}}}}}})
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 120, 40)
	d.typeText("/dash nope")
	d.send(key("enter"))
	if m.topPane() != nil {
		t.Fatal("an unknown dashboard opened a pane")
	}
	if s := d.screen(); !strings.Contains(s, "one") {
		t.Errorf("names not listed:\n%s", s)
	}
}

func TestDashTileErrorStaysInItsTile(t *testing.T) {
	withDashboards(t, []dashSpec{{name: "one", rows: []dashRow{{h: 1, tiles: []dashTile{{"nosuch", 1}, {"vaults", 1}}}}}})
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 120, 40)
	d.typeText("/dash one")
	d.send(key("enter"))
	d.drain()
	p := m.topPane()
	if p == nil || p.tiles[0].err == nil || p.tiles[1].res == nil {
		t.Fatalf("want tile 1 failed and tile 2 filled, got %+v", p)
	}
	if len(m.render()) != 40 {
		t.Fatal("a failed tile broke the frame height")
	}
}
