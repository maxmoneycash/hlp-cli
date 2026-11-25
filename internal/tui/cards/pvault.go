package cards

import (
	"strconv"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/pvault"
	"github.com/maxmoneycash/hlp-cli/internal/tui/glyph"
)

// Strategy-vault (HyperEVM pvault) cards. Until the integrator adds
// core.CardPVault* constants and cases in Render, RenderPVault renders these
// payloads directly:
//
//	*pvault.Tearsheet  → tearsheet (NAV is focal)
//	*pvault.EpochTable → settled-epoch table
//	*pvault.UserView   → one depositor
//
// Layout follows Stageshow craft (references/craft.md, Restraint): one focal
// number, real words only where the story is (NAV, price vs high-water mark,
// where the money sits), quiet labels everywhere else.

// RenderPVault renders a strategy-vault result with the standard frame and
// footer. Other payloads fall through to Render.
func RenderPVault(r *core.Result, o Opts) string {
	if r == nil {
		return ""
	}
	o = o.normalized()
	p := newPainter(o)
	c, ok := p.pvaultAny(r)
	if !ok {
		return Render(r, o)
	}
	if c.title == "" {
		c.title = r.Title
	}
	return p.finish(p.frame(c, r.Meta))
}

// pvaultAny dispatches on the payload type (for Render's switch too).
func (p *painter) pvaultAny(r *core.Result) (card, bool) {
	if d, ok := as[pvault.Tearsheet](r.Data); ok {
		return p.pvaultCard(r, &d), true
	}
	if d, ok := as[pvault.EpochTable](r.Data); ok {
		return p.pvaultEpochsCard(r, &d), true
	}
	if d, ok := as[pvault.UserView](r.Data); ok {
		return p.pvaultUserCard(r, &d), true
	}
	return card{}, false
}

// vaultTitle drops a "(testnet)" suffix: the chain label already says so.
func vaultTitle(name string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(name), "(testnet)"))
}

func pvChain(id uint64) string {
	switch id {
	case 998:
		return "HyperEVM testnet"
	case 999:
		return "HyperEVM"
	}
	return "chain " + strconv.FormatUint(id, 10)
}

func bps(v uint64) string { return trimFloat(float64(v)/100, 2) + "%" }

func secs(s uint64) string {
	if s == 0 {
		return dash
	}
	d := (time.Duration(s) * time.Second).String()
	if strings.HasSuffix(d, "m0s") {
		d = strings.TrimSuffix(d, "0s")
	}
	if strings.HasSuffix(d, "h0m") {
		d = strings.TrimSuffix(d, "0m")
	}
	return d
}

