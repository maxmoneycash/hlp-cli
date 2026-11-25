package hyperliquid

import (
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

func TestInfoErrorsAndRetries(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	ctx := context.Background()

	// 422 -> BAD_REQUEST, no retry.
	var out any
	err := c.Info(ctx, map[string]string{"type": "nope"}, &out)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "BAD_REQUEST" || ae.Status != 422 {
		t.Fatalf("422: %v", err)
	}
	if f.count("nope") != 1 {
		t.Fatalf("422 retried: %d", f.count("nope"))
	}
	// 500 null on a coin request -> ErrUnknownCoin, no retry.
	if _, err := c.L2Book(ctx, "NOPE", BookOpts{}); !errors.Is(err, ErrUnknownCoin) {
		t.Fatalf("unknown coin: %v", err)
	}
	if f.count("l2Book") != 1 {
		t.Fatalf("unknown coin retried")
	}
	// 200 null on vaultDetails -> ErrNotFound.
	if _, err := c.VaultDetails(ctx, "0x0000000000000000000000000000000000000001", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("vault: %v", err)
	}
	// Missing type is refused locally.
	if err := c.Info(ctx, map[string]string{}, &out); err == nil {
		t.Fatal("no type accepted")
	}

	// 429 then success: retried.
	n := 0
	f.override = func(typ string, _ map[string]any) (int, []byte, bool) {
		if typ != "exchangeStatus" {
			return 0, nil, false
		}
		n++
		if n < 3 {
			return http.StatusTooManyRequests, []byte("slow down"), true
		}
		return 200, []byte(`{"specialStatuses":null,"time":1}`), true
	}
	var st map[string]any
	if err := c.Info(ctx, map[string]string{"type": "exchangeStatus"}, &st); err != nil || n != 3 {
		t.Fatalf("retry: %v n=%d", err, n)
	}
	// Persistent 503 -> SERVER after 1+4 attempts.
	n = 0
	f.override = func(typ string, _ map[string]any) (int, []byte, bool) {
		if typ != "exchangeStatus" {
			return 0, nil, false
		}
		n++
		return 503, []byte("down"), true
	}
	err = c.Info(ctx, map[string]string{"type": "exchangeStatus"}, &st)
	if !errors.As(err, &ae) || ae.Code != "SERVER" || n != 5 {
		t.Fatalf("5xx: %v n=%d", err, n)
	}
	// Persistent 429 -> ErrRateLimited.
	f.override = func(typ string, _ map[string]any) (int, []byte, bool) {
		return http.StatusTooManyRequests, nil, typ == "exchangeStatus"
	}
	if err := c.Info(ctx, map[string]string{"type": "exchangeStatus"}, &st); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("429: %v", err)
	}
	// Malformed JSON -> DECODE.
	f.override = func(typ string, _ map[string]any) (int, []byte, bool) {
		return 200, []byte("{not json"), typ == "exchangeStatus"
	}
	if err := c.Info(ctx, map[string]string{"type": "exchangeStatus"}, &st); !errors.As(err, &ae) || ae.Code != "DECODE" {
		t.Fatalf("decode: %v", err)
	}
}

