#!/usr/bin/env bash
# desktop.sh [OUTDIR] [SCENE...]
#
# The README set: hlp in a full-size terminal window (200x56 by default, a
# maximised terminal on a laptop screen), mostly /dash dashboards. Writes
# docs/screenshots/desktop by default. Name scene ids (01 05 ...) to shoot
# only those. Same rules as
# scenes.sh: a clean config, no keys, reduced motion, read-only public data.
# Scene 10 is the markets dashboard in a see-through terminal over a
# wallpaper (docs/screenshots/wallpaper/field.jpg, generated, not a photo).
#
#   tools/shot/desktop.sh
#   DESK_COLS=240 DESK_ROWS=64 tools/shot/desktop.sh /tmp/x 02
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

ASK="where should I park USDC for steady returns with shallow drawdowns?"

# id | file name | env (VAR=value ...; SHOT_BG/SHOT_FG for themes that paint their own ground; FAKE=1; COLS=n) | keys
SCENES=(
	"01|01-dash-markets|SHOT_WAIT=25 SHOT_MAX=40|/dash markets{Enter}{wait 25}"
	"02|02-dash-vaults|SHOT_WAIT=25 SHOT_MAX=40|/dash vaults{Enter}{wait 25}"
	"03|03-dash-hlp|SHOT_WAIT=30 SHOT_MAX=50|/dash hlp{Enter}{wait 30}"
	"04|04-dash-strategies|SHOT_WAIT=40 SHOT_MAX=90|/dash strategies{Enter}{wait 40}"
	"05|05-dash-research|SHOT_WAIT=25 SHOT_MAX=40|/dash research{Enter}{wait 25}"
	"06|06-dash-markets-eth-hyperliquid|SHOT_BG=#0F1A1F SHOT_FG=#F6FEFD SHOT_WAIT=25 SHOT_MAX=40|/theme hyperliquid{Enter}{wait 1}/dash markets ETH{Enter}{wait 25}"
	"07|07-dash-vaults-paper|SHOT_BG=#c6c4ba SHOT_FG=#191a17 SHOT_WAIT=25 SHOT_MAX=40|/theme paper{Enter}{wait 1}/dash vaults{Enter}{wait 25}"
	"08|08-dash-research-sol-heikin|SHOT_WAIT=25 SHOT_MAX=40|/candles heikin{Enter}{wait 1}/dash research SOL{Enter}{wait 25}"
	"09|09-shell|COLS=150 SHOT_WAIT=6 SHOT_MAX=30|/chart ETH 1h{Enter}{wait 6}{settle}/vault hlp{Enter}{wait 8}{settle}/price btc eth hype sol{Enter}{wait 4}{settle}{C-b}{wait 2}/da"
	"10|10-dash-markets-glass|GLASS=1 SHOT_WAIT=25 SHOT_MAX=40|/dash markets{Enter}{wait 25}"
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
	fake="" bg="" fg="" glass="" cols=$COLS
	for kv in ${envv[@]+"${envv[@]}"}; do
		[[ $kv == COLS=* ]] && cols=${kv#COLS=}
		[[ $kv == FAKE=1 ]] && fake=1
		[[ $kv == SHOT_BG=* ]] && bg=${kv#SHOT_BG=}
		[[ $kv == SHOT_FG=* ]] && fg=${kv#SHOT_FG=}
		[[ $kv == GLASS=1 ]] && glass=1
	done
	extra=(SHOT_FLAGS="$DEFAULT_FLAGS")
	[[ -n $bg ]] && extra=(SHOT_FLAGS="-bg $bg${fg:+ -fg $fg}")
	# GLASS=1: a translucent terminal over a wallpaper (Warp, Ghostty, iTerm
	# with a background image), saved as JPEG (the gradient is 3 MB as PNG).
	[[ -n $glass ]] && extra=(SHOT_FLAGS="-bg #131315 -opacity 0.45 -wallpaper-blur 10 -wallpaper $ROOT/docs/screenshots/wallpaper/field.jpg")
	if [[ -n $fake ]]; then
		start_fake
		extra+=(SHOT_CONFIG="$HERE/fake.toml" SHOT_TITLE="hlp (illustrative: local fake model)")
	fi
	if ! env ${envv[@]+"${envv[@]}"} "${extra[@]}" "$HERE/shoot.sh" "$name" "$cols" "$ROWS" "$keys" 2>&1 | grep -v 'Abort trap' | grep '^shot:' | sed 's/  (.*//'; then
		failed=1
	fi
	[[ -s $OUT/$name.ansi ]] || { echo "desktop: no capture for $id" >&2; failed=1; }
	if [[ -n $glass && -s $OUT/$name.png ]]; then
		sips -s format jpeg -s formatOptions 90 "$OUT/$name.png" --out "$OUT/$name.jpg" >/dev/null && rm "$OUT/$name.png"
	fi
done
exit $failed
