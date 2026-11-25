package core_test

import (
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/paperx"
)

// The fixture BTC book: bids 73499x1 73498x2 73497x3, asks 73501x1
// 73502x2 73503x4.

// Paper orders obey the live risk caps; the default max_notional ($1000)
// is raised so a walk can cross levels.
func paperEnv(t *testing.T) *core.Env {
	env := coretest.Env(t, nil)
	env.Config.Risk.MaxNotionalUSD = 1e6
	return env
}

func TestPaperOrderMarketWalksLiveBook(t *testing.T) {
	env := paperEnv(t)
	res := mustInvoke(t, env, "paper_order", `{"side":"buy","coin":"btc","size":"1.5"}`)
	if res.Meta.Mode != "paper" || res.Meta.Source != "paper" {
		t.Fatalf("meta %+v", res.Meta)
	}
	d := res.Data.(*core.PaperOrderData)
	o := d.Order
	if d.Mode != "paper" || o.Mode != "paper" || o.Status != paperx.StatusFilled || o.Filled != "1.5" || o.Type != "market" {
		t.Fatalf("order %+v", o)
	}
	if len(o.Fills) != 2 || o.Fills[0].Px != "73501" || o.Fills[1].Px != "73502" || o.Fills[1].Sz != "0.5" {
		t.Fatalf("fills %+v", o.Fills)
	}
	if o.Fills[0].Maker || o.Fills[0].SlippageBps <= 0 {
		t.Fatalf("fill %+v", o.Fills[0])
	}
	// a second market order right away sees the liquidity it took
	res = mustInvoke(t, env, "paper_order", `{"side":"buy","coin":"BTC","size":"1"}`)
	o = res.Data.(*core.PaperOrderData).Order
	var at2 string
	for _, f := range o.Fills {
		if f.Px == "73501" {
			t.Fatalf("filled again at the level it emptied: %+v", o.Fills)
		}
		at2 = f.Px
	}
	if at2 == "" || o.Fills[0].ImpactBps <= 0 {
		t.Fatalf("no impact reported: %+v", o.Fills)
	}

	// account paper:default reads it like an address
	acct := mustInvoke(t, env, "get_account", `{"address":"paper:default"}`)
	a := acct.Data.(domain.Account)
	if acct.Meta.Mode != "paper" || a.Role != "paper" || len(a.Positions) != 1 || a.Positions[0].Size != 2.5 {
		t.Fatalf("account %+v meta %+v", a, acct.Meta)
	}
	fills := mustInvoke(t, env, "get_fills", `{"address":"paper:default"}`)
	if fs := fills.Data.([]domain.Fill); len(fs) < 3 || fills.Meta.Mode != "paper" {
		t.Fatalf("fills %+v", fs)
	}
	sum := mustInvoke(t, env, "paper_account", `{}`).Data.(*paperx.Summary)
	if sum.Mode != "paper" || sum.Fees <= 0 || sum.Account.Positions[0].LiqPx <= 0 && sum.Account.Positions[0].Leverage > 1 {
		t.Fatalf("summary %+v", sum)
	}
}