func TestInfoChargesItemSurcharge(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	lim := NewLimiter(1000, time.Hour)
	c.opts.Limiter = lim
	f.override = func(typ string, _ map[string]any) (int, []byte, bool) {
		if typ != "candleSnapshot" {
			return 0, nil, false
		}
		var b strings.Builder
		b.WriteString("[")
		for i := 0; i < 120; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"t":0,"T":59999,"s":"BTC","i":"1m","o":"1","c":"1","h":"1","l":"1","v":"1","n":1}`)
		}
		b.WriteString("]")
		return 200, []byte(b.String()), true
	}
	if _, err := c.CandleSnapshot(context.Background(), "BTC", domain.I1m, 0, 0); err != nil {
		t.Fatal(err)
	}
	// 20 base + 120/60 = 22.
	if got := 1000 - lim.Available(); math.Abs(got-22) > 0.01 {
		t.Fatalf("charged %v", got)
	}
}

func TestNormalizeAddress(t *testing.T) {
	a, err := NormalizeAddress(" 0xDFC24B077BC1425AD1DEA75BCB6F8158E10DF303 ")
	if err != nil || a != addrHLP {
		t.Fatalf("%q %v", a, err)
	}
	for _, bad := range []string{"", "0x123", "dfc24b077bc1425ad1dea75bcb6f8158e10df303", "0xzz24b077bc1425ad1dea75bcb6f8158e10df303"} {
		if _, err := NormalizeAddress(bad); !errors.Is(err, ErrInvalidAddress) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEndpointsAndDefaults(t *testing.T) {
	c := New(Options{})
	if c.Network() != domain.Mainnet || !strings.Contains(c.Endpoints().Info, "api.hyperliquid.xyz") {
		t.Fatal("mainnet defaults")
	}
	tc := New(Options{Network: domain.Testnet})
	if !strings.Contains(tc.Endpoints().WS, "testnet") || !strings.HasSuffix(tc.Endpoints().Stats, "Testnet") {
		t.Fatal("testnet endpoints")
	}
	if c.Limiter() == nil || c.opts.UserAgent == "" {
		t.Fatal("defaults")
	}
}

func TestRegistryFromFixtures(t *testing.T) {
	r := NewRegistryFrom(fixtureRegistryData(t))
	cases := map[string]struct {
		coin string
		id   int
		kind domain.MarketKind
	}{
		"btc":        {"BTC", 0, domain.Perp},
		"BTC":        {"BTC", 0, domain.Perp},
		"ETH":        {"ETH", 1, domain.Perp},
		"eth-perp":   {"ETH", 1, domain.Perp},
		"$sol":       {"SOL", 5, domain.Perp},
		"xyz:NVDA":   {"xyz:NVDA", 110002, domain.HIP3},
		"XYZ:nvda":   {"xyz:NVDA", 110002, domain.HIP3},
		"nvda":       {"xyz:NVDA", 110002, domain.HIP3},
		"NVDA (xyz)": {"xyz:NVDA", 110002, domain.HIP3},
		"nvidia":     {"xyz:NVDA", 110002, domain.HIP3},
		"@107":       {"@107", 10107, domain.Spot},
		"HYPE/USDC":  {"@107", 10107, domain.Spot},
		"hype/usdc":  {"@107", 10107, domain.Spot},
		"PURR/USDC":  {"PURR/USDC", 10000, domain.Spot},
		"#337130":    {"#337130", 100337130, domain.Outcome},
		"MATIC":      {"MATIC", 3, domain.Perp}, // delisted resolves, flagged
		"xyz:XYZ100": {"xyz:XYZ100", 110000, domain.HIP3},
	}
	for in, want := range cases {
		m, err := r.Resolve(in)
		if err != nil {
			t.Errorf("Resolve(%q): %v", in, err)
			continue
		}
		if m.Coin != want.coin || m.AssetID != want.id || m.Kind != want.kind {
			t.Errorf("Resolve(%q) = %s/%d/%s, want %s/%d/%s", in, m.Coin, m.AssetID, m.Kind, want.coin, want.id, want.kind)
		}
	}
	m, _ := r.Resolve("MATIC")
	if !m.Delisted {
		t.Error("MATIC should be flagged delisted")
	}
	m, _ = r.Resolve("@107")
	if m.Display != "HYPE/USDC" || m.SzDecimals != 2 {
		t.Errorf("spot display %+v", m)
	}
	m, _ = r.Resolve("xyz:NVDA")
	if m.Display != "NVDA (xyz)" || m.Dex != "xyz" || m.SzDecimals != 3 || m.MaxLeverage != 20 {
		t.Errorf("hip3 %+v", m)
	}
	m, _ = r.Resolve("#337130")
	if !strings.Contains(m.Display, "WAS vs HOU") || !strings.HasSuffix(m.Display, ": WAS") {
		t.Errorf("outcome display %q", m.Display)
	}

	_, err := r.Resolve("NOPE")
	if !errors.Is(err, ErrUnknownCoin) {
		t.Fatalf("NOPE: %v", err)
	}
	_, err = r.Resolve("BTX")
	var uc *UnknownCoinError
	if !errors.As(err, &uc) || len(uc.Suggestions) == 0 || uc.Suggestions[0] != "BTC" {
		t.Fatalf("suggestions: %v", err)
	}
	if !strings.Contains(err.Error(), "did you mean BTC") {
		t.Fatalf("message: %v", err)
	}
	if _, err := r.Resolve("  "); !errors.Is(err, ErrUnknownCoin) {
		t.Fatal("blank accepted")
	}
	// SPY needs xyz:SP500, which the trimmed fixture lacks: unknown.
	if _, err := r.Resolve("SPY"); !errors.Is(err, ErrUnknownCoin) {
		t.Fatal("SPY without SP500 should miss")
	}
	for _, mk := range r.Markets() {
		if mk.Delisted {
			t.Fatalf("Markets returned delisted %s", mk.Coin)
		}
	}
	if len(r.Markets()) == 0 || r.Refresh(context.Background()) != nil {
		t.Fatal("static registry")
	}
}

func TestRegistryAliasSPY(t *testing.T) {
	d := fixtureRegistryData(t)
	d.Metas[1].Universe = append(d.Metas[1].Universe, AssetMeta{Name: "xyz:SP500", SzDecimals: 3, MaxLeverage: 20})
	r := NewRegistryFrom(d)
	for _, in := range []string{"SPY", "s&p", "S&P 500", "sp500", "xyz-sp500"} {
		m, err := r.Resolve(in)
		if err != nil || m.Coin != "xyz:SP500" || m.AssetID != 110003 {
			t.Errorf("Resolve(%q) = %+v %v", in, m, err)
		}
	}
	v := CoinValidator(r)
	if v("xyz:SP500") != nil || v("BTC") != nil {
		t.Fatal("validator rejects wire coins")
	}
	if v("btc") == nil || v("NOPE") == nil {
		t.Fatal("validator must require exact wire coins")
	}
}

func TestRegistryRefreshNetworkAndDisk(t *testing.T) {
	f := newFakeHL(t)
	dir := t.TempDir()
	ep := Endpoints{Info: f.srv.URL + "/info", Stats: f.srv.URL + "/stats"}
	now := time.Unix(1794735000, 0)
	mk := func() *Client {
		c := New(Options{Endpoints: &ep, CacheDir: dir, Now: func() time.Time { return now }})
		c.retryBase = time.Millisecond
		return c
	}
	c := mk()
	if err := c.Registry().Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Registry() != c.Registry() {
		t.Fatal("registry must be cached on the client")
	}
	m, err := c.Registry().Resolve("nvda")
	if err != nil || m.AssetID != 110002 {
		t.Fatalf("%+v %v", m, err)
	}
	// flx came from allPerpMetas[2]; vntl's meta failed (422) and is skipped.
	if m, err := c.Registry().Resolve("flx:NVDA"); err != nil || m.AssetID != 120001 || !m.Delisted {
		t.Fatalf("flx: %+v %v", m, err)
	}
	if f.count("meta") != 1 {
		t.Fatalf("expected one per-dex meta fallback, got %d", f.count("meta"))
	}
	if _, err := os.Stat(dir + "/registry-mainnet.json"); err != nil {
		t.Fatalf("disk cache: %v", err)
	}
	// A second client within the TTL loads from disk without the network.
	before := f.count("perpDexs")
	c2 := mk()
	if _, err := c2.Registry().Resolve("ETH"); err != nil {
		t.Fatal(err)
	}
	if f.count("perpDexs") != before {
		t.Fatal("fresh disk cache ignored")
	}
	// After the TTL the network is used again; if it fails the stale copy serves.
	now = now.Add(2 * time.Hour)
	f.override = func(string, map[string]any) (int, []byte, bool) { return 503, nil, true }
	c3 := mk()
	if err := c3.Registry().Refresh(context.Background()); err != nil {
		t.Fatalf("stale fallback: %v", err)
	}
	if _, err := c3.Registry().Resolve("BTC"); err != nil {
		t.Fatal(err)
	}
	// No cache at all + network down -> error.
	c4 := New(Options{Endpoints: &ep})
	c4.retryBase = time.Millisecond
	if err := c4.Registry().Refresh(context.Background()); err == nil {
		t.Fatal("expected error with no data")
	}
}

func TestMarketsAndMarketData(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	// Pin the clock to the fixture capture time: Funding with start 0 looks
	// back 7 days from now, so a wall clock drops the captured prints once
	// the fixtures are a week old.
	fxNow := time.UnixMilli(1794735423948).UTC()
	c.opts.Now = func() time.Time { return fxNow }
	ctx := context.Background()
	mkts, meta, err := c.Markets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Partial || len(meta.Warnings) == 0 {
		t.Errorf("flx/vntl dex failures should mark partial: %+v", meta)
	}
	by := map[string]domain.Market{}
	for _, m := range mkts {
		by[m.Coin] = m
		if m.Delisted {
			t.Errorf("delisted %s in Markets", m.Coin)
		}
	}
	btc := by["BTC"]
	if btc.Mark != 65392 || btc.PrevDay != 67133 || btc.Funding != 0.0000125 || btc.AssetID != 0 || btc.Category != "crypto" {
		t.Errorf("BTC %+v", btc)
	}
	if math.Abs(btc.ComputeChange24h()-(65392.0/67133-1)) > 1e-12 {
		t.Error("change")
	}
	if n := by["xyz:NVDA"]; n.AssetID != 110002 || n.Mark == 0 || n.Kind != domain.HIP3 {
		t.Errorf("NVDA %+v", n)
	}
	if p := by["PURR/USDC"]; p.Mark != 0.08215 || p.Kind != domain.Spot {
		t.Errorf("PURR %+v", p)
	}
	if _, ok := by["MATIC"]; ok {
		t.Error("delisted MATIC listed")
	}
	// Cached within the TTL.
	n := f.count("metaAndAssetCtxs")
	if _, _, err := c.Markets(ctx); err != nil || f.count("metaAndAssetCtxs") != n {
		t.Fatal("markets cache")
	}

	cs, err := c.Candles(ctx, "btc", domain.I1h, 3, 0)
	if err != nil || len(cs) != 3 || cs[0].T != 1794650400000 || cs[2].C != 66970 {
		t.Fatalf("candles %v %+v", err, cs)
	}
	if _, err := c.Candles(ctx, "BTC", "6h", 3, 0); err == nil {
		t.Fatal("bad interval accepted")
	}
	if _, err := c.Candles(ctx, "NOPE", domain.I1h, 3, 0); !errors.Is(err, ErrUnknownCoin) {
		t.Fatalf("unknown coin: %v", err)
	}
	b, err := c.Book(ctx, "nvda", BookOpts{})
	if err != nil || b.Coin != "xyz:NVDA" || len(b.Bids) != 5 || b.Bids[0].Px != 160.77 || b.Asks[0].Px <= b.Bids[0].Px {
		t.Fatalf("book %v %+v", err, b)
	}
	if math.Abs(b.Mid()-160.78) > 1e-9 {
		t.Fatalf("mid %v", b.Mid())
	}
	if _, err := c.Book(ctx, "BTC", BookOpts{NSigFigs: 1}); err == nil {
		t.Fatal("nSigFigs 1 accepted")
	}
	if _, err := c.Book(ctx, "BTC", BookOpts{NSigFigs: 4, Mantissa: 2}); err == nil {
		t.Fatal("mantissa without 5 sig figs accepted")
	}
	tr, err := c.Trades(ctx, "BTC")
	if err != nil || len(tr) != 3 || tr[0].Side != domain.Buy || tr[0].TID != 162824165292289 {
		t.Fatalf("trades %v %+v", err, tr)
	}
	fp, err := c.Funding(ctx, "BTC", 0, 0)
	if err != nil || len(fp) != 3 || fp[0].Rate != 0.0000001823 {
		t.Fatalf("funding %v %+v", err, fp)
	}
}

func TestFundingPagination(t *testing.T) {
	f := newFakeHL(t)
	ep := Endpoints{Info: f.srv.URL + "/info", Stats: f.srv.URL + "/stats", WS: "ws://unused", Exchange: f.srv.URL + "/exchange"}
	const hour = int64(3600_000)
	now := time.UnixMilli(2002 * hour).UTC()
	c := New(Options{Endpoints: &ep, Limiter: NewLimiter(100000, time.Minute), CacheDir: t.TempDir(), Now: func() time.Time { return now }})
	c.retryBase = time.Millisecond
	pages := 0
	// The fake serves one print per hour in [startTime, endTime], at most
	// 500 per response, like Hyperliquid.
	f.override = func(typ string, body map[string]any) (int, []byte, bool) {
		if typ != "fundingHistory" {
			return 0, nil, false
		}
		pages++
		from := int64(body["startTime"].(float64))
		to := int64(body["endTime"].(float64))
		var sb strings.Builder
		sb.WriteString("[")
		n := 0
		for ts := (from + hour - 1) / hour * hour; ts <= to && n < 500; ts += hour {
			if n > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(`{"coin":"BTC","fundingRate":"0.00001","premium":"0","time":` + itoa(ts) + `}`)
			n++
		}
		sb.WriteString("]")
		return 200, []byte(sb.String()), true
	}
	start := 900*hour + 1
	out, err := c.FundingHistory(context.Background(), "BTC", start, 0)
	if err != nil || len(out) != 1102 || out[0].Time != 901*hour || out[len(out)-1].Time != 2002*hour {
		t.Fatalf("%v len=%d", err, len(out))
	}
	for i := 1; i < len(out); i++ {
		if out[i].Time <= out[i-1].Time {
			t.Fatal("not strictly increasing")
		}
	}
	first := pages
	// Windows entirely in the past come from the disk cache; only the
	// current window is fetched again.
	again, err := c.FundingHistory(context.Background(), "BTC", start, 0)
	if err != nil || len(again) != len(out) || pages != first+1 {
		t.Fatalf("cached run: %v len=%d requests=%d (first run %d)", err, len(again), pages-first, first)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestRegistryUSDCSpotAndAlternatives(t *testing.T) {
	d := fixtureRegistryData(t)
	r := NewRegistryFrom(d)
	for _, in := range []string{"hype-usdc", "HYPE usdc"} {
		if m, err := r.Resolve(in); err != nil || m.Coin != "@107" {
			t.Errorf("Resolve(%q) = %+v %v, want the HYPE/USDC spot pair", in, m.Coin, err)
		}
	}
	if m, err := r.Resolve("btc-usdc"); err != nil || m.Coin != "BTC" {
		t.Errorf("btc-usdc falls back to the perp: %+v %v", m.Coin, err)
	}
	if alts := Alternatives(r, "nvda"); alts != nil {
		t.Errorf("one dex lists NVDA: %v", alts)
	}
	d2 := fixtureRegistryData(t)
	d2.Metas[1].Universe = append(d2.Metas[1].Universe, AssetMeta{Name: "abc:NVDA", SzDecimals: 3, MaxLeverage: 20})
	r2 := NewRegistryFrom(d2)
	alts := Alternatives(r2, "nvda")
	if len(alts) != 2 || alts[0] != "xyz:NVDA" {
		t.Fatalf("alternatives %v", alts)
	}
	if Alternatives(r2, "xyz:nvda") != nil || Alternatives(r2, "BTC") != nil {
		t.Fatal("qualified and native names are never ambiguous")
	}
}
