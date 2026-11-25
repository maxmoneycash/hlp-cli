package config

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestRiskValidateDefaults(t *testing.T) {
	if err := Defaults().Risk.Validate(); err != nil {
		t.Fatal(err)
	}
	r := RiskConfig{MaxNotionalUSD: math.Inf(1), MaxLeverage: 5, DailyLossUSD: math.NaN()}
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "max_notional_usd") || !strings.Contains(err.Error(), "daily_loss_usd") {
		t.Errorf("inf/NaN caps accepted: %v", err)
	}
}

func TestAllowsCoin(t *testing.T) {
	r := RiskConfig{AllowedCoins: []string{"BTC", "xyz:SP500"}}
	for coin, want := range map[string]bool{"btc": true, "BTC": true, "XYZ:sp500": true, "ETH": false, "": false} {
		if r.AllowsCoin(coin) != want {
			t.Errorf("AllowsCoin(%q) != %v", coin, want)
		}
	}
	if !(RiskConfig{}).AllowsCoin("ANY") {
		t.Error("empty list should allow all")
	}
}

func TestCheckOrder(t *testing.T) {
	r := Defaults().Risk
	r.AllowedCoins = []string{"BTC", "ETH"}
	cases := []struct {
		o    OrderCheck
		rule string
	}{
		{OrderCheck{Coin: "BTC", NotionalUSD: 500, Leverage: 3}, ""},
		{OrderCheck{Coin: "BTC", NotionalUSD: 1000, Leverage: 5}, ""},
		{OrderCheck{Coin: "BTC", NotionalUSD: 1000.01}, "max_notional_usd"},
		{OrderCheck{Coin: "BTC", NotionalUSD: math.Inf(1)}, "max_notional_usd"},
		{OrderCheck{Coin: "BTC", NotionalUSD: math.NaN()}, "max_notional_usd"},
		{OrderCheck{Coin: "BTC", NotionalUSD: -1}, "max_notional_usd"},
		{OrderCheck{Coin: "BTC", NotionalUSD: 10, Leverage: 6}, "max_leverage"},
		{OrderCheck{Coin: "DOGE", NotionalUSD: 10}, "allowed_coins"},
		{OrderCheck{Coin: "BTC", NotionalUSD: 10, RealizedLossUSD: 200}, "daily_loss_usd"},
		{OrderCheck{Coin: "DOGE", NotionalUSD: 1e9, Leverage: 50, RealizedLossUSD: 1e6, ReduceOnly: true}, ""},
	}
	for _, tc := range cases {
		err := r.CheckOrder(tc.o)
		if tc.rule == "" {
			if err != nil {
				t.Errorf("%+v: unexpected %v", tc.o, err)
			}
			continue
		}
		var v *RiskViolation
		if !errors.As(err, &v) || v.Rule != tc.rule {
			t.Errorf("%+v: got %v, want rule %s", tc.o, err, tc.rule)
		}
	}
	err := r.CheckOrder(OrderCheck{Coin: "BTC", NotionalUSD: 2500})
	if err == nil || err.Error() != "risk cap max_notional_usd: $2500.00 exceeds limit $1000.00" {
		t.Errorf("message: %v", err)
	}
}