// pvaultCard is the tearsheet.
func (p *painter) pvaultCard(r *core.Result, d *pvault.Tearsheet) card {
	title := d.Name
	if title == "" {
		title = r.Title
	}
	if title == "" {
		title = "Strategy vault " + ShortAddr(d.Vault)
	}
	// a strategy name of 3 words or fewer is a card title: UPPERCASE
	c := card{title: termSafe(vaultTitle(title)), name: len(strings.Fields(title)) > 3}
	// Status words are gold text with their mark, never a filled badge
	// (DESIGN.md 2 rule 4).
	if d.ChainID == 998 {
		c.pill = p.st.warn.Render(glyph.Note + " TESTNET")
		c.said = "not investable" // TESTNET already says it
	}
	if d.Paused {
		c.pill = strings.TrimSpace(c.pill + "  " + p.st.warn.Render(glyph.Note+" PAUSED"))
	}
	if !p.o.Compact {
		c.sub = ShortAddr(d.Vault)
	}
	// The share price (NAV per share) is the number this vault is about;
	// its assets follow as the delta.
	c.focal, c.focalStyle, c.numeric = Num(d.SharePrice, 4), p.st.bold, true
	c.delta, c.deltaStyle, c.deltaLabel = USD(d.NavUSD), p.st.text, "TVL"

	// Meta: EPOCH 485   CUTOFF 19:43   HWM −4.2%. The share price is the
	// header's delta; the high-water mark is a signed distance.
	cutoff := ""
	if d.SecondsToCutoff > 0 && d.Phase == "open" {
		// an absolute time: a countdown would freeze in the transcript
		base := r.Meta.AsOf
		if base.IsZero() {
			base = time.Now()
		}
		loc := p.o.Location
		if loc == nil {
			loc = time.UTC
		}
		cutoff = base.Add(time.Duration(d.SecondsToCutoff) * time.Second).In(loc).Format("15:04")
	}
	pairs := [][2]string{{"epoch", p.st.text.Render(strconv.FormatUint(d.Epoch, 10))}}
	if cutoff != "" { // no bare CUTOFF label outside an open epoch
		pairs = append(pairs, [2]string{"cutoff", p.st.text.Render(cutoff)})
	}
	pairs = append(pairs, [2]string{"hwm", p.sign(d.DrawdownVsHWM).Render(SignedPct(d.DrawdownVsHWM, 1))})
	c.body = append(c.body, p.meta(pairs...))
	if n := len(d.History); n > 1 {
		vals := make([]float64, n)
		for i, h := range d.History {
			vals[i] = h.SharePrice
		}
		// the NAV as a thin 2-row line under the numbers, in ink
		for _, l := range lineRows(vals, min(p.w, 60), 2) {
			c.body = append(c.body, p.st.text.Render(l))
		}
	}
	if p.o.Compact {
		return c
	}

	var badges []string
	if !d.CoreLinkProven {
		badges = append(badges, p.badge("CORE LINK UNPROVEN", true))
	}
	if d.PendingTransfer != nil {
		badges = append(badges, p.badge("TRANSFER "+strings.ToUpper(d.PendingTransfer.Kind), false))
	}
	if len(badges) > 0 {
		c.body = append(c.body, strings.Join(badges, " "))
	}
	c.body = append(c.body, "")

	phase := d.Phase
	if cutoff != "" {
		phase += " · cutoff " + cutoff
	}
	kv := [][2]string{
		{"chain", pvChain(d.ChainID)},
		{"hwm px", Num(d.HighWaterMark, 4)},
		{"epoch", strconv.FormatUint(d.Epoch, 10) + " " + phase},
		{"capital", USD(d.IdleUSD) + " idle " + Pct(d.IdlePct, 0) + " · " + USD(d.CorePerpEquityUSD+d.CoreSpotUSD) + " on Core " + Pct(d.DeployedPct, 0)},
	}
	if d.Source == pvault.SourceLens {
		kv = append(kv, [2]string{"deploy room", USD(d.DeployLimit) + " · withdrawable " + USD(d.CoreWithdrawableUSD)})
	}
	if d.PendingDepositsUSD > 0 || d.PendingRedeemShares > 0 {
		kv = append(kv, [2]string{"queued", USD(d.PendingDepositsUSD) + " in · " + Num(d.PendingRedeemShares, 2) + " shares out"})
	}
	fees := "mgmt " + bps(d.Fees.ManagementBps) + " · perf " + bps(d.Fees.PerformanceBps) + " · exit " + bps(d.Fees.ExitBps)
	if d.FeesPaidUSD > 0 {
		fees += " · " + USD(d.FeesPaidUSD) + " paid (" + strconv.Itoa(len(d.Epochs)) + " ep)"
	}
	kv = append(kv, [2]string{"fees", fees})
	if d.Config != nil && !p.o.Compact {
		kv = append(kv, [2]string{"terms", secs(d.Config.EpochDurationSec) + " epochs · min " + USD(d.Config.MinDepositUSD) +
			" · cap " + USD(d.Config.DepositCapUSD) + " (" + USD(d.Config.CapacityUSD) + " left)"})
	}
	if d.Bot != nil {
		mode := "live"
		if d.Bot.Paper {
			mode = "paper"
		}
		bot := mode + " · gross " + Num(d.Bot.GrossX, 2) + "x · ran " + fillTime(d.Bot.At, p.o.Location)
		kv = append(kv, [2]string{"bot", bot})
	}
	if d.Agent != nil && d.Agent.DaysLeft > 0 {
		kv = append(kv, [2]string{"agent", ShortAddr(d.Agent.Address) + " · " + trimFloat(d.Agent.DaysLeft, 0) + " days left"})
	}
	{
		roles := "owner " + shortOr(d.Roles.Owner) + " · keeper " + shortOr(d.Roles.Keeper) + " · guardian " + shortOr(d.Roles.Guardian)
		kv = append(kv, [2]string{"roles", roles})
		if d.ManagerShares > 0 {
			kv = append(kv, [2]string{"manager", shortOr(d.Roles.Manager) + " · stake " + USD(d.ManagerStakeUSD)})
		}
	}
	c.body = append(c.body, p.kvLines(kv, p.w)...)

	if d.User != nil {
		c.body = append(c.body, "", p.section("you "+ShortAddr(d.User.Address)))
		c.body = append(c.body, p.kvLines(p.userPairs(d.User), p.w)...)
	}

	if d.Bot != nil && len(d.Bot.Book) > 0 {
		rows := make([][]cell, 0, len(d.Bot.Book))
		for _, b := range d.Bot.Book {
			dir, s := "FLAT", p.st.muted
			switch {
			case b.Dir > 0:
				dir, s = "LONG", p.st.up
			case b.Dir < 0:
				dir, s = "SHORT", p.st.down
			}
			rows = append(rows, []cell{p.c(b.Coin), p.cst(dir, s), p.c(strconv.Itoa(b.Units)), p.c(Px(b.Mark)), p.cm(Px(b.Stop))})
		}
		cols := []column{{title: "book", flex: true, minW: 5, keep: 100}, {title: "dir", keep: 90},
			{title: "units", right: true, keep: 50}, {title: "mark", right: true, keep: 80}, {title: "stop", right: true, keep: 60}}
		c.body = append(c.body, "")
		c.body = append(c.body, p.table(cols, rows, p.w, nil)...)
	}

	if n := len(d.Epochs); n > 0 {
		show := min(5, n)
		c.body = append(c.body, "")
		c.body = append(c.body, p.epochTable(d.Epochs[n-show:], true)...)
	}
	// The RPC endpoint and read path are plumbing: they stay in --json.
	if len(d.Unknown) > 0 {
		c.notes = append(c.notes, strings.Join(d.Unknown, ", ")+" n/a")
	}
	return c
}

