package pinecorpus

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The fixtures in testdata are synthetic: invented prices in TradingView's
// export layout. No TradingView export is committed to hlp.

func readFixture(t *testing.T, loc *time.Location) []Trade {
	t.Helper()
	f, err := os.Open("testdata/tv_trades_synthetic.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	trades, err := ReadTVTrades(f, loc)
	if err != nil {
		t.Fatal(err)
	}
	return trades
}

func TestReadTVTrades(t *testing.T) {
	trades := readFixture(t, time.UTC)
	if len(trades) != 3 {
		t.Fatalf("got %d trades, want 3", len(trades))
	}
	a, b, c := trades[0], trades[1], trades[2]
	if a.N != 1 || !a.Long || a.EntryPrice != 100 || a.ExitPrice != 96 || a.PnL != -4 || a.EntrySignal != "L" || a.ExitSignal != "Stop" {
		t.Errorf("trade 1 = %+v", a)
	}
	if want := time.Date(2025, 1, 1, 9, 30, 0, 0, time.UTC); !a.EntryTime.Equal(want) {
		t.Errorf("trade 1 entry time %v, want %v", a.EntryTime, want)
	}
	if b.Long || b.Qty != 1 || b.ExitTime.IsZero() || b.Open {
		t.Errorf("trade 2 = %+v", b)
	}
	if !c.Open || !c.ExitTime.IsZero() || c.Qty != 2 {
		t.Errorf("trade 3 should be open with qty 2: %+v", c)
	}
}

func TestReadTVTradesTimeZone(t *testing.T) {
	loc, err := Location("asia_taipei")
	if err != nil {
		t.Fatal(err)
	}
	if loc.String() != "Asia/Taipei" {
		t.Fatalf("Location(asia_taipei) = %s", loc)
	}
	if fixed, err := Location("utc_plus_8"); err != nil {
		t.Fatal(err)
	} else if _, off := time.Date(2025, 1, 1, 0, 0, 0, 0, fixed).Zone(); off != 8*3600 {
		t.Fatalf("utc_plus_8 offset = %d", off)
	}
	trades := readFixture(t, loc)
	// 09:30 in Taipei (UTC+8) is 01:30 UTC.
	if want := time.Date(2025, 1, 1, 1, 30, 0, 0, time.UTC); !trades[0].EntryTime.Equal(want) {
		t.Errorf("entry %v, want %v", trades[0].EntryTime, want)
	}
}

func TestReadTVTradesRejectsUnknownHeader(t *testing.T) {
	f, _ := os.Open("testdata/ohlcv_synthetic.csv")
	defer f.Close()
	if _, err := ReadTVTrades(f, time.UTC); err == nil {
		t.Fatal("expected an error for a non-trade-list CSV")
	}
}

func TestReadOHLCV(t *testing.T) {
	f, err := os.Open("testdata/ohlcv_synthetic.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bars, err := ReadOHLCV(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || bars[1].Close != 101.5 || bars[1].Volume != 12.5 {
		t.Fatalf("bars = %+v", bars)
	}
	if want := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC); !bars[0].Time.Equal(want) {
		t.Errorf("first bar %v, want %v", bars[0].Time, want)
	}
}

func TestCompare(t *testing.T) {
	want := readFixture(t, time.UTC)

	same := append([]Trade(nil), want...)
	if d := Compare(want, same, time.Minute); !d.Exact(1e-9) {
		t.Fatalf("identical lists: %s", d)
	}

	// Shift one fill price, drop a trade, add one outside the window and
	// one inside it.
	got := append([]Trade(nil), want...)
	got[0].ExitPrice = 96.096 // +0.1%
	got = append(got[:1], got[2:]...)
	got = append(got,
		Trade{Long: true, EntryTime: want[0].EntryTime.Add(-48 * time.Hour), EntryPrice: 1},
		Trade{Long: false, EntryTime: want[2].EntryTime.Add(-time.Hour), EntryPrice: 1},
	)
	d := Compare(want, got, time.Minute)
	if d.Want != 3 || d.Got != 3 || d.Matched != 2 || len(d.Missing) != 1 || len(d.Extra) != 1 {
		t.Fatalf("diff = %s", d)
	}
	if d.Missing[0].N != 2 {
		t.Errorf("missing trade %d, want 2", d.Missing[0].N)
	}
	if d.MaxExitRel < 0.0009 || d.MaxExitRel > 0.0011 {
		t.Errorf("MaxExitRel = %g, want ~0.001", d.MaxExitRel)
	}
	if d.Exact(1e-6) {
		t.Error("diff with a missing trade must not be exact")
	}
}

func TestCompareDirectionMatters(t *testing.T) {
	want := readFixture(t, time.UTC)[:1]
	got := []Trade{want[0]}
	got[0].Long = false
	if d := Compare(want, got, time.Hour); d.Matched != 0 {
		t.Fatalf("a short must not match a long: %s", d)
	}
}

func TestFetchRefusesForeignDir(t *testing.T) {
	t.Setenv(EnvCache, t.TempDir())
	keep := t.TempDir()
	if err := os.WriteFile(filepath.Join(keep, "precious"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit := "0123456789abcdef0123"
	for _, dir := range []string{keep, filepath.Join(keep, commit[:12])} {
		if err := safeFetchDir(dir, commit); err == nil {
			t.Errorf("safeFetchDir(%s) accepted a dir outside the cache", dir)
		}
	}
	cache, _ := CacheDir()
	if err := safeFetchDir(filepath.Join(cache, commit[:12]), commit); err != nil {
		t.Errorf("cache dir refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(keep, "precious")); err != nil {
		t.Fatal("foreign dir was touched")
	}
}
