package hyperliquid

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func dd(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestToWire(t *testing.T) {
	for in, want := range map[string]string{
		"1670.10": "1670.1", "100.0": "100", "-0": "0", "-0.000": "0", "0.0147": "0.0147",
		"123456": "123456", "1e3": "1000", "0.00001000": "0.00001", "-2.50": "-2.5",
	} {
		if got := ToWire(dd(in)); got != want {
			t.Errorf("ToWire(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestRoundPxValidPx(t *testing.T) {
	cases := []struct {
		px       string
		sz, max  int
		dir      int
		want     string
		validRaw bool
	}{
		{"1670.10", 4, MaxDecimalsPerp, 0, "1670.1", true},
		{"100.0", 0, MaxDecimalsPerp, 0, "100", true},
		{"123456", 0, MaxDecimalsPerp, 0, "123456", true},           // integers always valid
		{"12345.6", 0, MaxDecimalsPerp, 0, "12346", false},          // 6 sig figs
		{"12345.6", 0, MaxDecimalsPerp, -1, "12345", false},         // floor
		{"12345.6", 0, MaxDecimalsPerp, +1, "12346", false},         // ceil
		{"0.123456", 0, MaxDecimalsPerp, 0, "0.12346", false},       // sig figs
		{"0.000012345", 0, MaxDecimalsPerp, 0, "0.000012", false},   // decimals cap 6
		{"0.000012345", 0, MaxDecimalsSpot, 0, "0.00001235", false}, // spot: 8 decimals max
		{"1.23456", 2, MaxDecimalsPerp, 0, "1.2346", false},         // 6-2=4 decimals
		{"65392.5", 5, MaxDecimalsPerp, 0, "65393", false},          // BTC: 6 sig figs, half away from zero
		{"65392.5", 5, MaxDecimalsPerp, -1, "65392", false},         // sells round down
		{"6539.25", 5, MaxDecimalsPerp, 0, "6539.3", false},         // 6-5 = 1 decimal
		{"0.00004961", 0, MaxDecimalsSpot, 0, "0.00004961", true},
		{"0.000049615", 0, MaxDecimalsSpot, 0, "0.00004962", false},
	}
	for _, c := range cases {
		got := RoundPx(dd(c.px), c.sz, c.max, c.dir)
		if ToWire(got) != c.want {
			t.Errorf("RoundPx(%s,sz=%d,max=%d,dir=%d) = %s, want %s", c.px, c.sz, c.max, c.dir, ToWire(got), c.want)
		}
		if v := ValidPx(dd(c.px), c.sz, c.max); v != c.validRaw {
			t.Errorf("ValidPx(%s,sz=%d,max=%d) = %v, want %v", c.px, c.sz, c.max, v, c.validRaw)
		}
		if !ValidPx(got, c.sz, c.max) && !got.IsZero() {
			t.Errorf("RoundPx result %s is not valid", got)
		}
	}
	if ValidPx(dd("0"), 0, 6) || ValidPx(dd("-5"), 0, 6) {
		t.Error("non-positive prices must be invalid")
	}
	lo, hi := NearestValidPx(dd("12345.6"), 0, MaxDecimalsPerp)
	if ToWire(lo) != "12345" || ToWire(hi) != "12346" {
		t.Errorf("nearest %s %s", lo, hi)
	}
}

func TestFloorSz(t *testing.T) {
	if got := ToWire(FloorSz(dd("0.123456"), 5)); got != "0.12345" {
		t.Error(got)
	}
	if got := ToWire(FloorSz(dd("9.99"), 0)); got != "9" {
		t.Error(got)
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(100, time.Second)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	ctx := context.Background()
	if err := l.Wait(ctx, 60); err != nil {
		t.Fatal(err)
	}
	if a := l.Available(); a != 40 {
		t.Fatalf("available %v", a)
	}
	l.Charge(50)
	if a := l.Available(); a != -10 {
		t.Fatalf("after charge %v", a)
	}
	now = now.Add(500 * time.Millisecond) // +50
	if a := l.Available(); a != 40 {
		t.Fatalf("after refill %v", a)
	}
	now = now.Add(time.Hour)
	if a := l.Available(); a != 100 {
		t.Fatalf("cap %v", a)
	}
	// Blocking: real clock, tiny bucket.
	l2 := NewLimiter(10, 100*time.Millisecond)
	_ = l2.Wait(ctx, 10)
	start := time.Now()
	if err := l2.Wait(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < 30*time.Millisecond {
		t.Fatalf("did not wait: %v", el)
	}
	// Context cancellation.
	_ = l2.Wait(ctx, 10)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
	defer cancel()
	if err := l2.Wait(cctx, 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}
	// Over-capacity weight is clamped, not stuck forever.
	l3 := NewLimiter(5, 10*time.Millisecond)
	if err := l3.Wait(ctx, 500); err != nil {
		t.Fatal(err)
	}
	if Weight("l2Book") != 2 || Weight("userRole") != 60 || Weight("meta") != 20 {
		t.Fatal("weights")
	}
}
