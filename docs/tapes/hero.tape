# hero.gif (8 s loop): type a command, get a live chart, zoom in.
# One focal object: the chart.
# On the day, pick the most active of HYPE, BTC and SOL (`hlp markets --limit 5`)
# and change the coin below; a flat market makes a dead hero.
# Poster: docs/gifs/hero.png at 7.0 s (wide candles, settled, live tick).
Output docs/gifs/hero.gif
Output docs/gifs/hero.mp4
Require hlp
Env HLP_CONFIG "/tmp/hlp-demo/config.toml"
Env PS1 "$ "
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

# Open on a finished screen: the shell with a small markets card.
Hide
Type "clear && hlp --theme hlp"
Enter
Sleep 1.5s
Type "/markets --sort volume --limit 6"
Enter
Sleep 1.5s
Show

# 0.0-0.6 calm open, then type the command (15 chars at 45 ms).
Sleep 600ms
Type "/chart HYPE 15m"
Sleep 200ms
Enter
# 1.5-2.6 the inline card ticks live.
Sleep 1.1s
# 2.6 Enter on the empty prompt opens the newest card full screen.
Enter
Sleep 1.2s
# 3.8 crosshair on, walk back three candles; the header reads their OHLC.
Type "c"
Sleep 200ms
Left
Sleep 250ms
Left
Sleep 250ms
Left
Sleep 550ms
Type "c"
Sleep 300ms
# 5.6 zoom: bodies widen (the pane opens at stride 2 at this width).
Type "+"
Sleep 1.4s
Screenshot docs/gifs/hero.png
Sleep 1s

# 8.0 cut back to the calm open. If the seam jars in review, move an Escape
# to 7.6 s so the take ends on the shell with the chart card in it.
Hide
Escape
Ctrl+C
Ctrl+C
