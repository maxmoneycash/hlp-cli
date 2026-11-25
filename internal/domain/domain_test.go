package domain

import (
	"encoding/json"
	"testing"
)

func TestIntervals(t *testing.T) {
	for _, iv := range Intervals {
		if !iv.Valid() {
			t.Errorf("%s should be valid", iv)
		}
	}
	if Interval("6h").Valid() {
		t.Error("6h is not a Hyperliquid interval")
	}
}

// Candle JSON must keep the OPEN time under "t".
func TestCandleJSON(t *testing.T) {
	b, _ := json.Marshal(Candle{T: 1, O: 2, H: 3, L: 1, C: 2.5, V: 10})
	var c Candle
	if err := json.Unmarshal(b, &c); err != nil || c.T != 1 {
		t.Fatalf("round trip lost open time: %+v %v", c, err)
	}
}
