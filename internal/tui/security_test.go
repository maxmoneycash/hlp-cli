package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/core"
)

// openTestConfirm starts a confirmer call and waits for its card.
func openTestConfirm(t *testing.T, m *Model, d *driver, p core.OrderPreview) chan bool {
	t.Helper()
	res := make(chan bool, 1)
	go func() {
		ok, _ := m.confirmer(context.Background(), p)
		res <- ok
	}()
	deadline := time.Now().Add(2 * time.Second)
	for m.confirm == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		d.drain()
	}
	if m.confirm == nil {
		t.Fatal("confirm card did not open")
	}
	return res
}

// A-T6 (audit S5): typed letters never answer the confirm card, and keys
// in the first 400 ms after it opens are dropped.
func TestConfirmIgnoresTypedYesAndEarlyKeys(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	res := openTestConfirm(t, m, d, core.OrderPreview{Action: "order", Coin: "BTC", DryRun: true})

	// still typing when the card opens: even enter is dropped
	m.confirm.openedAt = time.Now()
	d.send(key("enter"))
	if m.confirm == nil {
		t.Fatal("enter inside the 400 ms guard answered the card")
	}
	// after the guard: y, Y, h, l and other letters do nothing
	m.confirm.openedAt = time.Now().Add(-time.Second)
	for _, k := range []string{"y", "Y", "h", "l", "e", "s"} {
		d.send(key(k))
		if m.confirm == nil || m.confirm.answer {
			t.Fatalf("%q changed the confirm", k)
		}
	}
	// enter now submits the default, which is No
	d.send(key("enter"))
	select {
	case ok := <-res:
		if ok {
			t.Fatal("default answer must be No")
		}
	case <-time.After(time.Second):
		t.Fatal("confirmer did not return")
	}
}

// Audit S10: keys and seed phrases are recognised, not persisted.
func TestLooksSecret(t *testing.T) {
	for _, s := range []string{
		"0x0123456789012345678901234567890123456789012345678901234567890123",
		"my key is 0123456789012345678901234567890123456789012345678901234567890123 ok",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123",
		"export OPENAI_API_KEY=sk-proj-ABCDEFGHIJKLMNOPQRSTUV",
		"abandon ability able about above absent absorb abstract absurd abuse access accident",
		strings.TrimSpace(strings.Repeat("zoo wrong ", 12)),
	} {
		if !looksSecret(s) {
			t.Errorf("not flagged: %q", s)
		}
	}
	for _, s := range []string{
		"/buy BTC 0.01",
		"/account 0x1111111111111111111111111111111111111111",
		"what is the funding on hype and how has it moved over the last week",
		"show me the best vaults by sharpe with at least a million dollars in tvl please",
		"is the risk ok",
		"task-1234567890 done",
	} {
		if looksSecret(s) {
			t.Errorf("false positive: %q", s)
		}
	}
	h := newHistory(t.TempDir())
	h.Add("/vaults")
	h.Add("0x0123456789012345678901234567890123456789012345678901234567890123")
	if len(h.items) != 1 {
		t.Fatalf("secret recorded: %q", h.items)
	}
	h2 := newHistory(h.path[:len(h.path)-len("/history")])
	if len(h2.items) != 1 || h2.items[0] != "/vaults" {
		t.Fatalf("persisted %q", h2.items)
	}
}

func TestRefuseSecretShowsError(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	n := len(m.entries)
	if m.refuseSecret("/vaults") || len(m.entries) != n {
		t.Fatal("ordinary line refused")
	}
	if !m.refuseSecret("sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123") || m.entries[len(m.entries)-1].kind != entryError {
		t.Fatal("secret not refused")
	}
}

// Audit S6: the shell tags its own calls "tui" but never upgrades the
// assistant's "agent" calls.
func TestTUISourceTagging(t *testing.T) {
	if got := core.SourceFrom(tuiSource(context.Background())); got != "tui" {
		t.Fatalf("untagged call: %s", got)
	}
	if got := core.SourceFrom(tuiSource(core.WithSource(context.Background(), "agent"))); got != "agent" {
		t.Fatalf("agent call upgraded to %s", got)
	}
}
