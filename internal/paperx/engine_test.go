package paperx

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var btc = Spec{Coin: "BTC", AssetID: 0, SzDecimals: 5, MaxDecimals: 6, MaxLeverage: 40}

// lv builds levels from "px:sz" pairs.
func lv(pairs ...string) []domain.Level {
	var out []domain.Level
	for _, p := range pairs {
		a, b, _ := strings.Cut(p, ":")
		out = append(out, domain.Level{Px: d(a).InexactFloat64(), Sz: d(b).InexactFloat64()})
	}
	return out
}

func book(bids, asks []domain.Level) domain.Book {
	return domain.Book{Coin: "BTC", Bids: bids, Asks: asks}
}

// std is a thin BTC book around 100000.
func std() domain.Book {
	return book(lv("99999:1", "99998:2", "99990:5"), lv("100001:0.5", "100002:1", "100010:3"))
}

func newEngine(t *testing.T) *Engine {
	t.Helper()
	e := New(NewState("t", d("100000"), 0), Config{Latency: 200 * time.Millisecond})
	e.SetSpec(btc)
	return e
}

func mustSubmit(t *testing.T, e *Engine, r OrderReq, now int64) *Order {
	t.Helper()
	o, err := e.Submit(r, now)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return o
}

func TestRejectionsUseHyperliquidText(t *testing.T) {
	e := newEngine(t)
	cases := []struct {
		r    OrderReq
		code string
		msg  string
	}{
		{OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.001"), Px: d("100000.5")}, "TICK", "Price must be divisible by tick size. asset=0"},
		{OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.000001"), Px: d("100000")}, "SIZE", "Order has invalid size. asset=0"},
		{OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.00009"), Px: d("100000")}, "MIN_VALUE", "Order must have minimum value of $10. asset=0"},
		{OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.001"), Px: d("100000"), Leverage: 41}, "LEVERAGE", "Invalid leverage value. asset=0"},
		{OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.001"), Px: d("100000"), TP: d("99000")}, "TPSL", "Invalid TP/SL price. asset=0"},
	}
	for _, c := range cases {
		o, err := e.Submit(c.r, 1)
		var rej *Reject
		if !errors.As(err, &rej) || rej.Code != c.code || rej.Msg != c.msg {
			t.Errorf("%+v: got %v", c.r, err)
			continue
		}
		if o.Status != StatusRejected {
			t.Errorf("status %s", o.Status)
		}
	}
	// reduce-only below $10 is fine statically (it is exempt from the minimum)
	if _, err := e.Submit(OrderReq{Coin: "BTC", Side: domain.Sell, Sz: d("0.00001"), Px: d("100000"), ReduceOnly: true}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Submit(OrderReq{Coin: "ETH", Side: domain.Buy, Sz: d("1"), Px: d("1")}, 1); err == nil {
		t.Fatal("unknown market accepted")
	}
}

func TestLatencyGatesTheBook(t *testing.T) {
	e := newEngine(t)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.1"), Px: d("100010"), TIF: Ioc}, 1000)
	e.OnBook(std(), 1100) // inside the 200 ms latency: the decision could not have seen it
	if o.Status != StatusPending {
		t.Fatalf("filled before latency: %s", o.Status)
	}
	e.OnBook(std(), 1200)
	if o.Status != StatusFilled || !o.Filled.Equal(d("0.1")) {
		t.Fatalf("status %s filled %s", o.Status, o.Filled)
	}
	f := e.FillsFor(o.OID)
	if len(f) != 1 || !f[0].Px.Equal(d("100001")) || f[0].Maker {
		t.Fatalf("fills %+v", f)
	}
	// taker fee 4.5 bps on 10000.1
	if !f[0].Fee.Equal(d("4.500045")) {
		t.Fatalf("fee %s", f[0].Fee)
	}
}

func TestIocWalksAndCancelsRemainderNeverPastDepth(t *testing.T) {
	e := newEngine(t)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("10"), Px: d("100010"), TIF: Ioc, Market: true}, 0)
	e.OnBook(std(), 200)
	if o.Status != StatusCanceled || !o.Filled.Equal(d("4.5")) || !o.Sz.Equal(d("5.5")) {
		t.Fatalf("status %s filled %s rest %s", o.Status, o.Filled, o.Sz)
	}
	fs := e.FillsFor(o.OID)
	if len(fs) != 3 || !fs[2].Px.Equal(d("100010")) {
		t.Fatalf("fills %d", len(fs))
	}
	// VWAP (0.5*100001 + 1*100002 + 3*100010)/4.5 vs mid 100000
	if fs[0].SlippageBps <= 0 {
		t.Fatalf("slippage %v", fs[0].SlippageBps)
	}
	p := e.State().Positions["BTC"]
	if !p.Szi.Equal(d("4.5")) {
		t.Fatalf("position %s", p.Szi)
	}
}

func TestIocNoMatchError(t *testing.T) {
	e := newEngine(t)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.1"), Px: d("99000"), TIF: Ioc}, 0)
	e.OnBook(std(), 200)
	if o.Status != StatusCanceled || o.Error != "Order could not immediately match against any resting orders. asset=0" {
		t.Fatalf("%s %q", o.Status, o.Error)
	}
}

func TestAloRejectedWhenCrossing(t *testing.T) {
	e := newEngine(t)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.1"), Px: d("100005"), TIF: Alo}, 0)
	e.OnBook(std(), 200)
	if o.Status != StatusRejected || o.Error != "Post only order would have immediately matched, bbo was 99999@100001. asset=0" {
		t.Fatalf("%s %q", o.Status, o.Error)
	}
	if len(e.State().Fills) != 0 {
		t.Fatal("ALO filled as taker")
	}
	ok := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.1"), Px: d("99999"), TIF: Alo}, 300)
	e.OnBook(std(), 500)
	if ok.Status != StatusOpen || !ok.Queue.Equal(d("1")) {
		t.Fatalf("alo rest: %s queue %s", ok.Status, ok.Queue)
	}
}

func TestGtcTakesWhatCrossesAndRestsTheRest(t *testing.T) {
	e := newEngine(t)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("1"), Px: d("100001")}, 0)
	e.OnBook(std(), 200)
	if o.Status != StatusOpen || !o.Filled.Equal(d("0.5")) || !o.Sz.Equal(d("0.5")) {
		t.Fatalf("status %s filled %s", o.Status, o.Filled)
	}
	if !o.Queue.IsZero() {
		t.Fatalf("queue at a new level should be 0, got %s", o.Queue)
	}
}

func TestQueuePositionFillsByTradedVolume(t *testing.T) {
	e := newEngine(t)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.5"), Px: d("99999")}, 0)
	e.OnBook(std(), 200)
	if o.Status != StatusOpen || !o.Queue.Equal(d("1")) {
		t.Fatalf("queue %s", o.Queue)
	}
	// a sell print of 0.6 at our price eats queue first
	e.OnTrades([]domain.Trade{{Coin: "BTC", Side: domain.Sell, Px: 99999, Sz: 0.6}}, 300)
	if !o.Queue.Equal(d("0.4")) || !o.Filled.IsZero() {
		t.Fatalf("queue %s filled %s", o.Queue, o.Filled)
	}
	// a print on the other side does nothing
	e.OnTrades([]domain.Trade{{Coin: "BTC", Side: domain.Buy, Px: 100001, Sz: 5}}, 310)
	// 0.6 more: 0.4 queue, then 0.2 fills us (partial, maker)
	e.OnTrades([]domain.Trade{{Coin: "BTC", Side: domain.Sell, Px: 99999, Sz: 0.6}}, 400)
	if !o.Filled.Equal(d("0.2")) || o.Status != StatusOpen {
		t.Fatalf("filled %s status %s", o.Filled, o.Status)
	}
	fs := e.FillsFor(o.OID)
	if !fs[0].Maker || fs[0].QueueWaitMs != 200 || !fs[0].Fee.Equal(d("0.2").Mul(d("99999")).Mul(d("1.5")).Div(d("10000"))) {
		t.Fatalf("fill %+v", fs[0])
	}
	// a print THROUGH our price fills up to the print's size
	e.OnTrades([]domain.Trade{{Coin: "BTC", Side: domain.Sell, Px: 99998, Sz: 0.1}}, 500)
	if !o.Filled.Equal(d("0.3")) {
		t.Fatalf("through-print fill %s", o.Filled)
	}
	e.OnTrades([]domain.Trade{{Coin: "BTC", Side: domain.Sell, Px: 99990, Sz: 9}}, 600)
	if o.Status != StatusFilled || !o.Filled.Equal(d("0.5")) {
		t.Fatalf("final %s %s", o.Status, o.Filled)
	}
}

func TestQueueShrinksOnUnexplainedCancels(t *testing.T) {
	e := newEngine(t)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.5"), Px: d("99998")}, 0)
	e.OnBook(std(), 200) // level 2, queue 2
	// 0.5 traded at the level, and the level now shows 0.75: 0.75 of the
	// 1.5 that should be left was cancelled -> queue halves pro rata
	e.OnTrades([]domain.Trade{{Coin: "BTC", Side: domain.Sell, Px: 99998, Sz: 0.5}}, 250)
	if !o.Queue.Equal(d("1.5")) {
		t.Fatalf("after print %s", o.Queue)
	}
	e.OnBook(book(lv("99999:1", "99998:0.75", "99990:5"), lv("100001:0.5")), 300)
	if !o.Queue.Equal(d("0.75")) {
		t.Fatalf("after cancels %s", o.Queue)
	}
}

func TestSelfImpactDepletesAndRefills(t *testing.T) {
	e := newEngine(t)
	a := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.5"), Px: d("100001"), TIF: Ioc}, 0)
	e.OnBook(std(), 200)
	if !a.Filled.Equal(d("0.5")) {
		t.Fatal(a.Filled)
	}
	// immediately again: the real book still shows 0.5 at 100001, but we took it
	b := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.5"), Px: d("100002"), TIF: Ioc}, 210)
	e.OnBook(std(), 410)
	// 210 ms of a 15 s half-life gave back ~1% of the take; the rest
	// must come from the next level
	fs := e.FillsFor(b.OID)
	at1 := decimal.Zero
	for _, f := range fs {
		if f.Px.Equal(d("100001")) {
			at1 = at1.Add(f.Sz)
		}
	}
	if at1.GreaterThan(d("0.01")) || !b.Filled.Equal(d("0.5")) {
		t.Fatalf("second order did not see a thinner book: %+v", fs)
	}
	if fs[0].ImpactBps <= 0 {
		t.Fatalf("impact %v", fs[0].ImpactBps)
	}
	// the real level grows: refill releases the overlay
	e.OnBook(book(lv("99999:1"), lv("100001:1.5", "100002:1")), 500)
	c := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("1"), Px: d("100001"), TIF: Ioc}, 510)
	e.OnBook(book(lv("99999:1"), lv("100001:1.5", "100002:1")), 710)
	if !c.Filled.Equal(d("1")) {
		t.Fatalf("after refill filled %s", c.Filled)
	}
	// and the overlay decays with its half-life when the book stays flat
	left := e.ImpactLeft("BTC")
	if len(left) == 0 {
		t.Fatal("no overlay after a take")
	}
	e.OnBook(book(lv("99999:1"), lv("100001:1.5", "100002:1")), 710+300_000) // 20 half-lives
	if l := e.ImpactLeft("BTC"); len(l) != 0 {
		t.Fatalf("overlay did not decay: %+v", l)
	}
}