func TestPaperOrderRulesAndQueue(t *testing.T) {
	env := paperEnv(t)
	// Alo crossing: Hyperliquid's own text, usage exit
	_, err := invoke(t, env, "paper_order", `{"side":"buy","coin":"BTC","size":"0.1","px":"73501","tif":"Alo"}`)
	if code(err) != core.CodeUsage || !strings.Contains(err.Error(), "Post only order would have immediately matched, bbo was 73499@73501. asset=0") {
		t.Fatalf("alo: %v", err)
	}
	// $10 minimum
	_, err = invoke(t, env, "paper_order", `{"side":"buy","coin":"BTC","size":"0.0001","px":"73000"}`)
	if err == nil || !strings.Contains(err.Error(), "Order must have minimum value of $10.") {
		t.Fatalf("min: %v", err)
	}
	// off-grid price
	_, err = invoke(t, env, "paper_order", `{"side":"buy","coin":"BTC","size":"0.01","px":"73000.5"}`)
	if err == nil || !strings.Contains(err.Error(), "Price must be divisible by tick size.") {
		t.Fatalf("tick: %v", err)
	}
	// resting Alo at the bid queues behind the 1 BTC shown there
	res := mustInvoke(t, env, "paper_order", `{"side":"buy","coin":"BTC","size":"0.1","px":"73499","tif":"Alo"}`)
	o := res.Data.(*core.PaperOrderData).Order
	if o.Status != paperx.StatusOpen || o.QueueAhead != "1" {
		t.Fatalf("resting %+v", o)
	}
	acct := mustInvoke(t, env, "get_account", `{"address":"paper:default"}`).Data.(domain.Account)
	if len(acct.OpenOrders) != 1 || acct.OpenOrders[0].OID != o.OID {
		t.Fatalf("open orders %+v", acct.OpenOrders)
	}
	c := mustInvoke(t, env, "paper_cancel", `{"oid":`+itoa(o.OID)+`}`)
	if cd := c.Data.(*core.PaperOrderData); len(cd.Canceled) != 1 || c.Meta.Mode != "paper" {
		t.Fatalf("cancel %+v", cd)
	}
	if _, err := invoke(t, env, "paper_cancel", `{"oid":`+itoa(o.OID)+`}`); code(err) != core.CodeNotFound {
		t.Fatalf("second cancel: %v", err)
	}
}

func TestOrderPaperFlagAndModeLabels(t *testing.T) {
	env := paperEnv(t)
	res := mustInvoke(t, env, "place_order", `{"side":"sell","coin":"BTC","size":"0.2","paper":true,"paperAccount":"alt"}`)
	pv := res.Data.(*core.OrderPreview)
	if pv.Paper == nil || pv.Paper.Account != "paper:alt" || pv.Paper.Order.Status != paperx.StatusFilled || res.Meta.Mode != "paper" {
		t.Fatalf("preview %+v meta %+v", pv, res.Meta)
	}
	if len(pv.Envelope) != 0 {
		t.Fatal("a paper order produced an exchange envelope")
	}
	// live results say so
	if live := mustInvoke(t, env, "get_orderbook", `{"coin":"BTC"}`); live.Meta.Mode != "live" {
		t.Fatalf("live meta %+v", live.Meta)
	}
	// unknown paper account
	if _, err := invoke(t, env, "get_account", `{"address":"paper:nobody"}`); code(err) != core.CodeNotFound {
		t.Fatalf("missing account: %v", err)
	}
	// reset an existing account needs yes
	if _, err := invoke(t, env, "paper_reset", `{"account":"alt"}`); code(err) != core.CodeConfirmRequired {
		t.Fatalf("reset without yes: %v", err)
	}
	r := mustInvoke(t, env, "paper_reset", `{"account":"alt","balance":500,"yes":true}`)
	if s := r.Data.(*core.PaperOrderData).Summary; s.Equity != 500 || len(s.Account.Positions) != 0 {
		t.Fatalf("reset %+v", s)
	}
	// spot is not paper-tradable (perps only)
	if _, err := invoke(t, env, "paper_order", `{"side":"buy","coin":"@107","size":"1"}`); code(err) != core.CodeUsage {
		t.Fatalf("spot: %v", err)
	}
}

func TestPaperOrderObeysRiskCaps(t *testing.T) {
	env := coretest.Env(t, nil)
	env.Config.Risk.MaxNotionalUSD = 100
	_, err := invoke(t, env, "paper_order", `{"side":"buy","coin":"BTC","size":"0.01"}`)
	if code(err) != core.CodeSafety || !strings.Contains(err.Error(), "max_notional") {
		t.Fatalf("risk cap: %v", err)
	}
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
