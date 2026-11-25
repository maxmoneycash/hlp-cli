package pvault

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/evm"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
)

func fixtureDir(t testing.TB) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "fixtures", "pvault")
}

func readJSON(t testing.TB, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir(t), name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

type snapCall struct {
	Calldata string          `json:"calldata"`
	Result   string          `json:"result"`
	Error    json.RawMessage `json:"error"`
	Named    json.RawMessage `json:"named"`
}

type snapshot struct {
	Vault          string              `json:"vault"`
	Lens           string              `json:"lens"`
	Calls          map[string]snapCall `json:"calls"`
	LensVault      snapCall            `json:"lens_vaultState"`
	LensUser       map[string]snapCall `json:"lens_userState"`
	LensRange      snapCall            `json:"lens_epochRange_438_442"`
	BlockTimestamp int64               `json:"blockTimestamp"`
}

func loadSnapshot(t testing.TB) *snapshot {
	var s snapshot
	readJSON(t, "onchain_snapshot.json", &s)
	return &s
}

type epochFixture struct {
	Epochs []EpochData `json:"epochs"`
}

func loadEpochs(t testing.TB) []EpochData {
	var e epochFixture
	readJSON(t, "epochs.json", &e)
	return e.Epochs
}

// fakeCaller answers eth_call from onchain_snapshot.json (by calldata) and
// synthesizes epochRange from epochs.json. Unknown calldata reverts.
type fakeCaller struct {
	vault, lens evm.Address
	byData      map[string]string
	epochs      []EpochData
	calls       int
	noLens      bool
}

func newFakeCaller(t testing.TB) *fakeCaller {
	s := loadSnapshot(t)
	f := &fakeCaller{vault: evm.MustAddress(s.Vault), lens: evm.MustAddress(s.Lens), byData: map[string]string{}, epochs: loadEpochs(t)}
	add := func(c snapCall) {
		if c.Result != "" {
			f.byData[strings.ToLower(c.Calldata)] = c.Result
		}
	}
	for _, c := range s.Calls {
		add(c)
	}
	add(s.LensVault)
	for _, c := range s.LensUser {
		add(c)
	}
	return f
}

func (f *fakeCaller) Call(_ context.Context, to evm.Address, data []byte) ([]byte, error) {
	f.calls++
	if to == f.lens && len(data) >= 4 && evm.Selector(data[:4]) == selEpochRange {
		w := evm.Words(data[4:])
		from, _ := w.Uint64(1)
		hi, _ := w.Uint64(2)
		return encodeRange(f.epochs, from, hi), nil
	}
	if to == f.lens && f.noLens {
		return nil, evm.ErrEmptyResult
	}
	if r, ok := f.byData[evm.EncodeHex(data)]; ok {
		return evm.DecodeHex(r)
	}
	return nil, &evm.RevertError{}
}

func (f *fakeCaller) BatchCall(ctx context.Context, msgs []evm.CallMsg) ([]evm.CallResult, error) {
	out := make([]evm.CallResult, len(msgs))
	for i, m := range msgs {
		b, err := f.Call(ctx, m.To, m.Data)
		out[i] = evm.CallResult{Data: b, Err: err}
	}
	return out, nil
}

func (f *fakeCaller) GetCode(_ context.Context, a evm.Address) ([]byte, error) {
	if a == f.vault || a == f.lens {
		return []byte{0x60, 0x80}, nil
	}
	return nil, nil
}

// encodeRange ABI-encodes epochs [from, to] clamped to the last fixture
// epoch + 1 (the open epoch, all zeros), like the live lens.
func encodeRange(es []EpochData, from, to uint64) []byte {
	last := es[len(es)-1].Epoch + 1
	to = min(to, last)
	var words []evm.Word
	words = append(words, evm.Uint64Word(0x20))
	n := uint64(0)
	if to >= from {
		n = to - from + 1
	}
	words = append(words, evm.Uint64Word(n))
	for e := from; e <= to; e++ {
		var d EpochData
		for _, x := range es {
			if x.Epoch == e {
				d = x
			}
		}
		u := func(b evm.Big) evm.Word { w, _ := evm.UintWord(b.V()); return w }
		words = append(words, evm.Uint64Word(d.SettledAt), u(d.SharePrice), u(d.TotalAssetsAfter), u(d.TotalSupplyAfter),
			u(d.DepositAssets), u(d.DepositShares), u(d.RedeemSharesPending), u(d.RedeemSharesFulfilled), u(d.RedeemAssets), u(d.FeeShares))
	}
	out := make([]byte, 0, 32*len(words))
	for _, w := range words {
		out = append(out, w[:]...)
	}
	return out
}

type fakeTelemetry struct {
	status hlpxyz.PVaultStatus
	hist   []hlpxyz.PVaultSample
}

func newFakeTelemetry(t testing.TB) *fakeTelemetry {
	f := &fakeTelemetry{}
	readJSON(t, "api_status.json", &f.status)
	var h struct {
		Samples []hlpxyz.PVaultSample `json:"samples"`
	}
	readJSON(t, "api_history.json", &h)
	f.hist = h.Samples
	return f
}

func (f *fakeTelemetry) PVaultStatus(context.Context) (hlpxyz.PVaultStatus, error) {
	return f.status, nil
}
func (f *fakeTelemetry) PVaultHistory(context.Context, int, string) ([]hlpxyz.PVaultSample, error) {
	return f.hist, nil
}

// fakeCore serves hypercore_testnet.json.
type fakeCore struct {
	ch    json.RawMessage
	spot  []SpotBalance
	agent []CoreAgent
}

func newFakeCore(t testing.TB) *fakeCore {
	var raw struct {
		CH   json.RawMessage `json:"clearinghouseState"`
		Spot struct {
			Balances []SpotBalance `json:"balances"`
		} `json:"spotClearinghouseState"`
		Agents []CoreAgent `json:"extraAgents"`
	}
	readJSON(t, "hypercore_testnet.json", &raw)
	return &fakeCore{ch: raw.CH, spot: raw.Spot.Balances, agent: raw.Agents}
}

func (f *fakeCore) ClearinghouseState(_ context.Context, _ string, dex string) (*Clearinghouse, error) {
	if dex != "" {
		return &Clearinghouse{AccountValue: "0.0", Withdrawable: "0.0"}, nil
	}
	return ParseClearinghouse(f.ch, dex)
}
func (f *fakeCore) SpotBalances(context.Context, string) ([]SpotBalance, error) { return f.spot, nil }
func (f *fakeCore) ExtraAgents(context.Context, string) ([]CoreAgent, error)    { return f.agent, nil }
func (f *fakeCore) PerpDexNames(context.Context) ([]string, error)              { return []string{"", "test"}, nil }

func bigStr(t testing.TB, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad int %q", s)
	}
	return v
}
