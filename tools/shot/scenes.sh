#!/usr/bin/env bash
# scenes.sh [OUTDIR] [SCENE...]
#
# Shoots the compact set at 140x42 (scenes 01 to 18 with
# 03b, 06b, 08b, 13b and 16b) with the same names and keystrokes, into OUTDIR
# (default tools/shot/out/scenes). Name scene ids (02 06b ...) to shoot only
# those. Each scene is one tools/shot/shoot.sh run: a clean config, no keys,
# reduced motion, read-only public data.
#
# Scene 18 talks to a local fake model (tools/shot/fakellm, started here on
# 127.0.0.1:18765 and stopped at exit) and is captioned illustrative. 16b and
# 17 run with no model: 16b shows the real no-model path for plain text.
#
#   tools/shot/scenes.sh                      # the whole set
#   tools/shot/scenes.sh /tmp/x 02 08 08b     # three scenes
#   tools/shot/words.sh tools/shot/out/scenes
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
OUT=${1:-$ROOT/tools/shot/out/scenes}
[[ $# -gt 0 ]] && shift
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
[[ -x /usr/local/opt/go/bin/go ]] && export PATH=/usr/local/opt/go/bin:$PATH

WORK=${TMPDIR:-/tmp}/hlp-shot/scenes-$$
mkdir -p "$WORK"
FAKE_PID=""
cleanup() {
	[[ -n $FAKE_PID ]] && kill "$FAKE_PID" 2>/dev/null || true
	rm -rf "$WORK"
}
trap cleanup EXIT

# Build once for the whole set.
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/hlp" ./cmd/hlp)
(cd "$ROOT" && go build -o "$WORK/shot" ./tools/shot)
(cd "$ROOT" && go build -o "$WORK/fakellm" ./tools/shot/fakellm)
export HLP_BIN=${HLP_BIN:-$WORK/hlp} SHOT_BIN=$WORK/shot SHOT_OUT=$OUT
# rush paints the terminal ground #131315 with OSC 11 (DESIGN 2), which a
# tmux capture does not carry: render the default background in it.
export SHOT_FLAGS=${SHOT_FLAGS:--bg #131315}

WHALE=0xb83de012dba672c76a7dbbbf3e459cb59d7d6e36
LEFT30=$(printf '{Left}%.0s' $(seq 30))
H12=$(printf 'h{wait 0.2}%.0s' $(seq 12))
ASK16B="which vaults have the best risk-adjusted returns right now?"
ASK18="where should I park USDC for steady returns with shallow drawdowns?"

# id | file name | env (VAR=value ...) | keys
SCENES=(
	"01|01-welcome||"
	"02|02-chart-btc-15m-inline||/chart BTC 15m{Enter}"
	"03|03-chart-btc-15m-fullscreen-crosshair|SHOT_MAX=12|/chart BTC 15m{Enter}{wait 6}{Enter}{wait 2}c{wait 1}$LEFT30"
	"03b|03b-chart-fullscreen-panned-past-history|SHOT_MAX=12|/chart BTC 15m{Enter}{wait 6}{Enter}{wait 2}$H12"
	"04|04-chart-hype-1h-hollow||/candles hollow{Enter}{wait 1}/chart HYPE 1h{Enter}"
	"05|05-chart-hype-1h-theme-paper|SHOT_BG=#c6c4ba SHOT_FG=#191a17|/theme paper{Enter}{wait 1}/chart HYPE 1h{Enter}"
	"06|06-vaults||/vaults{Enter}"
	"06b|06b-vaults-screener-fullscreen|SHOT_WAIT=3|/vaults{Enter}{wait 6}{settle}{C-o}"
	"07|07-vaults-tag-memes||/vaults --tag memes{Enter}"
	"08|08-vault-hlp-tearsheet|SHOT_WAIT=8|/vault hlp{Enter}"
	"08b|08b-vault-hlp-tearsheet-fullscreen|SHOT_WAIT=4|/vault hlp{Enter}{wait 8}{settle}{C-o}"
	"09|09-fund-strategy-vault|SHOT_WAIT=8|/fund{Enter}"
	"10|10-book-eth-live|SHOT_MAX=10|/book ETH{Enter}"
	"11|11-positions-whale|SHOT_WAIT=6 SHOT_MAX=12|/positions $WHALE{Enter}"
	"12|12-backtest-pine-ema-cross|SHOT_WAIT=10 SHOT_MAX=60|/backtest examples/pine/ema-cross.pine --coin BTC --days 90{Enter}"
	"13|13-arena|SHOT_WAIT=12 SHOT_MAX=60|/arena --entrants examples/pine/supertrend.pine@HYPE,examples/pine/ema-cross.pine@BTC,examples/thesis/long-spy-short-memes.yaml{Enter}"
	"13b|13b-arena-vaults-head-to-head|SHOT_WAIT=10 SHOT_MAX=40|/arena --vaults hlp,growi,22cap{Enter}"
	"14|14-autocomplete-open|SHOT_WAIT=2|/va"
	"15|15-help-keybindings|SHOT_WAIT=2|/help{Enter}"
	"16|16-error-unknown-coin||/chart FOOBARX 1h{Enter}"
	"16b|16b-freetext-no-model|SHOT_WAIT=4|$ASK16B{Enter}"
	"17|17-ask-top-vaults-by-sharpe|SHOT_WAIT=6|top vaults by sharpe{Enter}"
	"18|18-agent-chat-illustrative|FAKE=1 SHOT_WAIT=10 SHOT_MAX=40|$ASK18{Enter}"
)

want() {
	[[ ${#PICK[@]} -eq 0 ]] && return 0
	local p
	for p in "${PICK[@]}"; do [[ $p == "$1" ]] && return 0; done
	return 1
}
PICK=("$@")

start_fake() {
	[[ -n $FAKE_PID ]] && return 0
	"$WORK/fakellm" 18765 "$WORK/fakellm.log" &
	FAKE_PID=$!
	for _ in $(seq 50); do
		curl -fs http://127.0.0.1:18765/v1/models >/dev/null 2>&1 && return 0
		sleep 0.1
	done
	echo "scenes: fake model did not start" >&2
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
		# a theme that paints its own ground and ink (OSC 11 and 10, which
		# a tmux capture drops) renders on them: paper is #c6c4ba / #191a17,
		# and the frame's title takes the theme's ink too
		[[ $kv == SHOT_BG=* ]] && bg=${kv#SHOT_BG=}
		[[ $kv == SHOT_FG=* ]] && fg=${kv#SHOT_FG=}
	done
	extra=()
	[[ -n $bg ]] && extra+=(SHOT_FLAGS="-bg $bg${fg:+ -fg $fg}")
	if [[ -n $fake ]]; then
		start_fake
		extra+=(SHOT_CONFIG="$HERE/fake.toml" SHOT_TITLE="hlp (illustrative: local fake model)")
	fi
	if ! env ${envv[@]+"${envv[@]}"} ${extra[@]+"${extra[@]}"} "$HERE/shoot.sh" "$name" 140 42 "$keys" 2>&1 | grep -v 'Abort trap' | tail -2; then
		failed=1
	fi
	[[ -s $OUT/$name.ansi ]] || { echo "scenes: no capture for $id" >&2; failed=1; }
done

# Captions: which scenes are illustrative.
{
	echo "# Screenshots"
	echo
	echo "Made by \`tools/shot/scenes.sh\` at 140x42 on $(date -u +%Y-%m-%d) from a clean config with no keys and reduced motion. Live public mainnet data, read-only."
	echo
	echo "**Illustrative:** 18 answers from a local fake model (\`tools/shot/fakellm\`), not a real one. The data in its card is real. 16b is the real no-model path."
	echo
	echo "Word counts against the DESIGN §9 budgets: \`tools/shot/words.sh $(basename "$OUT")\`."
} >"$OUT/INDEX.md"
exit $failed
