package hyperliquid

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

func TestAllMidsSharedAcrossConcurrentCallers(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := c.AllMids(ctx, "")
			if err != nil || len(m) == 0 {
				t.Errorf("AllMids: %v %d", err, len(m))
			}
			m["MUTATED"] = Dec("1") // callers own their copy
		}()
	}
	wg.Wait()
	if n := f.count("allMids"); n != 1 {
		t.Fatalf("allMids requests = %d, want 1", n)
	}
	m, _ := c.AllMids(ctx, "")
	if _, ok := m["MUTATED"]; ok {
		t.Fatal("memo shared a caller's map")
	}
}

func TestAccountsShareOneAllMids(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	ctx := context.Background()
	for range 3 {
		if _, err := c.Account(ctx, addrTrader); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count("allMids"); n != 1 {
		t.Fatalf("allMids per 3 accounts = %d", n)
	}
}

func TestMarketsDiskMicroCacheAndDerived(t *testing.T) {
	f := newFakeHL(t)
	dir := t.TempDir()
	c := f.client(t)
	c.opts.CacheDir = dir
	ctx := context.Background()
	ms, _, err := c.Markets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var btc domain.Market
	for _, m := range ms {
		if m.Coin == "BTC" {
			btc = m
		}
	}
	if btc.PrevDay == 0 || btc.Change24h == 0 || btc.Change24h != btc.ComputeChange24h() || btc.FundingAPR != btc.ComputeFundingAPR() {
		t.Fatalf("derived fields not filled: %+v", btc)
	}
	n := f.count("metaAndAssetCtxs")
	// A second process (new client, same cache dir) within 5 s reads the file.
	c2 := f.client(t)
	c2.opts.CacheDir = dir
	ms2, _, err := c2.Markets(ctx)
	if err != nil || len(ms2) != len(ms) {
		t.Fatalf("cached markets: %v %d/%d", err, len(ms2), len(ms))
	}
	if f.count("metaAndAssetCtxs") != n {
		t.Fatal("second process refetched markets")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, "markets-mainnet.json.gz"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("markets cache mode: %v %v", fi, err)
		}
	}
}

func TestVaultChildrenMemoized(t *testing.T) {
	f := newFakeHL(t)
	dir := t.TempDir()
	c := f.client(t)
	c.opts.CacheDir = dir
	ctx := context.Background()
	kids, err := c.VaultChildren(ctx, addrHLP)
	if err != nil || len(kids) == 0 {
		t.Fatalf("children: %v %v", kids, err)
	}
	if _, err := c.VaultChildren(ctx, addrHLP); err != nil {
		t.Fatal(err)
	}
	c2 := f.client(t)
	c2.opts.CacheDir = dir
	if k2, err := c2.VaultChildren(ctx, addrHLP); err != nil || len(k2) != len(kids) {
		t.Fatalf("disk memo: %v %v", k2, err)
	}
	if n := f.count("vaultDetails"); n != 1 {
		t.Fatalf("vaultDetails requests = %d, want 1", n)
	}
	if f.count("clearinghouseState") != 0 {
		t.Fatal("VaultChildren fanned out to Account")
	}
}

func TestMappersCleanThirdPartyText(t *testing.T) {
	evil := "Nice vault\x1b]52;c;MHhkZWFk\x07"
	var sv StatsVault
	sv.Summary.Name = evil
	sv.Summary.VaultAddress = "0x0000000000000000000000000000000000000001"
	if s := StatsToSummary(sv, time.Now(), false); strings.ContainsAny(s.Name, "\x1b\x07") {
		t.Fatalf("stats name not cleaned: %q", s.Name)
	}
	vd := VaultDetails{Name: evil, Description: evil + "‮"}
	d := VaultDetailFrom(vd, time.Now())
	if strings.ContainsAny(d.Summary.Name+d.Description, "\x1b\x07‮") {
		t.Fatalf("vault detail not cleaned: %q %q", d.Summary.Name, d.Description)
	}
	if d.Summary.UpdatedAt.Location() != time.UTC || d.Meta.AsOf.Location() != time.UTC {
		t.Fatal("vault detail times not UTC")
	}
	ms := BuildMarkets(RegistryData{Spot: SpotMeta{
		Tokens:   []SpotToken{{Name: "USDC", Index: 0}, {Name: "EV\x1b[2JIL", Index: 1}},
		Universe: []SpotPair{{Name: "@1", Tokens: [2]int{1, 0}, Index: 1}},
	}})
	for _, m := range ms {
		if strings.ContainsRune(m.Display, 0x1b) {
			t.Fatalf("market display not cleaned: %q", m.Display)
		}
	}
}

func TestCacheFilesOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "x.json")
	if err := writeFileAtomic(p, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	di, _ := os.Stat(filepath.Dir(p))
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Fatalf("modes %v %v", fi.Mode().Perm(), di.Mode().Perm())
	}
	// Replacing an existing file works (the Windows fallback path too).
	if err := writeFileAtomic(p, []byte("[]")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "[]" {
		t.Fatalf("replace: %s", b)
	}
}
