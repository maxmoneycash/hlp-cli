# hlp design spec: the "rush" look

Status: binding. Every change to `internal/theme`, `internal/tui` and `internal/tui/cards` follows this file. When code and this file disagree, the code is wrong.

The direction in one line: say less. The interface shows the number you came for, in ink on near-black with one Hyperliquid mint accent, and nothing that repeats what is already on screen. Terminal references: lazygit, k9s and btop.

Section numbers below are cited from code comments (for example `DESIGN.md section 5.1 rule 6`). "Before" in the tables refers to an earlier, wordier build.

---

## 1. The one idea

**A quiet, near-black terminal: real words only on the thing you asked for, one big number, one Hyperliquid mint accent, and nothing else talking.**

The landing page works because of what it leaves out. It has six statements, six sentences and mono labels of three words or fewer, and each section shows one live object. hlp currently does the opposite:

- The welcome block (tagline, ticker and "try" line, 6 rows) stays at the top of the transcript forever, so every screenshot spends 25% of its words on it.
- Every card ends with a `source · as of HH:MM:SS` line and up to three sentence-long warnings.
- The footer lists five key hints all the time.

The fix has three parts:

- Cut words, not data.
- Move secondary fields behind a key.
- Make a number, not a sentence, the focal point of each screen.

Six things go wrong, and each has a rule here:

| Failure | Rule |
|---|---|
| Too much text | §5 Text diet |
| Teal plus yellow plus green plus red plus purple on one screen | §2 Palette: one accent, white for gains |
| Every line the same weight | §3 Type: one big number, labels dim |
| A box or border around everything | §4: no boxes, corner reticle on one card only |
| Void under the content | §6: the floor grid and the wordmark fill the empty state |
| Decorative motion | §7: only flaps and the cursor move |

---

## 2. Palette: rush tokens

The new built-in theme `rush` becomes the default for `auto` and empty config, on both dark and light terminals. On a light terminal the existing `themeMismatch` path repaints the background with OSC 11, as it already does for a polarity mismatch. The old `hlp` (teal) theme stays registered and selectable. Nothing else changes for the other themes.

Mapping from setup-rush:

- **Landing surfaces** (`landing/landing.css:10-16`, one step lighter than the app): background `#131315`, card `#1a1a1d`, secondary `#202023`, border `#303034`, muted surface `#171719`.
- **App tokens** (`index.css:42-71`): foreground `#f4f3ef`, muted `#97979c`, orange `#ff5a1f`, bear red `#ff4b3e`, gold `#e9b949`, cyan `#8ab4f8`, violet `#b4a2ff`.

| Token (Theme field) | Role | Truecolor | 256 | 16 | Source |
|---|---|---|---|---|---|
| `BG` | ground; painted by OSC 11 when `ui.paint_background` is true (default for rush) | `#131315` | 233 | 0 | landing background |
| `Surface` | card face: selected row, flap cells, pane header band | `#1a1a1d` | 234 | -2 (reverse video) | landing card |
| `Raised` (new) | hover/selected on a surface, keycap press | `#202023` | 235 | -2 | landing secondary |
| `Border` | hairline rules, reticle when blurred, table rules | `#303034` | 236 | 8 | landing border |
| `Grid` | chart gridlines | `#1e1e21` | 234 | -2 | mix(BG, ink, 4%) |
| `Floor` (new) | floor-grid dots | `#2c2c30` | 236 | -2 (omitted) | dot `rgba(244,243,239,.07)`, lifted for a cell-sized dot |
| `Text` | ink: titles, values, gains | `#f4f3ef` | 255 | 15 | foreground |
| `Muted` | labels, units, secondary values | `#97979c` | 246 | 7 | muted-foreground |
| `Dim` | meta, placeholders, ghost hints, axis ticks | `#6b6b70` | 242 | 8 | between muted and border |
| `Accent` | **the only accent**: prompt `❯`, focused reticle, live `■`, selection bar `▌`, keycap face | `#97fce4` | 122 | 14 | primary / Hyperliquid mint |
| `AccentEdge` (new) | keycap lip under the face | `#2f7d6d` | 30 | -2 | keycap edge |
| `Up` | gains, bull candles | `#f4f3ef` | 255 | 15 | bull = foreground |
| `Down` | losses, bear candles, failures | `#ff4b3e` | 203 | 9 | bear |
| `Warn` | stale data, testnet, notes (`!`) | `#e9b949` | 179 | 11 | gold |
| `Error` | errors | `#ff4b3e` | 203 | 9 | destructive |
| `Success` | "ok" states (rare; prefer no colour) | `#f4f3ef` | 255 | 15 | no green on the landing |
| `MA[0..2]` | indicator and benchmark lines | `#8ab4f8`, `#b4a2ff`, `#e9b949` | 111, 147, 179 | 12, 13, 11 | cyan, violet, gold |
| `Accent2` | second series in compare charts only | `#8ab4f8` | 111 | 12 | cyan |
| `AxisLine` / `AxisText` / `Cursor` | chart chrome | `#303034` / `#97979c` / `#202023` | 236 / 246 / 235 | 8 / 7 / -2 | |
| `VolMix` | volume bars | 0.30 | | | |

