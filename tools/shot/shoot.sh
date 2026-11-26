#!/usr/bin/env bash
# shoot.sh SCENE COLS ROWS "keys..."
#
# Starts hlp in a fresh detached tmux session of COLS x ROWS, types the keys,
# waits for live data to land and the screen to settle, captures the pane
# with its colors, renders it to a framed PNG and kills the session.
#
# Keys are typed literally, except for {...} tokens:
#   {Enter} {Tab} {BTab} {Esc} {Space} {BSpace} {Up} {Down} {Left} {Right}
#   {PageUp} {PageDown} {Home} {End} {C-c} {C-l} ... (any tmux key name)
#   {wait N}    sleep N seconds (decimals ok)
#   {settle}    wait until the screen stops changing (max SHOT_MAX seconds)
# A final settle always runs before the capture.
#
#   tools/shot/shoot.sh chart-btc 120 36 "/chart BTC 15m{Enter}"
#
# Output: $SHOT_OUT/SCENE.png and SCENE.ansi (default tools/shot/out).
#
# Environment:
#   HLP_BIN      hlp binary to run (default: build ./cmd/hlp, else bin/hlp)
#   HLP_ARGS     arguments for hlp (default none: the interactive shell)
#   SHOT_OUT     output directory
#   SHOT_TITLE   window title (default "hlp")
#   SHOT_FLAGS   extra flags for the renderer (e.g. "-backdrop light -size 13")
#   SHOT_ENV     extra VAR=value pairs for hlp (e.g. OPENAI_BASE_URL=...)
#   SHOT_WAIT    minimum seconds to wait after the keys (default 4)
#   SHOT_MAX     maximum seconds a settle waits (default 20)
#   SHOT_KEEP_CONFIG=1  use your real hlp config/data instead of a clean one
#   SHOT_CONFIG  a config.toml to start from (copied into the clean config dir)
#   SHOT_MOTION=1  leave motion on (default: HLP_REDUCED_MOTION=1, so the
#                flap ticker and cursor blink hold still and captures repeat)
#   SHOT_BIN     prebuilt renderer (default: build ./tools/shot)
#   SHOT_FILM    a directory: also capture the pane every SHOT_FILM_DT seconds
#                (default 0.2) from the first key on, as NNNNN.ansi with the
#                capture time in times.txt, and every key token in keys.txt
#                (tools/shot/film.sh turns them into a GIF)
#
# hlp runs with a minimal environment (env -i): no API keys reach it, and by
# default it gets an empty config and data dir, so shots show a first-run
# state and never your account. The candle/meta cache is shared between
# shots to keep public requests few.
set -euo pipefail