func TestRealFixtureBookWalk(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "hyperliquid", "info", "l2Book_BTC.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wb hyperliquid.L2Book
	if err := json.Unmarshal(raw, &wb); err != nil {
		t.Fatal(err)
	}
	db := hyperliquid.ToBook(wb)
	var depth decimal.Decimal
	for _, l := range db.Asks {
		depth = depth.Add(decimal.NewFromFloat(l.Sz))
	}
	e := newEngine(t)
	e.st.Balance = d("100000000")
	far := hyperliquid.RoundPx(decimal.NewFromFloat(db.Asks[len(db.Asks)-1].Px*1.5), 5, 6, 1)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("500"), Px: far, TIF: Ioc, Market: true}, 0)
	e.OnBook(db, 200)
	if !o.Filled.Equal(depth) {
		t.Fatalf("filled %s, visible depth %s: must stop exactly at real depth", o.Filled, depth)
	}
	for _, f := range e.FillsFor(o.OID) {
		if f.Px.GreaterThan(decimal.NewFromFloat(db.Asks[len(db.Asks)-1].Px)) {
			t.Fatalf("synthetic price %s", f.Px)
		}
	}
	// exact decimals: the account renders without float noise
	a := e.Account()
	if a.Positions[0].Size != depth.InexactFloat64() {
		t.Fatalf("size %v", a.Positions[0].Size)
	}
}

