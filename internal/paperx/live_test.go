package paperx

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
)

// TestLivePaperOrderOnRealBook places a paper market buy against one real
// mainnet BTC l2Book snapshot (one public /info read; nothing is signed or
// sent to /exchange). Run with HLP_LIVE=1.
func TestLivePaperOrderOnRealBook(t *testing.T) {
	if os.Getenv("HLP_LIVE") != "1" {
		t.Skip("set HLP_LIVE=1 to read one real Hyperliquid book")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := hyperliquid.New(hyperliquid.Options{})
	submitAt := time.Now().UnixMilli()
	e := newEngine(t)
	e.st.Balance = d("1000000")
	time.Sleep(e.cfg.Latency) // the book must postdate the decision
	db, err := c.Book(ctx, "BTC", hyperliquid.BookOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Asks) == 0 {
		t.Fatal("empty book")
	}
	mid := decimal.NewFromFloat(db.Mid())
	px := hyperliquid.RoundPx(mid.Mul(d("1.01")), btc.SzDecimals, btc.MaxDecimals, 1)
	o, err := e.Submit(OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.01"), Px: px, Market: true}, submitAt)
	if err != nil {
		t.Fatal(err)
	}
	e.OnBook(db, time.Now().UnixMilli())
	if o.Status != StatusFilled {
		t.Fatalf("status %s (%s)", o.Status, o.Error)
	}
	for _, f := range e.FillsFor(o.OID) {
		if f.Px.LessThan(decimal.NewFromFloat(db.Asks[0].Px)) || f.Px.GreaterThan(px) {
			t.Fatalf("fill %s outside [best ask, limit]", f.Px)
		}
	}
	v := e.ViewOrder(o)
	if v.Mode != "paper" {
		t.Fatal("mode label")
	}
	t.Logf("paper buy 0.01 BTC: avg %s, slippage %.2f bps, fee %.4f", v.AvgPx, v.Fills[0].SlippageBps, v.Fees)
}
