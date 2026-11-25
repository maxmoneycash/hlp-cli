package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/ticks"
)

// renderTicks draws get_tick_candles for a terminal or pipe: the shared
// candle renderer with an index x axis (TickBars) and one history line.
// The card layer (internal/tui/cards) does not know this card.
func (a *app) renderTicks(env *core.Env, r *core.Result, m Mode) string {
	d, ok := r.Data.(*core.TickChartData)
	if !ok || d == nil {
		return r.Title
	}
	bars := d.All
	if bars == nil {
		bars = d.Bars
	}
	head := strings.TrimSpace(r.Title)
	if len(bars) == 0 {
		return head + "\nno trades yet" + "\n" + tickHistoryLine(d.History, a.location(env))
	}
	cs := make([]domain.Candle, len(bars))
	for i, b := range bars {
		cs[i] = b.Candle()
	}
	skin := env.Config.Chart.Theme
	if skin == "" {
		skin = chart.DefaultPalette
	}
	style := chart.Style(env.Config.Chart.Style)
	if st, ok := chart.ParseStyle(d.Style); ok && d.Style != "" {
		style = st
	}
	zoom := 2
	if chart.WideBodies(style, skin) {
		zoom = 4
	}
	if d.Zoom > 0 {
		zoom = d.Zoom
	}
	h := d.Height
	if h <= 0 {
		h = a.chartHeight()
	}
	if h <= 0 {
		h = 24
	}
	coin := d.Market.Display
	if coin == "" {
		coin = d.Coin
	}
	g, _ := chart.Candles(cs, chart.CandleOpts{
		Width: a.width(), Height: max(h-1, 4), Style: style, Skin: skin, Zoom: zoom, VolRows: -1, VolumeUSD: true,
		Cursor: -1, Header: true, Coin: coin, Interval: domain.Interval(d.Label), Location: a.location(env), TickBars: true,
	}, env.Theme)
	if g == nil {
		return head
	}
	body := g.Plain()
	if !m.Plain {
		body = g.ANSI(env.Profile)
	}
	return strings.TrimRight(body, "\n") + "\n" + tickHistoryLine(d.History, a.location(env))
}

// tickHistoryLine: "local history 12,345 trades · Nov 15 09:12 → 10:31 · 2 gaps · recording".
func tickHistoryLine(h core.TickHistory, loc *time.Location) string {
	parts := []string{fmt.Sprintf("local history %s trades", groupInt(h.Trades))}
	if h.From > 0 {
		f := time.UnixMilli(h.From).In(loc)
		t := time.UnixMilli(h.To).In(loc)
		parts = append(parts, f.Format("Jan 2 15:04")+" → "+t.Format("Jan 2 15:04"))
	}
	switch {
	case h.Gaps == 1:
		parts = append(parts, "1 gap")
	case h.Gaps > 1:
		parts = append(parts, fmt.Sprintf("%d gaps", h.Gaps))
	}
	switch {
	case h.Recording:
		parts = append(parts, "recording")
	case h.RecordedElsewhere:
		parts = append(parts, "recorded by another hlp")
	}
	return strings.Join(parts, " · ")
}

func groupInt(n int64) string {
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// ticksCmd is `hlp ticks` (stored tick history) and `hlp ticks record
// COIN...` (record trades in the foreground until interrupted; no daemon).
func (a *app) ticksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ticks [coin...]",
		Short: "Local trade history behind tick charts: what is stored, and recording",
		Long: "Tick, volume and range bars (`hlp candles`) are built from trades recorded on this machine:\n" +
			"Hyperliquid has no free trade history. Every live tick chart records while it runs;\n" +
			"`hlp ticks record` keeps recording in the foreground until you stop it (no background service).\n" +
			"Retention: config [ticks] retention_days (7) and max_mb (512, all coins).",
		Example: "  hlp ticks\n  hlp ticks record BTC ETH HYPE\n  hlp ticks record BTC --for 1h --ndjson",
		GroupID: "market",
		Args:    cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			m, err := a.mode()
			if err != nil {
				return err
			}
			env, err := a.env(c.Context())
			if err != nil {
				return err
			}
			defer env.Close()
			res, err := core.TickStatus(c.Context(), env, args)
			if err != nil {
				return err
			}
			if m.JSON {
				return writeResult(a.io.Out, res, m)
			}
			_, err = fmt.Fprintln(a.io.Out, tickStatusText(res.Data.(*core.TickStatusData), a.location(env)))
			return err
		},
	}
	var every time.Duration
	rec := &cobra.Command{
		Use:     "record coin...",
		Short:   "Record trades for tick charts in the foreground until interrupted",
		Example: "  hlp ticks record BTC ETH\n  hlp ticks record HYPE --for 8h",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			m, err := a.mode()
			if err != nil {
				return err
			}
			env, err := a.env(c.Context())
			if err != nil {
				return err
			}
			defer env.Close()
			ctx, stop := context.WithCancelCause(core.WithSource(c.Context(), "cli"))
			defer stop(nil)
			if m.For > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeoutCause(ctx, m.For, errStreamBound)
				defer cancel()
			}
			events := 0
			var werr error
			err = core.RecordTicks(ctx, env, args, every, func(r *core.Result) {
				if werr != nil {
					return
				}
				if m.JSON {
					werr = writeResult(a.io.Out, r, m)
				} else {
					line := tickStatusText(r.Data.(*core.TickStatusData), a.location(env))
					if a.io.OutTTY && !m.Plain && events > 0 {
						line = "\x1b[H\x1b[2J" + line
					}
					_, werr = fmt.Fprintln(a.io.Out, line)
				}
				events++
				if m.MaxEvents > 0 && events >= m.MaxEvents {
					stop(errStreamBound)
				}
			})
			if werr != nil {
				return werr
			}
			if err != nil && (errors.Is(context.Cause(ctx), errStreamBound) || c.Context().Err() != nil) {
				return nil // --for, --max-events or Ctrl-C: a clean stop
			}
			return err
		},
	}
	rec.Flags().DurationVar(&every, "every", 10*time.Second, "status interval")
	cmd.AddCommand(rec, a.tickReplayCmd())
	return cmd
}

