# chart.gif (10 s): intervals, zoom, crosshair, MAs, Heikin-Ashi, theme.
# Every change has a cause on screen: a key press shown in the footer.
Set Shell "bash"
Set FontFamily "JetBrains Mono"
Set FontSize 14
Set Width 1200
Set Height 700
Set Padding 24
Set Framerate 30
Set TypingSpeed 45ms
Set CursorBlink false
Set Theme { "name": "hlp", "background": "#0E0F0F", "foreground": "#E1E5EB", "cursor": "#97FCE4", "selection": "#292B2B", "black": "#0E0F0F", "red": "#FF5C7A", "green": "#3DDC97", "yellow": "#F0B90B", "blue": "#38BDF8", "magenta": "#A78BFA", "cyan": "#97FCE4", "white": "#E1E5EB", "brightBlack": "#5A6478", "brightRed": "#FF5C7A", "brightGreen": "#3DDC97", "brightYellow": "#F0B90B", "brightBlue": "#38BDF8", "brightMagenta": "#A78BFA", "brightCyan": "#97FCE4", "brightWhite": "#FFFFFF" }
Require hlp

Output docs/gifs/chart.gif
Output docs/gifs/chart.mp4
Env HLP_CONFIG "/tmp/hlp-demo/config.toml"
Env PS1 "$ "

Hide
Type "hlp"
Enter
Sleep 1s
Type "/chart BTC 1m"
Enter
Sleep 1s
Ctrl+O
Sleep 400ms
Show

Sleep 800ms
# 1m -> 4h -> 1d (number keys).
Type "6"
Sleep 1.2s
Type "8"
Sleep 1.2s
# Zoom in, then out.
Type "+"
Sleep 600ms
Type "-"
Sleep 500ms
# Crosshair walks back three candles; the header reads OHLC under it.
Type "c"
Sleep 300ms
Left
Sleep 250ms
Left
Sleep 250ms
Left
Sleep 900ms
Type "c"
# Moving averages off and on, then Heikin-Ashi (s cycles styles).
Type "i"
Sleep 500ms
Type "i"
Sleep 400ms
Type "ss"
Sleep 1.2s
Ctrl+T
Sleep 1.2s

Hide
Escape
Ctrl+C
Ctrl+C