func TestReduceOnlyAndMargin(t *testing.T) {
	e := newEngine(t)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Sell, Sz: d("0.1"), Px: d("99999"), TIF: Ioc, ReduceOnly: true}, 0)
	e.OnBook(std(), 200)
	if o.Status != StatusRejected || o.Error != "Reduce only order would increase position. asset=0" {
		t.Fatalf("%s %q", o.Status, o.Error)
	}
	small := New(NewState("s", d("100"), 0), Config{})
	small.SetSpec(btc)
	big := mustSubmit(t, small, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.1"), Px: d("100001"), TIF: Ioc, Leverage: 20}, 0)
	small.OnBook(std(), 200)
	if big.Status != StatusRejected || big.Error != "Insufficient margin to place order. asset=0" {
		t.Fatalf("%s %q", big.Status, big.Error)
	}
}

func TestTpSlOnMarkWithOCO(t *testing.T) {
	e := newEngine(t)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.2"), Px: d("100001"), TIF: Gtc, TP: d("101000"), SL: d("99000")}, 0)
	e.OnBook(std(), 200)
	if o.Status != StatusFilled || !o.Filled.Equal(d("0.2")) {
		t.Fatalf("status %s filled %s", o.Status, o.Filled)
	}
	var tp, sl *Order
	for _, x := range e.State().Orders {
		if x.Trigger != nil && x.Trigger.Kind == "tp" {
			tp = x
		}
		if x.Trigger != nil && x.Trigger.Kind == "sl" {
			sl = x
		}
	}
	if tp == nil || sl == nil || tp.Status != StatusTrigger || !tp.Sz.Equal(o.Filled) {
		t.Fatalf("children not armed with the parent's fills: %+v %+v", tp, sl)
	}
	// mark below the SL fires it; the TP is cancelled (OCO)
	e.OnMark("BTC", d("98990"), 1000)
	if sl.Status != StatusPending || !sl.Triggered || tp.Status != StatusCanceled {
		t.Fatalf("sl %s tp %s", sl.Status, tp.Status)
	}
	if !sl.Px.Equal(d("89091")) { // 98990 * 0.9, floored for a sell
		t.Fatalf("slippage bound %s", sl.Px)
	}
	e.OnBook(book(lv("98980:1", "98970:1"), lv("98990:1")), 1200)
	if sl.Status != StatusFilled {
		t.Fatalf("sl %s %s", sl.Status, sl.Error)
	}
	if _, ok := e.State().Positions["BTC"]; ok {
		t.Fatal("position not closed")
	}
}

