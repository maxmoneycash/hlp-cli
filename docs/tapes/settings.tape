# Shared look for every hlp GIF.
# Each tape inlines these lines too, so any tape renders on its own; keep them
# in sync when changing the look.
#
# Render (maintainer only, at a busy hour, on real mainnet public data, with no
# key in the environment):
#   make gifs                                # every tape, with bin/hlp on PATH
#   vhs docs/tapes/hero.tape                 # one tape
#   gifsicle -O3 --batch docs/gifs/*.gif     # then check each is under 2 MB
# If a GIF is still over 2 MB: Framerate 24, then --lossy=20 (check the eighth
# blocks at 100% zoom; lossy compression smears them first).
#
# Verify the grid once before recording (expect at least 120 x 32):
#   Output /tmp/size.gif + the lines below, then: Type "stty size" / Enter /
#   Screenshot /tmp/size.png. Under 120 columns: FontSize 13, not a wider canvas.
#
# Every tape points HLP_CONFIG at a path with no file, so defaults apply and no
# personal watchlist, theme or account leaks into a recording.
#   Env HLP_CONFIG "/tmp/hlp-demo/config.toml"
#   Env PS1 "$ "

Set Shell "bash"
Set FontFamily "JetBrains Mono"
Set FontSize 14
Set Width 1200
Set Height 700
Set Padding 24
Set Framerate 30
Set TypingSpeed 45ms
Set CursorBlink false
# Brand palette (PLAN section 9): surface #0E0F0F, text #E1E5EB, mint accent
# #97FCE4, up #3DDC97, down #FF5C7A. hlp draws truecolor from its own theme, so
# this mostly sets the background and the 16-colour fallback. Yellow, blue and
# magenta have no brand value yet.
Set Theme { "name": "hlp", "background": "#0E0F0F", "foreground": "#E1E5EB", "cursor": "#97FCE4", "selection": "#292B2B", "black": "#0E0F0F", "red": "#FF5C7A", "green": "#3DDC97", "yellow": "#F0B90B", "blue": "#38BDF8", "magenta": "#A78BFA", "cyan": "#97FCE4", "white": "#E1E5EB", "brightBlack": "#5A6478", "brightRed": "#FF5C7A", "brightGreen": "#3DDC97", "brightYellow": "#F0B90B", "brightBlue": "#38BDF8", "brightMagenta": "#A78BFA", "brightCyan": "#97FCE4", "brightWhite": "#FFFFFF" }