// tickReplayCmd emits one bounded, machine-readable local recording.
func (a *app) tickReplayCmd() *cobra.Command {
	var from, to int64
	var count int
	type barFlag struct {
		name string
		kind ticks.Kind
		help string
	}
	sizes := []barFlag{
		{"ticks", ticks.KindTicks, "trades per bar (default 500)"},
		{"volume-bars", ticks.KindVolume, "base units per bar"},
		{"usd-bars", ticks.KindUSD, "notional per bar in USD"},
		{"range-bars", ticks.KindRange, "price distance per bar"},
	}
	cmd := &cobra.Command{
		Use:     "replay coin",
		Short:   "Read locally recorded trades and their candle updates as JSON",
		Long:    "Read the newest matching trades, oldest first, from local day files. Each frame carries one print and the bars it changed. An unfinished bar includes earlier trades from its day. This command never starts a recorder or contacts the exchange.",
		Example: "  hlp ticks replay BTC --ticks 50 --count 500 --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			m, err := a.mode()
			if err != nil {
				return err
			}
			spec := ticks.Spec{Kind: ticks.KindTicks, Size: 500}
			chosen := false
			for _, size := range sizes {
				if f := c.Flags().Lookup(size.name); f.Changed {
					if chosen {
						return core.Errorf(core.CodeUsage, "choose one bar type")
					}
					n, err := core.ParseNumber(f.Value.String())
					if err != nil {
						return core.Errorf(core.CodeUsage, "--%s: %v", size.name, err)
					}
					spec, chosen = ticks.Spec{Kind: size.kind, Size: n}, true
				}
			}
			env, err := a.env(c.Context())
			if err != nil {
				return err
			}
			defer env.Close()
			batchSize := count
			if m.Limit > 0 && batchSize >= 1 && batchSize <= ticks.MaxReplayTrades {
				batchSize = min(batchSize, m.Limit)
				m.Limit = 0
			}
			res, err := core.ReplayTicks(c.Context(), env, args[0], spec, from, to, batchSize)
			if err != nil {
				return err
			}
			return writeResult(a.io.Out, res, m)
		},
	}
	for _, size := range sizes {
		cmd.Flags().String(size.name, "", size.help)
	}
	cmd.Flags().Int64Var(&from, "from", 0, "inclusive earliest trade time, epoch milliseconds")
	cmd.Flags().Int64Var(&to, "to", 0, "inclusive latest trade time, epoch milliseconds")
	cmd.Flags().IntVar(&count, "count", 500, fmt.Sprintf("maximum newest matching trades (1-%d)", ticks.MaxReplayTrades))
	return cmd
}

func tickStatusText(d *core.TickStatusData, loc *time.Location) string {
	var b strings.Builder
	if len(d.Coins) == 0 {
		b.WriteString("no tick history yet: `hlp candles COIN --live` or `hlp ticks record COIN` records trades\n")
	}
	for _, c := range d.Coins {
		fmt.Fprintf(&b, "%-12s %12s trades  %8s", c.Coin, groupInt(c.Trades), mb(c.Bytes))
		if c.From > 0 {
			fmt.Fprintf(&b, "  %s → %s", time.UnixMilli(c.From).In(loc).Format("Jan 2 15:04"), time.UnixMilli(c.To).In(loc).Format("Jan 2 15:04"))
		}
		if c.Recording {
			fmt.Fprintf(&b, "  recording (+%s", groupInt(int64(c.Session)))
			if c.GapsSeen > 0 {
				fmt.Fprintf(&b, ", gaps %d", c.GapsSeen)
			}
			b.WriteString(")")
		}
		if c.Error != "" {
			b.WriteString("  " + c.Error)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "local only · %s", d.Path)
	return b.String()
}

func mb(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