func TestPositionTpslSizedToPosition(t *testing.T) {
	e := newEngine(t)
	mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.3"), Px: d("100002"), TIF: Ioc, SL: d("90000"), PositionTpsl: true}, 0)
	e.OnBook(std(), 200)
	e.OnMark("BTC", d("89000"), 300)
	e.OnBook(book(lv("88990:5"), lv("89000:5")), 600)
	if _, ok := e.State().Positions["BTC"]; ok {
		t.Fatal("position tpsl did not close the position")
	}
}

func TestFundingSettledOncePerHourOnOracle(t *testing.T) {
	e := newEngine(t)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("1"), Px: d("100010"), TIF: Ioc}, 1000)
	e.OnBook(std(), 1200)
	if !o.Filled.Equal(d("1")) {
		t.Fatal(o.Filled)
	}
	bal := e.State().Balance
	e.OnFunding("BTC", 0, d("0.0001"), d("100000"))         // before the position: ignored
	e.OnFunding("BTC", 3_600_000, d("0.0001"), d("100000")) // longs pay 10
	e.OnFunding("BTC", 3_600_000, d("0.0001"), d("100000")) // once per hour
	if got := bal.Sub(e.State().Balance); !got.Equal(d("10")) {
		t.Fatalf("paid %s", got)
	}
	e.OnFunding("BTC", 7_200_000, d("-0.00005"), d("120000")) // receives 6
	if got := bal.Sub(e.State().Balance); !got.Equal(d("4")) {
		t.Fatalf("net paid %s", got)
	}
}

