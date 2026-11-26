#!/usr/bin/env bash
# desktop.sh [OUTDIR] [SCENE...]
#
# The desktop set: hlp in a full-size terminal window (200x56 by default, a
# maximised terminal on a laptop screen). Writes docs/screenshots/desktop by
# default. Name scene ids (d02 d05 ...) to shoot only those. Same rules as
# scenes.sh: a clean config, no keys, reduced motion, read-only public data.
# The ask scene talks to a local fake model (tools/shot/fakellm) and is
# captioned illustrative.
#
#   tools/shot/desktop.sh
#   DESK_COLS=240 DESK_ROWS=64 tools/shot/desktop.sh /tmp/x d02
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
OUT=${1:-$ROOT/docs/screenshots/desktop}
[[ $# -gt 0 ]] && shift
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
[[ -x /usr/local/opt/go/bin/go ]] && export PATH=/usr/local/opt/go/bin:$PATH
COLS=${DESK_COLS:-200}
ROWS=${DESK_ROWS:-56}

WORK=${TMPDIR:-/tmp}/hlp-shot/desktop-$$
mkdir -p "$WORK"
FAKE_PID=""
cleanup() {
	[[ -n $FAKE_PID ]] && kill "$FAKE_PID" 2>/dev/null || true
	rm -rf "$WORK"
}
trap cleanup EXIT

(cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/hlp" ./cmd/hlp)
(cd "$ROOT" && go build -o "$WORK/shot" ./tools/shot)
(cd "$ROOT" && go build -o "$WORK/fakellm" ./tools/shot/fakellm)
export HLP_BIN=${HLP_BIN:-$WORK/hlp} SHOT_BIN=$WORK/shot SHOT_OUT=$OUT
# rush paints the terminal ground #131315 with OSC 11, which a tmux capture
# does not carry: render the default background in it.
DEFAULT_FLAGS=${SHOT_FLAGS:--bg #131315}

LEFT40=$(printf '{Left}%.0s' $(seq 40))
OPEN='{wait 6}{settle}{C-o}'
THESIS='/backtest --thesis "long BTC, short DOGE FARTCOIN, rebalance 1w" --days 90'
ASK="where should I park USDC for steady returns with shallow drawdowns?"

# id | file name | env (VAR=value ...; SHOT_BG/SHOT_FG for themes that paint their own ground; FAKE=1) | keys
SCENES=(
	"d01|d01-shell-session|SHOT_WAIT=8 SHOT_MAX=30|/chart BTC 15m{Enter}{wait 6}{settle}/vaults --limit 6{Enter}{wait 6}{settle}/price btc eth hype sol{Enter}"
	"d02|d02-chart-btc-15m|SHOT_MAX=12|/chart BTC 15m{Enter}$OPEN{wait 2}c{wait 1}$LEFT40"
	"d03|d03-shell-watchlist|SHOT_WAIT=6 SHOT_MAX=20|/chart HYPE 1h{Enter}{wait 6}{settle}/vault hlp{Enter}{wait 8}{settle}{C-b}"
	"d04|d04-vault-screener|SHOT_WAIT=3|/vaults{Enter}$OPEN"
	"d05|d05-vault-hlp-tearsheet|SHOT_WAIT=4|/vault hlp{Enter}{wait 8}{settle}{C-o}"
	"d06|d06-hlp-positioning|SHOT_WAIT=15 SHOT_MAX=30|/hlp{Enter}{wait 20}{settle}{C-o}"
	"d07|d07-book-and-trades|SHOT_WAIT=20 SHOT_MAX=40|/book ETH{Enter}{wait 15}/trades ETH{Enter}"
	"d08|d08-markets|SHOT_WAIT=4|/markets --sort volume{Enter}$OPEN"
	"d09|d09-funding|SHOT_WAIT=4|/funding{Enter}$OPEN"
	"d10|d10-vault-compare|SHOT_WAIT=6 SHOT_MAX=30|/compare hlp growi 22cap{Enter}{wait 12}{settle}{C-o}"
	"d11|d11-discover|SHOT_WAIT=4|/discover{Enter}$OPEN"
	"d12|d12-index|SHOT_WAIT=6 SHOT_MAX=30|/index mavericks{Enter}{wait 12}{settle}{C-o}"
	"d13|d13-quant-strategy|SHOT_WAIT=6 SHOT_MAX=30|/strat trend{Enter}{wait 10}{settle}{C-o}"
	"d14|d14-strategy-vault|SHOT_WAIT=6 SHOT_MAX=30|/fund{Enter}{wait 12}{settle}{C-o}"
	"d15|d15-macro|SHOT_WAIT=6 SHOT_MAX=30|/macro{Enter}{wait 12}{settle}{C-o}"
	"d16|d16-order-preview|SHOT_WAIT=6|/preview buy BTC --usd 100{Enter}"
	"d17|d17-thesis-backtest|SHOT_WAIT=6 SHOT_MAX=60|$THESIS{Enter}{wait 15}{settle}{C-o}"
	"d18|d18-pine-backtest|SHOT_WAIT=6 SHOT_MAX=60|/backtest examples/pine/ema-cross.pine --coin BTC --days 90{Enter}{wait 12}{settle}{C-o}"
	"d19|d19-vault-arena|SHOT_WAIT=6 SHOT_MAX=40|/arena --vaults hlp,growi,22cap,citadel{Enter}{wait 12}{settle}{C-o}"
	"d21|d21-help|SHOT_WAIT=2|/help{Enter}"
	"d22|d22-autocomplete|SHOT_WAIT=2|/va"
	"d23|d23-palette|SHOT_WAIT=2|{C-k}"
	"d24|d24-theme-hyperliquid|SHOT_BG=#0F1A1F SHOT_FG=#F6FEFD SHOT_MAX=12|/theme hyperliquid{Enter}{wait 1}/chart HYPE 1h{Enter}$OPEN"
	"d25|d25-theme-paper|SHOT_BG=#c6c4ba SHOT_FG=#191a17 SHOT_MAX=12|/theme paper{Enter}{wait 1}/chart ETH 4h{Enter}$OPEN"
	"d26|d26-heikin-ashi|SHOT_MAX=12|/candles heikin{Enter}{wait 1}/chart SOL 1h{Enter}$OPEN"
	"d29|d29-ask-illustrative|FAKE=1 SHOT_WAIT=40 SHOT_MAX=90|$ASK{Enter}"
)

PICK=("$@")
want() {
	[[ ${#PICK[@]} -eq 0 ]] && return 0
	local p
	for p in "${PICK[@]}"; do [[ $p == "$1" ]] && return 0; done
	return 1
}

start_fake() {
	[[ -n $FAKE_PID ]] && return 0
	"$WORK/fakellm" 18765 "$WORK/fakellm.log" &
	FAKE_PID=$!
	for _ in $(seq 50); do
		curl -fs http://127.0.0.1:18765/v1/models >/dev/null 2>&1 && return 0
		sleep 0.1
	done
	echo "desktop: fake model did not start" >&2
	return 1
}

failed=0
for row in "${SCENES[@]}"; do
	IFS='|' read -r id name envs keys <<<"$row"
	want "$id" || continue
	echo "== $id $name"
	envv=()
	# shellcheck disable=SC2206
	[[ -n $envs ]] && envv=($envs)
	fake="" bg="" fg=""
	for kv in ${envv[@]+"${envv[@]}"}; do
		[[ $kv == FAKE=1 ]] && fake=1
		[[ $kv == SHOT_BG=* ]] && bg=${kv#SHOT_BG=}
		[[ $kv == SHOT_FG=* ]] && fg=${kv#SHOT_FG=}
	done
	extra=(SHOT_FLAGS="$DEFAULT_FLAGS")
	[[ -n $bg ]] && extra=(SHOT_FLAGS="-bg $bg${fg:+ -fg $fg}")
	if [[ -n $fake ]]; then
		start_fake
		extra+=(SHOT_CONFIG="$HERE/fake.toml" SHOT_TITLE="hlp (illustrative: local fake model)")
	fi
	if ! env ${envv[@]+"${envv[@]}"} "${extra[@]}" "$HERE/shoot.sh" "$name" "$COLS" "$ROWS" "$keys" 2>&1 | grep -v 'Abort trap' | grep '^shot:' | sed 's/  (.*//'; then
		failed=1
	fi
	[[ -s $OUT/$name.ansi ]] || { echo "desktop: no capture for $id" >&2; failed=1; }
done
exit $failed