func shortOr(a string) string {
	if a == "" {
		return dash
	}
	return ShortAddr(a)
}

func (p *painter) userPairs(u *pvault.UserView) [][2]string {
	// shares and value are the header; the body starts with what they
	// would redeem for now.
	kv := [][2]string{{"redeem now", USD(u.RedeemPreviewUSD) + " after exit fee"}}
	if u.PendingDepositUSD > 0 {
		kv = append(kv, [2]string{"pending in", USD(u.PendingDepositUSD) + " ≈ " + Num(u.DepositPreviewShares, 4) + " shares"})
	}
	if u.PendingRedeemShares > 0 {
		kv = append(kv, [2]string{"pending out", Num(u.PendingRedeemShares, 4) + " shares"})
	}
	if u.ClaimableShares > 0 || u.ClaimableUSD > 0 {
		kv = append(kv, [2]string{"claimable", Num(u.ClaimableShares, 4) + " shares · " + USD(u.ClaimableUSD)})
	}
	if !u.MayDeposit {
		kv = append(kv, [2]string{"deposit", "not on the allowlist"})
	}
	return kv
}

// epochTable lays out settled epochs, newest first.
func (p *painter) epochTable(rows []pvault.EpochRow, recent bool) []string {
	out := make([][]cell, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		e := rows[i]
		flow := dash
		switch {
		case e.DepositUSD > 0 && e.RedeemUSD > 0:
			flow = "+" + USD(e.DepositUSD) + " " + minus + USD(e.RedeemUSD)
		case e.DepositUSD > 0:
			flow = "+" + USD(e.DepositUSD)
		case e.RedeemUSD > 0:
			flow = minus + USD(e.RedeemUSD)
		}
		ret := p.cs(Chg(e.Return), e.Return)
		if i == 0 && e.Return == 0 {
			ret = p.cm(dash) // first row of the series: no previous price
		}
		fee := p.cm(dash)
		if e.FeeUSD > 0 {
			fee = p.c(USD(e.FeeUSD))
		}
		out = append(out, []cell{p.c(strconv.FormatUint(e.Epoch, 10)), p.cm(fillTime(e.SettledAt*1000, p.o.Location)),
			p.c(Num(e.SharePrice, 4)), ret, p.c(USD(e.NavUSD)), p.cm(flow), fee})
	}
	title := "epoch"
	if recent {
		title = "recent"
	}
	cols := []column{{title: title, keep: 100}, {title: "settled", flex: true, minW: 6, keep: 40}, {title: "px", right: true, keep: 95},
		{title: "return", right: true, keep: 90}, {title: "nav", right: true, keep: 70}, {title: "flows", right: true, keep: 50},
		{title: "fee", right: true, keep: 30}}
	return p.table(cols, out, p.w, nil)
}