func TestCrossLiquidationThroughBookThenBackstop(t *testing.T) {
	e := New(NewState("l", d("1000"), 0), Config{})
	e.SetSpec(btc)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.35"), Px: d("100010"), TIF: Ioc, Leverage: 40}, 0)
	e.OnBook(std(), 200)
	if !o.Filled.Equal(d("0.35")) {
		t.Fatalf("filled %s (%s)", o.Filled, o.Error)
	}
	p := e.State().Positions["BTC"]
	liq := e.LiqPx(p)
	if liq.Sign() <= 0 || liq.GreaterThan(d("100000")) {
		t.Fatalf("liq px %s", liq)
	}
	// the book thins out far below; mark falls through liq
	e.OnBook(book(lv("97000:0.1"), lv("97010:1")), 300)
	e.OnMark("BTC", liq.Sub(d("50")), 300)
	if _, ok := e.State().Positions["BTC"]; ok {
		t.Fatal("not liquidated")
	}
	var book, back int
	for _, f := range e.State().Fills {
		if f.Liquidation && f.Backstop {
			back++
		} else if f.Liquidation {
			book++
		}
	}
	if book != 1 || back != 1 || e.State().Stats.Liquidations != 1 {
		t.Fatalf("book %d backstop %d", book, back)
	}
	if e.State().Balance.Sign() < 0 {
		t.Fatal("negative balance")
	}
}

func TestIsolatedMarginLockedAndLossCapped(t *testing.T) {
	e := New(NewState("i", d("10000"), 0), Config{})
	e.SetSpec(btc)
	mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.1"), Px: d("100001"), TIF: Ioc, Leverage: 10, Isolated: true}, 0)
	e.OnBook(std(), 200)
	p := e.State().Positions["BTC"]
	if !p.Isolated || !p.Margin.Equal(d("1000.01")) {
		t.Fatalf("margin %s", p.Margin)
	}
	// balance lost the margin and the fee
	if !e.State().Balance.Equal(d("10000").Sub(d("1000.01")).Sub(d("4.5000450"))) {
		t.Fatalf("balance %s", e.State().Balance)
	}
	// a gap to 80000 with no book: backstop at mark, loss capped at the margin
	e.OnMark("BTC", d("80000"), 100_000)
	if _, ok := e.State().Positions["BTC"]; ok {
		t.Fatal("isolated position survived")
	}
	if e.State().Balance.LessThan(d("8995")) {
		t.Fatalf("isolated loss leaked into cross balance: %s", e.State().Balance)
	}
}