Rules:

1. **Gains are white, losses are red, and the sign carries direction.** There is no green and no teal anywhere in rush. Every signed number shows its sign: `+` for gains and U+2212 `−` for losses, never the ASCII hyphen, in cards, ticker and tables alike. `cards.SignedPct`, `SignedUSD` and the chart's change labels emit U+2212. Plain mode (`--plain`, MCP text, JSON) keeps ASCII `-`, so machine output does not change.
2. **The accent marks one thing at a time.** These are the only places it appears:
   - the prompt `❯`;
   - the reticle corners on the focused card;
   - the live `■` in the top bar;
   - the `▌` bar on the selected row of a list, completion or palette;
   - the keycap on a confirm.

   The accent is never used for titles, headers, links, numbers or the brand name.
3. **Status colours mean status.** Red is only for a negative number, an error, or a check that failed. Gold is only for a warning (`!`, STALE, TESTNET). Max drawdown is a negative number by definition, so it is shown in ink, not red. It turns red only when it is worse than the configured `risk.max_dd`, or than −30% when that is unset.
4. **Flat.** No borders around cards, no rounded boxes, no gradients, no fills used as decoration. Depth comes from the one surface step (`Surface` on `BG`). Tinted fills are allowed only where they encode a value: heatmap cells, depth bars and the drawdown area.
5. **Charts match.** In the TUI, theme `rush` uses the chart skin `rush`: the setup-rush "terminal" palette (ink `#f4f3ef` up, `#ff4b3e` down) with its background set to the ground `#131315`, so a chart is not a darker rectangle on the page. Up bodies are hollow (an ink outline on the ground, as the landing's ChartWall draws them) and down bodies solid red, on both `rush` and `terminal`; inline cards draw 3-column bodies so the outline reads as a box. The theme `paper` is the same rules on the paper skin's ground (`#c6c4ba`): `/theme paper` repaints the whole app (OSC 11), not just the chart, and the paper skin's live tag and volume are ink, not green.
6. **Fallbacks.** At 256 colours use the hand-picked indexes above. Never rely on automatic downsampling, which can land the accent next to a status colour and collides with Down. At 16 colours:
   - surfaces become reverse video;
   - the floor grid and the flap cell faces are omitted;
   - the accent is yellow (3), and warn is bright yellow (11) and always carries its `!` glyph or a word label, so the two never rely on hue alone.

   With no colour (ASCII), selection is reverse video, the focused reticle is still drawn, and signs carry direction.

Contrast on `#131315`:

| Colour | Ratio | Allowed for |
|---|---|---|
| `#f4f3ef` | 17.0:1 | any text |
| `#97979c` | 6.6:1 | any text |
| `#97fce4` | 15.6:1 | any text |
| `#ff4b3e` | 5.6:1 | any text |
| `#6b6b70` | 3.5:1 | Dim only: meta and ghost text, never a value someone must read |

---

## 3. Type in a terminal

A terminal has one face and one size. The landing's seven type styles collapse to four treatments:

| Landing style | Terminal treatment | Use |
|---|---|---|
| `t-hero` / `t-num` (display 800-900, tabular) | **Big digits** (§3.1), or **bold ink** where big digits do not fit | the one focal number per screen |
| `t-title` / `t-head` (display caps / Archivo 600) | **bold ink**, UPPERCASE for card titles of 3 words or fewer; names keep their own case | card title, vault name |
| `t-body` (muted) | ink for values, muted for prose | rows, agent replies |
| `t-label` (mono 600, 11px, uppercase, 0.1em tracking) | **UPPERCASE, Muted, not bold**, words separated by two spaces | column headers, section labels, ticker symbols, meta tags (`TVL`, `MAX DD`, `24H`) |

Rules:

1. **Hierarchy is set with bold and dim only.** Bold is used for the title and the focal number, nothing else. Never use italic, underline (except links in agent markdown), blink attributes or background highlights for emphasis.
2. **Labels are UPPERCASE, values are not.** For example `TVL $151.6M`, `SHARPE 1.83`, `MAX DD −5.8%`. The label is muted and the value is ink. To get the landing's tracked-label feel, put two spaces between label words (`MAX  DD`) and three spaces between label/value pairs. Never letter-space within a word: it breaks copy-paste and screen readers.
3. **Numbers are tabular and right-aligned in columns.**
   - Percentages take one decimal (`+24.0%`). Use two decimals only below 1% (`+0.04%`) and on funding.
   - Prices use `cards.Px`, which follows the landing's `fmtPrice`: no decimals at 1000 or more, 5 significant figures at 1 or more, 4 below 1.
   - Money uses `$151.6M`, `$2.3K` or `$346`, never more than 4 significant figures in chrome.
4. **Prefer a number plus a unit to a word.** `3.5Y` not `age 3.5y`. `4D LOCK` not `lockup 4d`. `100+ DEP` only in the pane.
5. **The prompt motif.**
   - `❯` in Accent, then a space, then a block cursor (reverse ink).
   - The echo of what was typed keeps the `❯`, in Dim, with the text muted.
   - The cursor blinks (§7).

### 3.1 Big digits

These are a compact 3-row display face for the hero number. Each glyph is 3 cells wide with a 1-cell gap, and each thousands group is separated by one more blank cell instead of a comma.

```
0    1    2    3    4    5    6    7    8    9    .   +    −
█▀█  ▀█   ▀▀█  ▀▀█  █ █  █▀▀  █▀▀  ▀▀█  █▀█  █▀█        ▄
█ █   █   █▀▀   ▀█  ▀▀█  ▀▀█  █▀█    █  █▀█  ▀▀█      ▀█▀  ▀▀▀
▀▀▀  ▀▀▀  ▀▀▀  ▀▀▀    ▀  ▀▀▀  ▀▀▀    ▀  ▀▀▀  ▀▀▀  ▀
```

Wordmark letters in the same face, used only on the welcome:

```
H    L    P
█ █  █    █▀█
█▀█  █    █▀▀
▀ ▀  ▀▀▀  ▀
```

Rules:

- Only digits, `.`, `+` and `−` are drawn big. The currency sign and unit (`$`, `%`, `M`, `X`) are drawn small and bold on the bottom row, right after the number: big `64 953` then small `−0.9%  24H`. The landing does the same with its hero number and `t-label`.
- **Where:** one per card: in the header band of a full-screen pane (ctrl+o), and under the title of an inline card:
  - chart: last price, or the crosshair close while the crosshair is on;
  - vault tearsheet: all-time return;
  - strategy vault: NAV;
  - account: equity;
  - backtest: total return;
  - arena: the leader's return.

  Inline transcript cards lead with their focal number too, in the same face, directly under the title (the landing's tile number, `−0.9%`), instead of a bold value at the far right of the header. Money keeps its sign big (`$` is in the face for inline cards: `$157.1` then small `M  TVL`). The book is the exception: its mid is bold ink on the ladder's spread row. A 2-row 3 × 4-dot face was tried and dropped: at that size 6, 8 and 0 read as blocks.
- **Fallback:** below 24 rows or 60 columns, or in ASCII/plain mode, draw the number as one bold ink line.
- Colour: ink, or red when negative. Gains are never coloured.

---

## 4. Shapes and marks

The kit has exactly these marks. Any other glyph used as an icon (`✓ ✗ ● ○ ◌ ‖ • ∞ ⏎ ▲ ▼ →`) is removed.

| Mark | Meaning | Colour |
|---|---|---|
| `❯` | prompt; echo of a typed line | Accent; Dim on the echo |
| `■` | live / pass / curated row / error lead | Accent (live), Text (pass), Down (error) |
| `□` | not live (idle, paused, offline) / n/a | Dim |
| `▌` | selected row in a list | Accent |
| `┌─ ─┐ └─ ─┘` | reticle on the focused card | Accent focused, otherwise not drawn |
| `!` | warning or note | Warn |
| `·` | floor dot; separator in a meta line (sparingly) | Floor / Dim |
| `−` `+` | sign | value colour |
| `›` | breadcrumb in the top bar | Dim |
| `─` | the one hairline above the prompt | Border |

`∞` is replaced by the word `NO CAP`. `✓` in the checklist becomes `■` (Text, pass), `■` (Down, fail) or `□` (Dim, n/a).

### 4.1 Corner reticle (lock-on)

The landing's `lockon` brackets go only on the object that matters. In hlp that is **the focused card**: the one `enter` or ctrl+o opens (by default the newest card) or the one under the mouse.

```
┌─                                                     ─┐
  ❯ /vault hlp
  HYPERLIQUIDITY PROVIDER                    +367.9%  ALL
  ...card body...
└─                                                     ─┘
```

- The corners are 2 cells: `┌─` / `─┐` on the blank row above the echo line, and `└─` / `─┘` on the blank row below the card's last line. They are drawn into the blank separator rows `layoutTranscript` already emits, so focusing never shifts the layout. The transcript always keeps one blank row after the last entry for this.
- Left corners are in column 0 (the gutter). Right corners end at the card's right edge, not the terminal edge.
- Exactly one reticle is on screen, and none while the input has text or the palette or help is open (focus is on the prompt then).
- Static: no bite or snap animation (§7).
- Full boxes are never drawn: no `│` sides and no `─` runs between the corners.

### 4.2 Keycap

The one primary action on a confirm is drawn as a keycap. The face is Accent with BG-coloured text, and a lip row sits under it in AccentEdge `▀`. There is one keycap per screen, and the secondary choice is plain muted text.

```
   CONFIRM    cancel
 ▀▀▀▀▀▀▀▀▀
```

At 16 colours the face is reverse video and there is no lip.

### 4.3 Floor grid

This is the dotted floor (`bg-floor`), the ground of the transcript, as on the landing:

- the welcome screen before the first command;
- every transcript row below the last card, and the ground beside each card (it starts 2 cells past the right edge of the card's group and never paints inside a card);
- the void inside an empty full-screen pane;
- `no results` states.

Details:

- Draw `·` in `Floor` every 4 columns and every 2 rows, starting at column 2, behind nothing: it is never drawn under text, and a cell with content wins.
- It is omitted at 16 colours and in ASCII mode, and when `ui.floor = false`.
- There is no spotlight.

### 4.4 Split-flap ticker

The live market strip uses the landing's `MarketFlap`. It lives in the top bar (§6.1):

```
BTC  64,951  −0.9%    ETH  1,825.9  −0.2%    HYPE  67.14  +2.8%    ■
```

- The symbol is a muted UPPERCASE label, with the `xyz:` prefix stripped. The price is ink tabular. The change is ink or red with its sign, 1 decimal.
- Each character of price and change sits on a `Surface` cell (the flap face); the symbol does not.
- There are 4 cells between items. Items are dropped from the right when they do not fit, never cut.
- The coins come from `config.Pinned`, or `BTC ETH HYPE xyz:SP500` by default.
- Flap motion is described in §7.

---

## 5. Text diet

### 5.1 Global rules (apply to every screen)

1. **One line of chrome at the top and one at the bottom.**
   - Top: the top bar.
   - Bottom: one hairline plus the prompt row, which carries the context keys on its right.
   - There is no separate hint row in transcript mode. Panes get one key row instead of the prompt.
2. **No sentences in chrome.** Top bar, key hints, card headers, footers, table headers and badges are labels of 4 words or fewer.
3. **Say it once.**
   - If the top bar shows the network, cards do not.
   - If the ticker shows BTC, the welcome does not.
   - If a header shows a value, the body does not repeat it.
4. **No source or as-of footers.**
   - A card shows its time only when it matters: `STALE 3M` (gold) when `meta.stale`, or `14:02` (Dim) on a non-live historical result.
   - Source and as-of are shown in the full pane's info row (key `i`).
   - The JSON contract (`meta.source`, `meta.asOf`) is unchanged.
5. **Warnings collapse to a tag.**
   - Inline, all of a card's warnings become one gold tag of 4 words or fewer on the card's last line (`! 49 CLIPPED`, `! HISTORY 201D OLD`, `! NOT INVESTABLE`), with ` +N` when there are more.
   - The full text is in the pane under a `NOTES` label.
   - Internal plumbing (RPC fallbacks, TLS retries, "via lens @ …") is never shown in the TUI. It stays in `meta.warnings` for agents.
6. **No hint walls.**
   - No `try …`, `enter opens the screener`, `/index <slug> for weights`, `hlp strat <id> for the live book`, or `Set [llm] in config.toml…` lines.
   - A tip is shown **once ever**, then never again. Seen tips are persisted in `<data dir>/seen.json`, and `/help` lists them.
7. **Captions are 4 words or fewer.** Section labels have no counts in parentheses (`ALLOCATION`, not `Latest allocation (12 rebalances)`).
8. **Numbers over words.**
   - `5MO`, not `age 5mo`.
   - Signed size `−717,362`, not a `short` column.
   - `2 ORDERS`, not `2 open orders`.
9. **Secondary fields go behind a key.**
   - A transcript card shows at most **6 columns** and **5 rows** (8 for positions), plus a `+N` Dim count.
   - ctrl+o (or `enter` on an empty prompt) opens the full pane with every column and row.
10. **Echo lines are cut, not wrapped.**
    - An address is middle-truncated (`0xb83d…6e36`).
    - A file path shows its basename (`ema-cross.pine`).
    - `--flag` lists of more than 2 items show `+N`.
    - One line, muted.
11. **No AI tells.**
    - No em-dash or en-dash anywhere visible.
    - No "Here are…" preambles in system text.
    - No fake precision: the screener's `7.17` and the curve's `1.83` Sharpe are not shown side by side. Use one, labelled.
12. **Copy self-audit before merge.** Run the word count in §9 on the after shots. Any screen over budget fails review.

### 5.2 Per-screen table

"Now" quotes the before shots. "Stays" is the whole visible text of the after state.

#### Shell

| Screen | Now | Cut | Stays |
|---|---|---|---|
| **Welcome** (01) | `hlp mainnet` · `The Hyperliquid terminal for vaults.` · ticker line · `try /chart HYPE 1h /vaults /vault hlp or ask top vaults by sharpe` · placeholder `ask, or type / for commands` · 5-key footer. 41 words. | Tagline. The ticker line (it moves to the top bar). `try`, `or ask`, the sentence example. The footer row. The whole welcome entry leaves the transcript when the first command runs: it is an empty state, not history. | Floor grid. The `HLP` wordmark in big letters, left-aligned at column 4 and about 40% down, framed by the reticle. **First run only**, one Dim line under it: `/chart BTC   /vaults   /vault hlp`. Placeholder: `ask or /`. |
| **Top bar** (all) | `hlp  mainnet  [addr]  › pane  tags  ⠋ loading` … `● live  no model` / `local openai:fake-local` | `mainnet` (the default is never shown). Account address (it lives in the ctrl+b side panel). Chart tags (they live in the pane legend). `no model` (it shows in `/help` and when you type plain text). Model name (in `/status`). The word `live`. The braille spinner and `loading`. | Left: `hlp` bold ink, then `TESTNET` gold reverse pill only on testnet, then `›` plus the pane name (UPPERCASE) in a pane. Right: the flap ticker (§4.4) in transcript mode, then the live mark (`■` Accent live, `■` Dim connecting, `□` Dim paused or offline). The ticker is hidden in panes; the live mark stays. |
| **Bottom bar / footer** | `enter send · / commands · ctrl+k palette · ctrl+o open card · ? help` (always) | All five hints in idle state. | Idle: nothing but `? keys` (Dim) at the right end of the prompt row. While a job runs: `esc stop`. With completions open: `tab` and `esc` only. In a pane: one key row of 3 hints or fewer plus `? keys` (§5.2 Panes). |
| **Input / placeholder** | `❯ ask, or type / for commands` | `, or type`, `for commands` | `❯ ask or /` placeholder (first screen only; after that, empty). The ghost argument hint stays (Dim, e.g. `<interval>`). |
| **Autocomplete** (14) | 6 rows of `/vault <vault>  Vault tearsheet: equity/NAV curve, drawdown, monthly returns, metrics vs HLP/BTC/HYPE, checklist` | Summary sentences. Argument hints on unselected rows. `+N more`. | Up to 5 rows: `/name`, with the selected row `▌` Accent plus bold, its argument hint Dim, and one caption of 4 words or fewer (`tearsheet`, `screener`, `NAV and epochs`). `+N` Dim if more. |
| **Palette** (ctrl+k) | `› run a command…` · rows with full summaries | `run a command…`, summaries over 4 words | `›` filter row; rows `/name  caption≤4w`; `no match` |
| **Help** (15) | `Keys` plus 11 rows with sentences (`send · open the latest card when the prompt is empty`, `(mouse wheel too)`, `next theme (hlp)`) · `Commands` by group | Every parenthetical and every description over 3 words. The `ctrl+c ×2` row (it is said when pressed). The theme name. | Two columns. `KEYS`: `enter send/open`, `tab complete`, `↑↓ history`, `pgup scroll`, `ctrl+o open`, `ctrl+k palette`, `ctrl+b side`, `ctrl+t theme`, `esc back`. `COMMANDS`: groups as UPPERCASE labels with `/names`. Last line: model status (`MODEL  none` or `MODEL  openai:…`) and the seen tips. |
| **Side panel** (ctrl+b) | `Watchlist` · rows · `Positions` · `/account <addr> to track` / `positions here` · `offline: no stream` | Both hint lines. `offline: no stream` (the top bar's `□` says it). | `WATCH` rows (`BTC  64,951  −0.9%`); `POSITIONS` rows (`HYPE  −$5.9M`) or nothing; `EQUITY  $152.5M`. |
| **Pending card** | title plus `⠋ 1.2s` / `working` and skeleton bars | Spinner frames, `working` | Card title plus skeleton rows (static `─` in Border). The focal slot shows `1S`, `2S`… (Dim, updated once a second). |
| **Interrupted / no result** | `interrupted: <title>` / `no result: <title>` | Sentence form | `□ STOPPED` / `□ NO RESULT` (Dim) in the card's focal slot; title kept. |
| **Errors** (16) | `✗ Unknown coin "FOOBARX"` / `list markets with `/markets --query FOOBARX` · UNKNOWN_COIN` | `✗`, `list markets with`, backticks, the machine code (it is in `--json`) | `■` (Down) and the message, 8 words or fewer (`Unknown coin FOOBARX`). Next line, Dim: the runnable command only (`/markets FOOBARX`). |
| **Unknown command** | `No command /opne. Did you mean /open? … /help` | Sentences | `/opne?  /open` (the suggestion in ink). |
| **No model, plain text** (16b, `noModelHint`) | `I didn't catch that, and no model is configured. Try /chart BTC 4h, /vaults, /vault hlp.` plus `Set [llm] in config.toml (ollama works offline) to chat in plain words.` | Both sentences after the first time | First time (tip, once ever): `! NO MODEL  /help model` and up to 3 matched commands. After that: only the matched commands, or `NO MODEL` (Dim). The mis-parse in 16b (free text run as a vault query) is an agent bug, outside this spec. |
| **Agent replies** (17, 18) prose in Muted, values and commands in ink | Markdown paragraphs that restate the card (`Ranked by Sharpe … the leaders right now are: 1. HLP: Sharpe 7.17, max drawdown −5.8%` and a closing caution sentence) | Restating numbers the card already shows (rule 5.1.3). Preambles. Headings above bold. Inline code with a background (`/vault <name>` on red). | The card, then at most 2 sentences (40 words or fewer), wrapped at 72 columns or fewer, in Muted (prose is muted, values ink, so the reply never competes with the card). Commands inline in ink, no background. This needs a system-prompt change in `internal/agent` (outside these packages; see DESIGN-TASKS "Follow-ups"). |
| **Theme / candle style lists** | `themes: a b c …` sentences | `themes:` prose | `THEME  rush` plus the names in ink, current one `▌`. |
| **Confirm** (order preview) | huh form, Warn border, buttons | The border box | The order card, then the keycap `CONFIRM` plus muted `cancel`. The title is the order itself (`BUY 0.5 BTC @ 64,950`). `MAINNET` gold pill only on mainnet live. |

#### Transcript cards

Every card header is `TITLE  sub` on the left and `FOCAL  delta` on the right. Header and body are adjacent. Inside a card, the only blank row is the one before a section label.

| Card | Now | Cut | Stays |
|---|---|---|---|
| **Chart, inline** (02, 04) | `BTC 15m ·MA20 ·MA50` / `64,953 −0.89% 24h` · chart · `funding +0.0013% /h · +11.0% APR  OI $2.34B  24h vol $6.74B` · `Hyperliquid · as of Nov 15 19:34:48` | MA legend (pane only), hourly funding, source and as-of line | Header `BTC  15M` / `64,953  −0.9%  24H`; the chart; one meta line `FUNDING  +11.0%   OI  $2.34B   VOL  $6.74B`. 3 lines of text. |
| **Book** (10) | `ETH book` · `Price Size # Total` · 10 levels · `spread 0.1 0.005%` · footer | `#` column, `Total` column, source footer, the mid repeated in header and spread row | Header `ETH  BOOK` / mid; `PRICE SIZE` plus depth bar; spread row `SPREAD  0.01%`. Bars fill at most 40 cells (asks Down at 35% mix, bids Text at 25% mix). Card at most 72 cols. |
| **Positions / account** (11) | `Account 0xb83d…6e36` · `$152.5M +$54.8M uPnL` · `perp … spot … withdrawable … leverage …` · 11 columns × 10 rows · `+24 more` · `2 open orders` · footer | `side` column (size is signed), `entry`, `mark`, `lev`, `liq` price, the perp/spot split, `uPnL` word after the delta, footer | Header `ACCOUNT  0xb83d…6e36` / `$152.5M  +$54.8M`; meta `LEV 1.26X   FREE $333   2 ORDERS`; columns `COIN SIZE VALUE UPNL ROE LIQ` (LIQ = distance to liquidation, %), 8 rows, `+26`. |
| **Fills / trades** | `… fills` · `buys 62% of $1.2M` · counts, fees, footer | Footer; `buys … of …` sentence | `FILLS` / realized focal; columns `TIME COIN SIZE PX PNL`; meta `FEES  $12`. Trades: `BUY 62%` label. |
| **Markets / funding** | Title, count focal, many columns, `longs pay`/`shorts pay` rows, `avg … low … high … APR over 72h` sentence | Sentence | Columns of 6 or fewer, 5 rows; meta `AVG +11%  LOW +2%  HIGH +30%  72H`. |
| **Vault list** (06, 07, 17) | `Vaults` / `$218.0M 50 vaults`; 10 columns × 10 rows with `•` curated marks; `+40 more · enter opens the screener`; `Hyperliquid + hlp.xyz · as of … • curated by hlp.xyz (sharpe, DD all-time)` | `7d`, `all`, `sharpe`, `age` columns (pane), `•` marks, `enter opens the screener`, footer, curated explanation | Header `VAULTS` / `$218.0M  50`; columns `VAULT TVL APR 30D MAXDD TREND`; 5 rows; curated vault names in ink, others muted; Dim `+45`. Empty: `NO MATCH` with floor grid, no "try a lower --min-tvl" sentence. |
| **Vault tearsheet** (08) | Title plus address; `+367.9% all time`; `TVL $151.6M age 3.5y leader 0.2% room ∞ depositors 100+ lockup 4d • curated`; legend `— Hyperliquidity … — BTC — HYPE`; curve; `sharpe 7.17 · daily 1.83 max DD −5.8% CAGR +55.0%`; footer; a 20-word warning | Address (pane), `leader`, `room ∞`, `depositors`, `• curated` (it moves to a `■` before the title), one of the two Sharpes, footer, the warning sentence | Header `■ HYPERLIQUIDITY PROVIDER` / `+367.9%  ALL`; meta `TVL $151.6M   3.5Y   4D LOCK`; curve (main line ink, benchmarks cyan/violet/gold, legend `HLP BTC HYPE` in label style only when benchmarks are drawn); stats `SHARPE 1.83   MAX DD −5.8%   CAGR +55.0%`; tag `! HISTORY 201D OLD`. |
| **Compare** | Many metrics plus `Correlation of daily returns` plus `|ρ| ≥ 0.7 highlighted` | Caption sentence | `CORRELATION` label; cells tinted by value. |
| **Strategy vault (`/fund`, pvault)** (09) | Title plus `HyperEVM testnet · 0x688d…aabd`; `$346 1.0171 /share`; `hwm 1.0612 −4.2% vs high-water mark`; 6 key/value rows (epoch, capital, deploy room, fees, bot, agent); `recent` table of 3 epochs; footer with RPC host; 2 warning sentences | Chain plus address (pane), `vs high-water mark`, capital, deploy room, fees, bot, agent rows (pane), recent epochs table (pane), footer, RPC warning (never shown), prototype sentence | Header `TREND PYRAMID` plus `TESTNET` pill / `$346  1.0171`; meta `EPOCH 485   CUTOFF 19:43   HWM −4.2%`; a NAV sparkline 1 row high; tag `! NOT INVESTABLE`. |
| **Epochs / depositor** | `epochs a–b` sub, `price change`, `no position`, `shares` | `price change` word | `EPOCHS 470-485` / `+1.2%`; table of 5 rows or fewer; depositor `$1,204  1,183.2 SH`. |
| **Index / discover / strat list** | `Latest allocation (12 rebalances)`, `<slug> for weights`, `backtest, full period · /strat <id> for the live book`, `computed …` | All hint lines and parentheticals | `ALLOCATION` label plus rows; counts as focal (`12`). |
| **Backtest** (12) | `EMA Cross backtest` / `−0.9% over 90d`; legend; curve; `sharpe (daily) 0.15 max DD −28.6% CAGR −3.6% vs HLP −3.3%`; `backtest · as of`; `! 49 order(s) clipped by margin (leverage above min(script, maxLeverage))` | `backtest` sub (the echo says it), `(daily)`, CAGR inline (pane), footer, warning sentence | Header `EMA CROSS  BTC` / `−0.9%  90D`; curve; stats `SHARPE 0.15   MAX DD −28.6%   VS HLP −3.3%`; tag `! 49 CLIPPED`. |
| **Paper** | strategy, id, equity, return | id (pane) | `NAME` / `$10,240  +2.4%`; position rows. |
| **Arena** (13) | `Arena` / leader plus return; `# entrant kind return sharpe max DD equity`; `… EMA Cross: waiting for the first closed 1h bar` per entrant; `paper · as of` | `kind` column, empty `equity` column, the waiting sentences, footer | Header `ARENA` / `LONG SPY / SHORT MEMES  +0.04%`; columns `# ENTRANT RETURN SHARPE MAXDD`; a waiting entrant shows `WAITING` (Dim) in its RETURN cell. |
| **Order preview** | full preview card | sentence checks | header `BUY 0.5 BTC` / notional; check rows `■ name` / `■ name` (Down) for blockers, 4 words or fewer each. |
| **Status / doctor** | `All checks pass` / `N of M checks failed`; detail column for every check | Details of passing checks | Focal `OK` or `2 FAILED` (Down); rows `CHECK  ■  12MS`; detail only on failed rows. |
| **Price** | `Mid` plus `+x.xx% 24h` | 2-decimal % | `BTC` / `64,951  −0.9%  24H`. |

#### Panes (ctrl+o, full screen)

| Pane | Now | Cut | Stays |
|---|---|---|---|
| **Full chart** (03) | top bar `hlp mainnet › BTC 15m tags`; 8 hints `esc back · h/l pan · +/- zoom · 1-9 interval · c crosshair · s style · i MAs · L log` | Tags in top bar, 5 of 8 hints | Header band: **big digits** price (or crosshair close), small `−0.9%  24H` on its baseline, `BTC  15M` label right; MA legend; chart; key row `esc back   +/- zoom   1-9 interval   ? keys`. `?` lists the rest. |
| **Vault screener** (06b) | `Vaults 50 of 50`; 10 columns × all rows; 6 hints | `•` marks, `of 50` when unfiltered, 3 hints | `VAULTS  50`; all columns, labels UPPERCASE; selected row `▌` plus Surface; key row `esc back   / filter   s sort   ? keys`. |
| **Tearsheet** (08b) | everything in the card plus `Checklist` with `✓`/`—` rows and detail sentences (`top 10 depositors hold 17.5%`, `accepting deposits`, `this is HLP`), `Sharpe above: daily returns of this curve · screener (hlp.xyz, all time): 7.17`, monthly heatmap | Checklist detail sentences become values (`17.5% TOP10`, `OPEN`, `n/a`), the Sharpe explanation sentence, `✓ —` glyphs | Big digits `+367.9` with `%  ALL`; meta line with the address; curve; `DRAWDOWN`; `METRICS` table (vs benchmarks); `CHECKLIST` (`■ MAX DD  −5.8%`); `MONTHLY %` heatmap (gains tinted Text 18%, losses Down 30%); `NOTES` (full warnings); info row (`i`): source and as-of. |
| **Account / pvault / backtest / arena** | as cards | as cards | Big digits for the focal; every column; `NOTES`; info row. |

---

## 6. Layout

### 6.1 Frame

```
row 0      hlp                       BTC 64,951 −0.9%    ETH 1,825.9 −0.2%    HYPE 67.14 +2.8%    ■
row 1      (blank)
rows 2..   transcript (cards separated by one blank row)
row H-2    ──────────────────────────────────────────────────────────────────────────────────────
row H-1    ❯ █                                                                              ? keys
```

- **Gutter:** 2 cells left and 2 right, for all content. Today it is 1. Card inner width is `W − 4`. The reticle sits in the gutter.
- **Max widths:**
  - transcript cards: 100 columns, left-aligned (never centred); cards led by a chart (chart, vault tearsheet, backtest, compare) take the full transcript width minus the gutters, like the landing's chart wall;
  - tables inside a card: 6 columns or fewer;
  - book and order preview: 72;
  - agent prose: 72;
  - panes: the full width minus the gutters.

  Wide terminals get more empty space on the right, not wider cards.
- **Grid:**
  - Columns align to a shared track per card: the label column, then values right-aligned with 3-cell gaps.
  - Header focal values right-align to the card's right edge, which is the same edge as the last table column.
- **Vertical rhythm:**
  - one blank row between transcript groups (echo plus card);
  - none inside a card except before a section label;
  - one blank row under the top bar and one above the hairline.

  Never pad rows apart to fill a screen: empty space below the content shows the floor.
- **Panes:** the top bar, a header band (big digits row group plus label, then 1 blank row), the body, then one key row. Panels never move between modes. The top bar and the prompt (or key row) are always in the same rows.

### 6.2 One focal point per screen

| Screen | Focal |
|---|---|
| Welcome | the `HLP` wordmark in the reticle, scaled with the window (1x to 4x, `glyph.Scale`) |
| Transcript | the newest card's focal number (bold); the reticle marks that card |
| Pane | the big digits |
| Confirm | the keycap |

Nothing else is bold except card titles.

### 6.3 Widths

| Width | Behaviour |
|---|---|
| under 60 | no ticker, no big digits, 4 columns or fewer in tables |
| 60 to 99 | ticker shows the items that fit |
| 100 and over | ticker items in full; side panel allowed (ctrl+b) at 120 and over |

---

## 7. Motion with a cause

Only two things move.

1. **Split-flap on ticker updates.**
   - **Cause:** a displayed character of a ticker price or change actually changed. Only changed characters flip.
   - **Frames:** each changed cell shows `▀` (Muted on Surface) for one tick, then `▄` for one tick, then the new character. Ticks are 33 ms, and characters stagger by 1 tick left to right. A 7-character change settles in about 300 ms. The landing uses 180 ms per character with a 22 ms stagger.
   - **Throttle:** a symbol flips at most once every 2.6 s (the landing's cadence). Newer values wait and land on the next flip, never queued one by one.
   - **Ticks run only while a flap is in flight.** There is no free-running animation timer.
   - **Paused** when the terminal is unfocused (`m.focused == false`), when a pane is open (the ticker is hidden), and under reduced motion.
2. **Cursor blink.**
   - A hard step with a 1.1 s period: visible for 605 ms, hidden for 495 ms. There is no fade (`landing-blink`, `steps(1,end)`).
   - Any keypress resets it to visible. It stops (solid) while the terminal is unfocused or a job is streaming.
   - Under reduced motion it is solid.

Nothing else animates:

- no spinner (pending cards show elapsed seconds, a state rather than decoration);
- no pulsing live dot;
- no reticle snap;
- no fades;
- no load sequences;
- no scroll easing.

Values that change in tables (book, account, arena) update in place without animation.

**Reduced motion** is `ui.motion = "off"` in config, or `HLP_REDUCED_MOTION=1`, or `NO_MOTION=1`. Default: on. `TERM=dumb` also turns it off. `tools/shot/shoot.sh` sets `HLP_REDUCED_MOTION=1`, so captures are deterministic.

**Cost:** the flap ticks are local redraws only. Ticker data still comes from the existing WebSocket mids feed. No new polling, no new network calls.

---

## 8. Config surface

These are new keys, all optional:

```toml
[ui]
theme = "auto"            # auto = rush
paint_background = true   # OSC 11 to #131315 for rush; restored on exit
motion = "on"             # "off" = no flap, solid cursor
floor = true              # dotted floor in empty states
```

---

## 9. Acceptance budgets

A word is a whitespace-separated token that contains a letter, counted on the ANSI-stripped capture at 140 × 42. Numbers are free, including numbers with a unit suffix (`$151.7M`, `3.5Y`, `11X`, `4D`: a token matching `^[+−$-]?[0-9.,]+[KMBYX%DMO]*$`). The top bar's ticker symbols count on 01 only: on every other screen the ticker is chrome (the landing's MarketFlap), so a row-1 symbol followed by its price is not a word. `tools/shot/words.sh` applies both rules.

| Shot | Before | Budget after |
|---|---|---|
| 01 welcome | 41 | **16** (ticker symbols count) |
| 02 chart inline | 56 | **16** |
| 03 chart full | 34 | **16** |
| 06 vaults | 113 | **40** |
| 06b screener | 196 | **120** (vault names are data) |
| 08 tearsheet | 85 | **30** |
| 08b tearsheet full | 106 | **80** |
| 09 fund | 103 | **20** |
| 10 book | 48 | **14** |
| 11 positions | 123 | **35** |
| 12 backtest | 81 | **24** (the legend names the strategy first, the tag says `ORDERS`) |
| 13 arena | 82 | **30** |
| 14 autocomplete | 110 | **25** |
| 15 help | 118 | **80** (`/macro` and `/models` joined the command list) |
| 16 error | 49 | **12** |
| 16b plain text, no model (the echoed question counts) | 78 | **24** |
| 17/18 agent | 109 / 143 | **60 / 70** |

Counting command (the same one the before numbers came from; `words.sh` adds the two amendments above):

```sh
perl -pe 's/\e\[[0-9;:]*[A-Za-z]//g; s/\e\][^\a]*\a//g' SHOT.ansi | tr -s ' \t' '\n' | grep -c '[A-Za-z]'
```

There are also visual checks, made by looking at the PNGs:

- one focal point;
- no teal, green or purple pixels in chrome (charts' MA lines excepted);
- the accent appears in 3 places or fewer per shot;
- no box borders;
- the reticle on exactly one card.
