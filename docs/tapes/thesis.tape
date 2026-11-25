# thesis.gif (10 s): a plain-YAML thesis backtested against HLP.
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

Output docs/gifs/thesis.gif
Output docs/gifs/thesis.mp4
Env HLP_CONFIG "/tmp/hlp-demo/config.toml"
Env PS1 "$ "

Type "cat examples/thesis/long-spy-short-memes.yaml"
Enter
Sleep 1.8s
Type "hlp backtest examples/thesis/long-spy-short-memes.yaml --days 180"
Enter
# The tearsheet card prints in place: curve vs HLP and BTC, metrics, monthly heatmap.
Sleep 4s
Type "hlp backtest --thesis 'long BTC, short DOGE FARTCOIN, rebalance 1w' --days 180"
Enter
Sleep 2s
