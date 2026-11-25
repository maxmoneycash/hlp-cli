package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/registry"
)

// Thresholds below which a vault deposit is flagged (audit S2): small or
// young vaults are where copycats and namesakes live.
const (
	vaultWarnTVL     = 100_000
	vaultWarnAgeDays = 30
	maxVaultChoices  = 8
)

// VaultCandidate is one vault a deposit or withdrawal name could mean,
// listed in the USAGE error of an ambiguous name.
type VaultCandidate struct {
	Name    string  `json:"name"`
	Address string  `json:"address"`
	Leader  string  `json:"leader,omitempty"`
	TVL     float64 `json:"tvl"`
	AgeDays int     `json:"ageDays,omitempty"`
	Closed  bool    `json:"closed,omitempty"`
}

// resolveVaultStrict maps a deposit/withdraw target to an address without
// fuzzy ranking (audit S2). Money must never move to a vault picked by a
// guess, because anyone can create a vault named like a popular one.
//
// It accepts:
//   - a 0x address, or "hlp";
//   - an exact match of a curated vaults.toml entry (reviewed addresses,
//     so a namesake can never win);
//   - the full name of a vault (letters and digits only, case-folded)
//     when that vault is also the only one whose name contains it. A
//     partial name that one vault contains is refused with that vault as
//     the candidate: an attacker can name a vault exactly what a user
//     types colloquially. Only open vaults count when depositing; a
//     withdrawal may also name a closed vault.
//
// Anything else is USAGE listing the candidates (name, address, leader,
// TVL, age), or UNKNOWN_VAULT when nothing matches. With "Growi" ($100)
// and "Growi HF" ($12M) both open, "growi" is ambiguous and refused.
func resolveVaultStrict(ctx context.Context, env *Env, s string, deposit bool) (string, *domain.VaultSummary, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil, Errorf(CodeUsage, "vault is required (an address or a name)")
	}
	ix := newVaultIndex(env)
	if strings.EqualFold(s, "hlp") || IsAddress(s) {
		addr, row, err := ix.resolve(ctx, s)
		if err != nil {
			return "", nil, err
		}
		if row == nil {
			row = ix.rowFor(ctx, addr)
		}
		return addr, row, nil
	}
	if addr, ok := curatedExact(env, s); ok {
		return addr, ix.rowFor(ctx, addr), nil
	}
	vs, err := ix.all(ctx)
	if err != nil {
		return "", nil, AsError(err)
	}
	nq := normName(s)
	if nq == "" {
		return "", nil, Errorf(CodeUsage, "vault %q has no letters or digits; pass the 0x address", s)
	}
	var ms []domain.VaultSummary
	for _, v := range vs {
		if deposit && v.Closed {
			continue
		}
		if strings.Contains(normName(v.Name), nq) {
			ms = append(ms, v)
		}
	}
	switch len(ms) {
	case 0:
		return "", nil, &Error{Code: CodeUnknownVault, Message: "no vault name contains " + s,
			Hint: "search with `hlp vaults <words>` or pass the 0x address"}
	case 1:
		if normName(ms[0].Name) == nq {
			row := ms[0]
			return strings.ToLower(row.Address), &row, nil
		}
	}
	ms = rankVaultMatches(ms, s)
	cands := make([]VaultCandidate, 0, min(len(ms), maxVaultChoices))
	lines := make([]string, 0, cap(cands))
	for _, v := range ms[:min(len(ms), maxVaultChoices)] {
		c := VaultCandidate{Name: domain.CleanText(v.Name), Address: strings.ToLower(v.Address), Leader: strings.ToLower(v.Leader),
			TVL: v.TVL, AgeDays: vaultAge(v, env.now()), Closed: v.Closed}
		cands = append(cands, c)
		lines = append(lines, fmt.Sprintf("%s %s (TVL $%s)", c.Name, c.Address, compactUSD(c.TVL)))
	}
	more := ""
	if len(ms) > len(cands) {
		more = fmt.Sprintf(" (showing %d)", len(cands))
	}
	msg := fmt.Sprintf("%q matches %d vaults%s: %s", s, len(ms), more, strings.Join(lines, "; "))
	if len(ms) == 1 {
		msg = fmt.Sprintf("%q is only part of a vault name: %s", s, lines[0])
	}
	return "", nil, &Error{Code: CodeUsage,
		Message: msg,
		Details: map[string]any{"candidates": cands},
		Hint:    "money never goes to a guessed vault: pass the 0x address (or the full name) of the one you mean"}
}