func TestCatchUpTradeThroughOnly(t *testing.T) {
	e := newEngine(t)
	o := mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.1"), Px: d("99000")}, 0)
	e.OnBook(std(), 200)
	if o.Status != StatusOpen {
		t.Fatal(o.Status)
	}
	m := int64(60_000)
	cs := []domain.Candle{
		{T: m, O: 99500, H: 99600, L: 99000, C: 99100}, // touch only: no fill
		{T: 2 * m, O: 99100, H: 99200, L: 98950, C: 99050},
	}
	if n := e.CatchUp("BTC", cs, m, 3*m); n != 1 {
		t.Fatalf("catch-up fills %d", n)
	}
	f := e.FillsFor(o.OID)
	if len(f) != 1 || f[0].Model != "catchup" || !f[0].Maker || !f[0].Px.Equal(d("99000")) {
		t.Fatalf("fill %+v", f)
	}
	if e.SyncedAt("BTC") != 3*m {
		t.Fatalf("synced %d", e.SyncedAt("BTC"))
	}
	// replaying the same candles again does nothing
	if n := e.CatchUp("BTC", cs, m, 3*m); n != 0 {
		t.Fatalf("replayed %d", n)
	}
}

func TestStoreRoundTripAndLock(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if err := ValidID("Bad Id"); err == nil {
		t.Fatal("bad id accepted")
	}
	st, err := s.Update("demo", func() *State { return NewState("demo", DefaultBalance, 5) }, func(st *State) error {
		e := New(st, Config{})
		e.SetSpec(btc)
		_, err := e.Submit(OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.1"), Px: d("100001"), TIF: Ioc}, 10)
		if err != nil {
			return err
		}
		e.OnBook(std(), 400)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	back, err := s.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	if !back.Positions["BTC"].Szi.Equal(st.Positions["BTC"].Szi) || !back.Balance.Equal(st.Balance) {
		t.Fatalf("round trip: %s vs %s", back.Balance, st.Balance)
	}
	ids, _ := s.List()
	if len(ids) != 1 || ids[0] != "demo" {
		t.Fatalf("ids %v", ids)
	}
	if _, err := s.Load("nope"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("err %v", err)
	}
	fi, _ := os.Stat(filepath.Join(s.Dir, "demo.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v", fi.Mode().Perm())
	}
}

func TestTapeReplayIsDeterministic(t *testing.T) {
	tape := []Event{}
	b := std()
	tape = append(tape, Event{At: 200, Book: &b})
	tape = append(tape, Event{At: 300, Trades: []domain.Trade{{Coin: "BTC", Side: domain.Sell, Px: 99999, Sz: 1.2}}})
	tape = append(tape, Event{At: 400, Coin: "BTC", Mark: "99950"})
	run := func() string {
		e := newEngine(t)
		mustSubmit(t, e, OrderReq{Coin: "BTC", Side: domain.Buy, Sz: d("0.5"), Px: d("99999")}, 0)
		for _, ev := range tape {
			e.Apply(ev)
		}
		js, _ := json.Marshal(e.Summarize())
		return string(js)
	}
	if a, b := run(), run(); a != b {
		t.Fatal("replay differs")
	}
	if !strings.Contains(run(), `"mode":"paper"`) {
		t.Fatal("summary lacks mode")
	}
}

func TestParseAddress(t *testing.T) {
	for in, want := range map[string]string{"paper:abc": "abc", "PAPER:x": "x", "paper": "default", "paper:": "default"} {
		if got, ok := ParseAddress(in); !ok || got != want {
			t.Errorf("%q -> %q %v", in, got, ok)
		}
	}
	if _, ok := ParseAddress("0xabc"); ok {
		t.Error("address parsed as paper")
	}
}

func TestMaintenanceTiers(t *testing.T) {
	s := Spec{MaxLeverage: 40, Tiers: []Tier{{LowerBound: d("0"), MaxLeverage: 40}, {LowerBound: d("150000000"), MaxLeverage: 20}}}
	// below the tier: 1/80
	if got := maintenance(s, d("80000")); !got.Equal(d("1000")) {
		t.Fatalf("mm %s", got)
	}
	// continuous at the boundary
	at := maintenance(s, d("150000000"))
	if !at.Equal(d("1875000")) {
		t.Fatalf("mm at boundary %s", at)
	}
}