if [[ $# -lt 4 ]]; then
	sed -n '2,37p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
fi
SCENE=$1 COLS=$2 ROWS=$3 KEYS=$4

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
OUT=${SHOT_OUT:-$HERE/out}
WORK=${TMPDIR:-/tmp}/hlp-shot
mkdir -p "$OUT" "$WORK/cache"
[[ -x /usr/local/opt/go/bin/go ]] && export PATH=/usr/local/opt/go/bin:$PATH

# tmux from Homebrew may link a libutf8proc major version that is no longer
# installed; shim it with whatever version is.
if ! (tmux -V) >/dev/null 2>&1; then
	SHIM=$WORK/dyld
	mkdir -p "$SHIM"
	for lib in /usr/local/lib/libutf8proc.*.dylib /opt/homebrew/lib/libutf8proc.*.dylib; do
		[[ -e $lib ]] && ln -sf "$lib" "$SHIM/libutf8proc.2.dylib" && ln -sf "$lib" "$SHIM/libutf8proc.3.dylib" && break
	done
	export DYLD_LIBRARY_PATH=$SHIM${DYLD_LIBRARY_PATH:+:$DYLD_LIBRARY_PATH}
	(tmux -V) >/dev/null 2>&1 || { echo "shoot: tmux will not start" >&2; exit 1; }
fi

# Renderer and app binaries.
SHOT=${SHOT_BIN:-}
if [[ -z $SHOT ]]; then
	SHOT=$WORK/shot-$$
	(cd "$ROOT" && go build -o "$SHOT" ./tools/shot)
fi
if [[ -z ${HLP_BIN:-} ]]; then
	HLP_BIN=$WORK/hlp-$$
	if ! (cd "$ROOT" && CGO_ENABLED=0 go build -o "$HLP_BIN" ./cmd/hlp) 2>"$WORK/build.log"; then
		echo "shoot: hlp build failed (see $WORK/build.log); using bin/hlp" >&2
		HLP_BIN=$ROOT/bin/hlp
	fi
fi

SOCK=hlpshot-$$
SESS=shot
T=(tmux -L "$SOCK" -f "$WORK/tmux-$$.conf")
cat >"$WORK/tmux-$$.conf" <<'EOF'
set -g status off
set -g default-terminal "xterm-256color"
set -g escape-time 10
set -g history-limit 0
set -ga terminal-overrides ",*:RGB"
EOF
cleanup() {
	"${T[@]}" kill-server >/dev/null 2>&1 || true
	rm -f "$WORK/tmux-$$.conf"
	[[ -z ${SHOT_BIN:-} ]] && rm -f "$WORK/shot-$$"
	[[ ${HLP_BIN:-} == "$WORK/hlp-$$" ]] && rm -f "$HLP_BIN"
	return 0
}
trap cleanup EXIT

STATE=$WORK/state-$$
rm -rf "$STATE"
mkdir -p "$STATE/config" "$STATE/data"
ENVV=(HOME="$HOME" PATH="/usr/bin:/bin:/usr/sbin:/sbin" TERM=xterm-256color COLORTERM=truecolor
	LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8 TZ="${TZ:-UTC}" XDG_CACHE_HOME="$WORK/cache")
if [[ -z ${SHOT_KEEP_CONFIG:-} ]]; then
	ENVV+=(XDG_CONFIG_HOME="$STATE/config" XDG_DATA_HOME="$STATE/data")
	if [[ -n ${SHOT_CONFIG:-} ]]; then
		mkdir -p "$STATE/config/hlp"
		cp "$SHOT_CONFIG" "$STATE/config/hlp/config.toml"
		chmod 600 "$STATE/config/hlp/config.toml"
	fi
fi
# Deterministic captures: no flap ticks, a solid cursor (DESIGN §7).
[[ ${SHOT_MOTION:-} == 1 ]] || ENVV+=(HLP_REDUCED_MOTION=1)
# shellcheck disable=SC2206
[[ -n ${SHOT_ENV:-} ]] && ENVV+=($SHOT_ENV)
CMD=$(printf '%q ' env -i "${ENVV[@]}" "$HLP_BIN" ${HLP_ARGS:-})

"${T[@]}" new-session -d -s "$SESS" -x "$COLS" -y "$ROWS" "$CMD"
"${T[@]}" set-option -t "$SESS" -g window-size manual >/dev/null 2>&1 || true
"${T[@]}" resize-window -t "$SESS" -x "$COLS" -y "$ROWS" >/dev/null 2>&1 || true

cap() { "${T[@]}" capture-pane -p -e -N -t "$SESS" 2>/dev/null || "${T[@]}" capture-pane -p -e -t "$SESS"; }

# settle MIN: wait at least MIN seconds, then until three captures 0.5s
# apart match (and the screen is not blank), up to SHOT_MAX seconds.
settle() {
	local min=${1:-0} maxs=${SHOT_MAX:-20} prev="" same=0 start=$SECONDS cur
	sleep "$min"
	while ((SECONDS - start < maxs)); do
		cur=$(cap | cksum)
		if [[ $cur == "$prev" ]] && [[ -n $(cap | tr -d ' \n') ]]; then
			same=$((same + 1))
			((same >= 3)) && return 0
		else
			same=0
		fi
		prev=$cur
		sleep 0.5
	done
	echo "shoot: screen still changing after ${maxs}s, capturing anyway" >&2
}

settle 1
FILM_PID=""
if [[ -n ${SHOT_FILM:-} ]]; then
	mkdir -p "$SHOT_FILM"
	: >"$SHOT_FILM/times.txt"
	: >"$SHOT_FILM/keys.txt"
	(
		n=0
		while :; do
			cap >"$SHOT_FILM/$(printf %05d $n).ansi"
			echo "$n $(perl -MTime::HiRes=time -e 'printf "%.3f", time')" >>"$SHOT_FILM/times.txt"
			n=$((n + 1))
			sleep "${SHOT_FILM_DT:-0.2}"
		done
	) &
	FILM_PID=$!
fi
now() { perl -MTime::HiRes=time -e 'printf "%.3f", time'; }
rest=$KEYS
while [[ -n $rest ]]; do
	if [[ $rest == \{* && $rest == *\}* ]]; then
		tok=${rest%%\}*}
		tok=${tok#\{}
		rest=${rest#*\}}
		[[ -n $FILM_PID ]] && echo "$(now) $tok" >>"$SHOT_FILM/keys.txt"
		case $tok in
		"wait "*) sleep "${tok#wait }" ;;
		settle) settle 0 ;;
		*) "${T[@]}" send-keys -t "$SESS" "$tok" ;;
		esac
		sleep 0.15
	else
		txt=${rest%%\{*}
		rest=${rest:${#txt}}
		"${T[@]}" send-keys -t "$SESS" -l "$txt"
		sleep 0.15
	fi
done
settle "${SHOT_WAIT:-4}"
{ [[ -n $FILM_PID ]] && kill "$FILM_PID" 2>/dev/null && wait "$FILM_PID" 2>/dev/null; } || true

cap >"$OUT/$SCENE.ansi"
# shellcheck disable=SC2086
"$SHOT" -cols "$COLS" -rows "$ROWS" -title "${SHOT_TITLE:-hlp}" ${SHOT_FLAGS:-} -o "$OUT/$SCENE.png" "$OUT/$SCENE.ansi"
rm -rf "$STATE"
echo "$OUT/$SCENE.png"
