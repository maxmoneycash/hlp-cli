#!/usr/bin/env bash
# desktop.sh [OUTDIR] [SCENE...]
#
# The desktop set: hlp in a full-size terminal window (200x56 by default, a
# maximised terminal on a laptop screen), with each view opened full screen.
# Writes docs/screenshots/desktop by default. Name scene ids (d02 d05 ...) to
# shoot only those. Same rules as scenes.sh: a clean config, no keys, reduced
# motion, read-only public data.
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
trap 'rm -rf "$WORK"' EXIT

(cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/hlp" ./cmd/hlp)
(cd "$ROOT" && go build -o "$WORK/shot" ./tools/shot)
export HLP_BIN=${HLP_BIN:-$WORK/hlp} SHOT_BIN=$WORK/shot SHOT_OUT=$OUT
# rush paints the terminal ground #131315 with OSC 11, which a tmux capture
# does not carry: render the default background in it.
export SHOT_FLAGS=${SHOT_FLAGS:--bg #131315}

LEFT40=$(printf '{Left}%.0s' $(seq 40))
OPEN='{wait 6}{settle}{C-o}'

# id | file name | env (VAR=value ...) | keys
SCENES=(
	"d01|d01-shell-session|SHOT_WAIT=8 SHOT_MAX=30|/chart BTC 15m{Enter}{wait 6}{settle}/vaults --limit 6{Enter}{wait 6}{settle}/price btc eth hype sol{Enter}"
	"d02|d02-chart-btc-15m|SHOT_MAX=12|/chart BTC 15m{Enter}$OPEN{wait 2}c{wait 1}$LEFT40"
	"d03|d03-shell-watchlist|SHOT_WAIT=6 SHOT_MAX=20|/chart HYPE 1h{Enter}{wait 6}{settle}/vault hlp{Enter}{wait 8}{settle}{C-b}"
	"d04|d04-vault-screener|SHOT_WAIT=3|/vaults{Enter}$OPEN"
	"d05|d05-vault-hlp-tearsheet|SHOT_WAIT=4|/vault hlp{Enter}{wait 8}{settle}{C-o}"
	"d06|d06-hlp-positioning|SHOT_WAIT=15 SHOT_MAX=30|/hlp{Enter}{wait 20}{settle}{C-o}"
)

PICK=("$@")
want() {
	[[ ${#PICK[@]} -eq 0 ]] && return 0
	local p
	for p in "${PICK[@]}"; do [[ $p == "$1" ]] && return 0; done
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
	if ! env ${envv[@]+"${envv[@]}"} "$HERE/shoot.sh" "$name" "$COLS" "$ROWS" "$keys" 2>&1 | grep -v 'Abort trap' | tail -2; then
		failed=1
	fi
	[[ -s $OUT/$name.ansi ]] || { echo "desktop: no capture for $id" >&2; failed=1; }
done
exit $failed