// curatedExact resolves s only when it equals (ignoring case and
// punctuation) the name, id, slug or alias of exactly one curated
// HyperCore vault.
func curatedExact(env *Env, s string) (string, bool) {
	addr := ""
	for _, m := range env.vaultRegistry().Search(s, 0) {
		if m.Tier != registry.TierExact {
			break
		}
		if m.Entry.Kind != registry.KindHyperCore || !IsAddress(m.Entry.Addr()) {
			continue
		}
		if addr != "" && addr != m.Entry.Addr() {
			return "", false
		}
		addr = m.Entry.Addr()
	}
	return addr, addr != ""
}

// rowFor finds the screener row of addr (curated list first, then the full
// list). Nil when neither source knows it; the preview then says so.
func (ix *vaultIndex) rowFor(ctx context.Context, addr string) *domain.VaultSummary {
	xs := ix.curated(ctx)
	for i := range xs {
		if strings.EqualFold(xs[i].Address, addr) {
			row := MergeVaults(nil, xs[i:i+1])[0]
			return &row
		}
	}
	vs, err := ix.all(ctx)
	if err != nil {
		return nil
	}
	for i := range vs {
		if strings.EqualFold(vs[i].Address, addr) {
			row := vs[i]
			return &row
		}
	}
	return nil
}

// vaultAge is the vault's age in whole days (0 when unknown).
func vaultAge(v domain.VaultSummary, now time.Time) int {
	if v.AgeDays > 0 {
		return v.AgeDays
	}
	if !v.CreatedAt.IsZero() && now.After(v.CreatedAt) {
		return int(now.Sub(v.CreatedAt).Hours() / 24)
	}
	return 0
}

// vaultChecks describe the target of a deposit or withdrawal on the
// preview and confirm card: who it is (name, leader, TVL, age, the full
// address) and, for deposits, a warning when it is small or young.
func vaultChecks(addr string, row *domain.VaultSummary, deposit bool, now time.Time) []domain.Check {
	if row == nil {
		st := domain.CheckPass
		if deposit {
			st = domain.CheckWarn
		}
		return []domain.Check{{ID: "vault_target", Label: "Vault", Status: st,
			Detail: addr + " · not in the vault list: check the address before depositing"}}
	}
	name := domain.CleanText(row.Name)
	if name == "" {
		name = "(unnamed)"
	}
	age := vaultAge(*row, now)
	parts := []string{name}
	if row.Leader != "" {
		parts = append(parts, "leader "+strings.ToLower(domain.CleanText(row.Leader)))
	}
	parts = append(parts, "TVL $"+compactUSD(row.TVL))
	if age > 0 {
		parts = append(parts, fmt.Sprintf("%d days old", age))
	}
	parts = append(parts, addr)
	out := []domain.Check{{ID: "vault_target", Label: "Vault", Status: domain.CheckPass, Detail: strings.Join(parts, " · ")}}
	if !deposit || strings.EqualFold(addr, HLPAddress) {
		return out
	}
	if row.TVL < vaultWarnTVL {
		out = append(out, domain.Check{ID: "vault_tvl", Label: "Vault size", Status: domain.CheckWarn,
			Detail: fmt.Sprintf("TVL $%s is under $%s: small vaults are where copycats live", compactUSD(row.TVL), compactUSD(vaultWarnTVL))})
	}
	switch {
	case age == 0:
		out = append(out, domain.Check{ID: "vault_age", Label: "Vault age", Status: domain.CheckWarn, Detail: "age unknown"})
	case age < vaultWarnAgeDays:
		out = append(out, domain.Check{ID: "vault_age", Label: "Vault age", Status: domain.CheckWarn,
			Detail: fmt.Sprintf("%d days old (under %d)", age, vaultWarnAgeDays)})
	}
	return out
}

// compactUSD formats a dollar amount as 12.3M, 450K or 99.50.
func compactUSD(v float64) string {
	switch a := v; {
	case a >= 1e9:
		return trimFloat(v/1e9) + "B"
	case a >= 1e6:
		return trimFloat(v/1e6) + "M"
	case a >= 1e3:
		return trimFloat(v/1e3) + "K"
	}
	return fmt.Sprintf("%.2f", v)
}
