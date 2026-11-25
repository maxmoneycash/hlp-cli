# pine.gif (8 s): an original Pine strategy run locally on Hyperliquid candles.
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

Output docs/gifs/pine.gif
Output docs/gifs/pine.mp4
Env HLP_CONFIG "/tmp/hlp-demo/config.toml"
Env PS1 "$ "

Type "hlp pine run examples/pine/supertrend.pine --coin HYPE --interval 1h"
Enter
# Chart with buy/sell markers, then the stats table.
Sleep 5s
