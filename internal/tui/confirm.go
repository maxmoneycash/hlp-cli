package tui

import (
	"context"
	"strings"
	"time"

	bkey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/tui/cards"
)

// confirmReq is a mutating tool asking the human (via Env.Confirm).
type confirmReq struct {
	preview core.OrderPreview
	reply   chan bool
}

type confirmReqMsg confirmReq

// confirmGuard is how long key presses are dropped after a confirm card
// opens: the card appears while the user may still be typing in the chat
// input, and those keys must not answer it (audit S5).
const confirmGuard = 400 * time.Millisecond

// confirmState is the open confirm card: the order preview plus a huh
// confirm. close_all asks twice.
type confirmState struct {
	req      confirmReq
	form     *huh.Form
	answer   bool
	stage    int
	openedAt time.Time // when the current stage's form opened (confirmGuard)
}

// confirmer implements core.Confirmer for the shell. It runs on the tool's
// goroutine and blocks until the human answers or ctx ends.
func (m *Model) confirmer(ctx context.Context, p core.OrderPreview) (bool, error) {
	req := confirmReq{preview: p, reply: make(chan bool, 1)}
	select {
	case m.confirmCh <- req:
	case <-ctx.Done():
		return false, ctx.Err()
	case <-m.ctx.Done():
		return false, m.ctx.Err()
	}
	select {
	case ok := <-req.reply:
		return ok, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-m.ctx.Done():
		return false, m.ctx.Err()
	}
}

func (m *Model) waitConfirm() tea.Cmd {
	ch, done := m.confirmCh, m.ctx.Done()
	return func() tea.Msg {
		select {
		case req := <-ch:
			return confirmReqMsg(req)
		case <-done:
			return nil
		}
	}
}

func (m *Model) openConfirm(req confirmReq) tea.Cmd {
	m.confirm = &confirmState{req: req}
	return m.confirmForm()
}

func (m *Model) confirmForm() tea.Cmd {
	c := m.confirm
	p := c.req.preview
	// The form is never drawn (confirmView draws the keycap); it keeps
	// the answer, the No default and the key guard.
	title, desc := "confirm", ""
	_ = p
	c.answer = false
	c.openedAt = m.now()
	c.form = huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(title).Description(desc).Affirmative("Yes").Negative("No").Value(&c.answer),
	)).WithShowHelp(false).WithTheme(m.huhTheme()).WithKeyMap(confirmKeyMap()).WithWidth(max(min(m.width-4, 72), 30))
	return c.form.Init()
}

// confirmKeyMap is huh's keymap without the single-letter answers: by
// default "y" accepts and submits at once, and "h"/"l" toggle, so a stray
// "yes…" or "hello" typed into the chat could approve an order (audit S5).
// Answering takes ←/→ (or tab to move) and enter; "No" is the default.
func confirmKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Confirm.Accept.SetEnabled(false)
	km.Confirm.Reject.SetEnabled(false)
	km.Confirm.Toggle = bkey.NewBinding(bkey.WithKeys("left", "right"), bkey.WithHelp("←/→", "toggle"))
	return km
}

// huhTheme maps the active theme onto huh's styles.
func (m *Model) huhTheme() huh.Theme {
	return huh.ThemeFunc(func(isDark bool) *huh.Styles {
		s := huh.ThemeBase(isDark)
		s.Focused.Base = s.Focused.Base.BorderForeground(m.th.Warn.Resolve(m.profile))
		s.Focused.Title = m.st.bold
		s.Focused.Description = m.st.muted
		btn := lipgloss.NewStyle().Padding(0, 2).MarginRight(1)
		s.Focused.FocusedButton = btn.Reverse(true).Bold(true)
		if c := m.th.Accent.Resolve(m.profile); c != nil {
			s.Focused.FocusedButton = btn.Foreground(c).Reverse(true).Bold(true)
		}
		s.Focused.BlurredButton = btn.Inherit(m.st.muted)
		s.Blurred = s.Focused
		return s
	})
}

func netOr(n domain.Network) domain.Network {
	if n == "" {
		return domain.Mainnet
	}
	return n
}

// confirmKey routes keys to the open confirm. Esc and ctrl+c always cancel;
// any other key within confirmGuard of the card opening is dropped.
func (m *Model) confirmKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.answerConfirm(false)
		return m.waitConfirm()
	}
	if m.now().Sub(m.confirm.openedAt) < confirmGuard {
		return nil
	}
	return m.formUpdate(msg)
}

// formUpdate feeds any message to the open form and acts on completion
// (huh completes through its own follow-up messages, not on the key).
func (m *Model) formUpdate(msg tea.Msg) tea.Cmd {
	c := m.confirm
	model, cmd := c.form.Update(msg)
	if f, ok := model.(*huh.Form); ok {
		c.form = f
	}
	switch c.form.State {
	case huh.StateCompleted:
		if c.answer && c.req.preview.Action == "close_all" && c.stage == 0 {
			c.stage = 1
			return m.confirmForm()
		}
		m.answerConfirm(c.answer)
		return m.waitConfirm()
	case huh.StateAborted:
		m.answerConfirm(false)
		return m.waitConfirm()
	}
	return cmd
}

func (m *Model) answerConfirm(ok bool) {
	if m.confirm == nil {
		return
	}
	m.confirm.req.reply <- ok
	m.confirm = nil
}

// confirmView is the order preview card with the keycap under it
// (DESIGN.md 4.2): no box; the selected answer is the one keycap (Accent
// face, AccentEdge lip), the other is plain muted text. ←→ choose, enter
// answers; the huh form underneath keeps the key guard and the No default.
func (m *Model) confirmView(width int) []string {
	c := m.confirm
	p := c.req.preview
	card := cards.OrderPreview(p, m.cardOpts(min(width, 72+4), false))
	lines := append([]string{""}, strings.Split(card, "\n")...)
	lines = append(lines, "")
	yes := "CONFIRM"
	if p.Action == "close_all" {
		yes = "CLOSE ALL"
		if c.stage == 1 {
			yes = "REALLY CLOSE ALL"
		}
	}
	no := "cancel"
	var row, lip string
	if c.answer {
		face := m.st.keycap.Render(yes)
		row = gutter + face + "    " + m.st.muted.Render(no)
		if m.st.keycapLip {
			lip = gutter + m.st.keycapEdge.Render(strings.Repeat("▀", lipgloss.Width(face)))
		}
	} else {
		face := m.st.keycap.Render(strings.ToUpper(no))
		lead := gutter + m.st.muted.Render(strings.ToLower(yes)) + "    "
		row = lead + face
		if m.st.keycapLip {
			lip = strings.Repeat(" ", lipgloss.Width(lead)) + m.st.keycapEdge.Render(strings.Repeat("▀", lipgloss.Width(face)))
		}
	}
	if !p.DryRun && netOr(p.Network) == domain.Mainnet {
		row += "    " + m.st.warnPill.Render("MAINNET")
	}
	lines = append(lines, m.withKeys(row, width))
	if lip != "" {
		lines = append(lines, lip)
	}
	lines = append(lines, "")
	return lines
}
