#!/usr/bin/env bash
# agent-film.sh [OUT.gif]
#
# The README agent clip: a real Claude Code session with hlp as its only MCP
# server, asked one vault question. The question is typed at normal speed, the
# tool calls are time-compressed (marked in the title bar), and the answer is
# held. Needs `claude` logged in; runs it with no user settings, no other MCP
# servers and only the hlp tools allowed, in a scratch directory (/tmp/hlp).
#
#   tools/shot/agent-film.sh
#   AGENT_Q="Is HLP long or short ETH right now?" tools/shot/agent-film.sh /tmp/x.gif
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
OUT=${1:-$ROOT/docs/screenshots/agent.gif}
Q=${AGENT_Q:-"Which Hyperliquid vaults over \$1M beat HLP risk-adjusted, with drawdown under 25%? Just a small table and one line."}
COLS=${AGENT_COLS:-86}
ROWS=${AGENT_ROWS:-46}
[[ -x /usr/local/opt/go/bin/go ]] && export PATH=/usr/local/opt/go/bin:$PATH

# The banner prints the working directory: keep it short and anonymous.
DEMO=/tmp/hlp
WORK=$(mktemp -d "${TMPDIR:-/tmp}/hlp-agent-film.XXXX")
mkdir -p "$DEMO" "$WORK/rec" "$WORK/fr"
SOCK=$WORK/tmux.sock
cleanup() {
	[[ -n ${LOOP:-} ]] && kill "$LOOP" 2>/dev/null || true
	tm kill-server 2>/dev/null || true
	rm -rf "$WORK"
}
trap cleanup EXIT

# tmux from Homebrew may link a libutf8proc major version that is gone.
SHIM=$WORK/dyld
mkdir -p "$SHIM"
for lib in /usr/local/lib/libutf8proc.*.dylib /opt/homebrew/lib/libutf8proc.*.dylib; do
	[[ -e $lib ]] && ln -sf "$lib" "$SHIM/libutf8proc.2.dylib" && ln -sf "$lib" "$SHIM/libutf8proc.3.dylib" && break
done
tm() { DYLD_LIBRARY_PATH=$SHIM tmux -S "$SOCK" "$@"; }

(cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/hlp" ./cmd/hlp && go build -o "$WORK/shot" ./tools/shot)
printf '{"mcpServers":{"hlp":{"command":"%s","args":["mcp"]}}}\n' "$WORK/hlp" >"$WORK/mcp.json"
cat >"$WORK/cc" <<EOF
#!/bin/bash
cd $DEMO
exec env -i HOME="\$HOME" USER="\$USER" PATH="$PATH" TERM=xterm-256color LANG=en_US.UTF-8 COLORTERM=truecolor \\
	claude --setting-sources '' --strict-mcp-config --mcp-config $WORK/mcp.json \\
	--allowedTools mcp__hlp --permission-mode default --disable-slash-commands
EOF
chmod +x "$WORK/cc"

tm new-session -d -s cc -x "$COLS" -y "$ROWS" "$WORK/cc"
# First run in a directory asks whether to trust it.
for _ in $(seq 60); do
	s=$(tm capture-pane -p -t cc)
	if grep -q 'trust this folder' <<<"$s"; then tm send-keys -t cc Down Enter; fi
	grep -q '^❯' <<<"$s" && grep -q 'Claude Code v' <<<"$s" && break
	sleep 0.5
done
sleep 1.5

# Record the pane every 0.1 s.
(
	i=0
	while :; do
		f=$(printf '%s/rec/%05d.ansi' "$WORK" "$i")
		tm capture-pane -e -p -S -1000 -t cc >"$f" 2>/dev/null || { rm -f "$f"; sleep 0.1; continue; }
		echo "$i $(python3 -c 'import time;print(time.time())')" >>"$WORK/rec/times.txt"
		i=$((i + 1))
		sleep 0.1
	done
) &
LOOP=$!
now() { python3 -c 'import time;print(time.time())'; }

sleep 1.2
echo "type $(now)" >"$WORK/rec/marks.txt"
for ((k = 0; k < ${#Q}; k += 3)); do
	tm send-keys -t cc -l "${Q:k:3}"
done
sleep 0.6
echo "enter $(now)" >>"$WORK/rec/marks.txt"
tm send-keys -t cc Enter
# Claude Code can swallow the first Enter after fast typing: press it again
# if the turn has not started.
sleep 2
tm capture-pane -p -t cc | grep -q 'esc to interrupt' || tm send-keys -t cc Enter
# Wait for the turn to finish: the spinner line ("esc to interrupt") gone.
sleep 3
for _ in $(seq 240); do
	tm capture-pane -p -t cc | grep -q 'esc to interrupt' || break
	sleep 0.5
done
echo "done $(now)" >>"$WORK/rec/marks.txt"
sleep 1.5
kill "$LOOP" 2>/dev/null || true
wait "$LOOP" 2>/dev/null || true
LOOP=""

python3 - "$WORK" "$COLS" "$ROWS" <<'PY'
import os, re, subprocess, sys
w, cols, rows = sys.argv[1], sys.argv[2], sys.argv[3]
esc = re.compile(r"\x1b\[[0-9;:?]*[A-Za-z]")
plain = lambda s: esc.sub("", s)

def view(n):
    """The screen as a terminal would show it: scrollback plus pane, its last
    ROWS lines once the transcript is taller than the window."""
    ls = open(ansi(n), errors="ignore").read().split("\n")
    while ls and not plain(ls[-1]).strip():
        ls.pop()
    return "\n".join(ls[-int(rows):]) + "\n"

rec = os.path.join(w, "rec")
t = {}
for l in open(os.path.join(rec, "times.txt")):
    a, b = l.split()
    if os.path.getsize(os.path.join(rec, f"{int(a):05d}.ansi")):
        t[int(a)] = float(b)
marks = dict((a, float(b)) for a, b in (l.split() for l in open(os.path.join(rec, "marks.txt"))))
ansi = lambda n: os.path.join(rec, f"{n:05d}.ansi")
frames = sorted(t)
# The work ends with the last frame that still shows the spinner.
answer = marks["done"]
for n in reversed(frames):
    if t[n] < marks["enter"]:
        break
    if "esc to interrupt" in plain(view(n)):
        answer = t[n]
        break
work = max(answer - marks["enter"] - 0.5, 0.1)
speed = max(round(work / 3.0), 1)  # the compressed part plays in about 3 s

# Typing plays in about 3 s, the tool calls in about 3 s (the title says how
# much faster), the rest in real time.
ty, e = marks["type"], marks["enter"] + 0.5
k_type = max((e - ty) / 3.0, 1.0)

def out_time(x):
    if x <= ty:
        return x
    if x <= e:
        return ty + (x - ty) / k_type
    if x <= answer:
        return ty + (e - ty) / k_type + (x - e) / speed
    return ty + (e - ty) / k_type + (answer - e) / speed + (x - answer)

kept, views = [], {}
for n in frames:
    if t[n] < marks["type"] - 0.8:
        continue
    v = view(n)
    if kept and v == views[kept[-1]]:
        continue
    views[n] = v
    kept.append(n)
lines = []
for i, n in enumerate(kept):
    start = out_time(t[n])
    stop = out_time(t[kept[i + 1]]) if i + 1 < len(kept) else start + 7.0
    if stop - start < 0.02:
        continue
    fast = e < t[n] < answer
    title = f"claude code + hlp   ▸▸ {speed}x" if fast else "claude code + hlp"
    png = os.path.join(w, "fr", f"{i:05d}.png")
    src = os.path.join(w, "fr", f"{i:05d}.ansi")
    open(src, "w").write(views[n])
    subprocess.run([os.path.join(w, "shot"), "-cols", cols, "-rows", rows, "-title", title, "-bg", "#131315",
                    "-scale", "1", "-o", png, src], check=True, capture_output=True)
    lines.append(f"file '{png}'\nduration {stop - start:.3f}")
lines.append(lines[-1].split("\n")[0])
open(os.path.join(w, "concat.txt"), "w").write("\n".join(lines) + "\n")
print(f"agent-film: {len(lines) - 1} frames, tools {work:.1f}s at {speed}x", file=sys.stderr)
PY

ffmpeg -v error -y -f concat -safe 0 -i "$WORK/concat.txt" -vf "palettegen=max_colors=128:stats_mode=full" -frames:v 1 "$WORK/pal.png"
ffmpeg -v error -y -f concat -safe 0 -i "$WORK/concat.txt" -i "$WORK/pal.png" \
	-lavfi "[0:v][1:v]paletteuse=dither=none:diff_mode=rectangle" -fps_mode vfr -loop 0 "$OUT"
echo "agent-film: $OUT"
