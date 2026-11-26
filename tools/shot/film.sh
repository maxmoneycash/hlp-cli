#!/usr/bin/env bash
# film.sh [OUT.gif]
#
# The README hero: a real recording of hlp in a 160x46 terminal, opening
# /dash markets and pressing tab through every dashboard. shoot.sh records
# the pane (SHOT_FILM) with live motion on; the last few seconds before
# each tab, once the tiles have loaded, are kept and joined with hard cuts
# (the tab is the cut), identical frames merged, and encoded as one GIF
# with a shared palette. Loading time between dashboards is cut out.
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
OUT=${1:-$ROOT/docs/screenshots/hero.gif}
# FILM_FLAGS: renderer flags for every frame (the site uses -no-frame: its
# own plate frames the terminal)
FILM_FLAGS=${FILM_FLAGS:--title hlp}
[[ -x /usr/local/opt/go/bin/go ]] && export PATH=/usr/local/opt/go/bin:$PATH
WORK=${TMPDIR:-/tmp}/hlp-film-$$
mkdir -p "$WORK"
trap 'rm -rf "$WORK"' EXIT

(cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/hlp" ./cmd/hlp && go build -o "$WORK/shot" ./tools/shot)
HLP_BIN=$WORK/hlp SHOT_BIN=$WORK/shot SHOT_OUT=$WORK/out SHOT_FILM=$WORK/rec SHOT_FILM_DT=0.08 \
	SHOT_MOTION=1 SHOT_WAIT=1 SHOT_MAX=3 SHOT_FLAGS="-bg #131315" \
	"$HERE/shoot.sh" hero 160 46 "/dash markets{Enter}{wait 25}{Tab}{wait 20}{Tab}{wait 25}{Tab}{wait 50}{Tab}{wait 25}" >/dev/null

# seconds kept before each tab: markets, vaults, hlp, strategies, research
FILM_FLAGS=$FILM_FLAGS python3 - "$WORK" <<'PY'
import filecmp, os, subprocess, sys
w = sys.argv[1]; rec = os.path.join(w, "rec")
t = {int(a): float(b) for a, b in (l.split() for l in open(os.path.join(rec, "times.txt")))}
tabs = [float(l.split()[0]) for l in open(os.path.join(rec, "keys.txt")) if l.split()[1] == "Tab"]
ends = tabs + [max(t.values())]
holds = [4.0, 2.6, 2.8, 3.2, 3.6]
ansi = lambda n: os.path.join(rec, f"{n:05d}.ansi")
os.makedirs(os.path.join(w, "fr"), exist_ok=True)
lines, k = [], 0
for end, hold in zip(ends, holds):
    seg = [n for n in sorted(t) if end - hold - 0.15 <= t[n] < end - 0.15]
    starts = []
    for n in seg:
        if not starts or not filecmp.cmp(ansi(starts[-1]), ansi(n), shallow=False):
            starts.append(n)
    for i, n in enumerate(starts):
        stop = t[starts[i + 1]] if i + 1 < len(starts) else t[seg[-1]] + 0.13
        png = os.path.join(w, "fr", f"{k:04d}.png")
        subprocess.run([os.path.join(w, "shot"), "-cols", "160", "-rows", "46", *os.environ.get("FILM_FLAGS", "-title hlp").split(),
                        "-bg", "#131315", "-scale", "1", "-o", png, ansi(n)], check=True, capture_output=True)
        lines.append(f"file '{png}'\nduration {stop - t[n]:.3f}")
        k += 1
lines.append(lines[-1].split("\n")[0])
open(os.path.join(w, "concat.txt"), "w").write("\n".join(lines) + "\n")
PY

ffmpeg -v error -y -f concat -safe 0 -i "$WORK/concat.txt" -vf "palettegen=max_colors=128:stats_mode=full" -frames:v 1 "$WORK/pal.png"
ffmpeg -v error -y -f concat -safe 0 -i "$WORK/concat.txt" -i "$WORK/pal.png" \
	-lavfi "[0:v][1:v]paletteuse=dither=none:diff_mode=rectangle" -fps_mode vfr -loop 0 "$OUT"
echo "film: $OUT"