// pvaultEpochsCard is the epoch-history table with a summary header.
func (p *painter) pvaultEpochsCard(r *core.Result, d *pvault.EpochTable) card {
	title := d.Name
	if title == "" {
		title = r.Title
	}
	if title == "" {
		title = "Epochs " + ShortAddr(d.Vault)
	}
	c := card{title: termSafe(vaultTitle(title)), name: true, sub: "epochs " + strconv.FormatUint(d.From, 10) + "-" + strconv.FormatUint(d.To, 10)}
	c.focal, c.focalStyle, c.numeric = SignedPct(d.Return, 1), p.sign(d.Return), true
	n := len(d.Epochs)
	if n == 0 {
		c.focal, c.focalStyle, c.numeric = "NO EPOCHS", p.st.muted, false
		return c
	}
	pairs := [][2]string{{"", p.st.text.Render(strconv.Itoa(n)) + " " + p.lbl(plural(n, "epoch", "epochs"))}}
	if d.DepositUSD > 0 || d.RedeemUSD > 0 {
		pairs = append(pairs, [2]string{"in", p.st.text.Render("+" + USD(d.DepositUSD))}, [2]string{"out", p.st.text.Render(minus + USD(d.RedeemUSD))})
	}
	if d.FeeShares > 0 {
		pairs = append(pairs, [2]string{"fees", p.st.text.Render(USD(d.FeeUSD))})
	}
	if !p.o.Compact {
		pairs = append(pairs, [2]string{"gap", p.st.text.Render(secs(uint64(max(d.MedianGapSec, 0))))})
	}
	c.body = append(c.body, p.meta(pairs...))
	budget := n
	if !p.o.AllRows {
		budget = 20
		if p.o.Compact {
			budget = inlineRows
		}
		if p.o.Height > 0 {
			budget = max(p.o.Height-len(c.body)-4, 3)
		}
		budget = min(budget, n)
	}
	c.body = append(c.body, p.more(p.epochTable(d.Epochs[n-budget:], false), n, budget)...)
	return c
}

// pvaultUserCard is one depositor.
func (p *painter) pvaultUserCard(r *core.Result, u *pvault.UserView) card {
	title := r.Title
	if title == "" {
		title = "Depositor " + ShortAddr(u.Address)
	}
	sub := ShortAddr(u.Address)
	if u.VaultName != "" {
		sub += " in " + vaultTitle(u.VaultName)
	}
	c := card{title: "Depositor", sub: termSafe(sub)}
	if u.Shares == 0 && u.PendingDepositUSD == 0 && u.PendingRedeemShares == 0 && u.ClaimableShares == 0 && u.ClaimableUSD == 0 {
		name := vaultTitle(u.VaultName)
		if name == "" {
			name = "this vault"
		}
		may := "no"
		if u.MayDeposit {
			may = "yes"
		}
		c.focal, c.focalStyle = "FLAT", p.st.muted
		c.body = []string{p.meta([2]string{"may deposit", p.st.text.Render(strings.ToUpper(may))})}
		_ = name
		return c
	}
	c.focal, c.focalStyle, c.numeric = USD(u.ValueUSD), p.st.bold, true
	c.delta, c.deltaStyle, c.deltaLabel = Num(u.Shares, 1), p.st.text, "sh"
	c.body = p.kvLines(p.userPairs(u), p.w)
	return c
}
