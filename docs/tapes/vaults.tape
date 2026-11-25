# vaults.gif (10 s, seamless loop): screener by Sharpe, find a vault, read its tearsheet.
# Vault: "Long HYPE & BTC | Short Garbage". Ask its leader first (LAUNCH-PLAN
# section 8); if they decline, filter for "hyperliquidity" to show HLP instead.
# Poster: docs/gifs/vaults.png at 4.4 s (curve vs HLP/BTC/HYPE and metrics).
Output docs/gifs/vaults.gif
Output docs/gifs/vaults.mp4
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

# Open on the full-screen screener sorted by Sharpe, selection on row 1.
Hide
Type "clear && hlp --theme hlp"
Enter
Sleep 1.5s
Type "/vaults --sort sharpe --min-tvl 100k"
Enter
Sleep 2s
Enter
Sleep 800ms
Show

Sleep 800ms
# Browse three rows.
Type "j"
Sleep 200ms
Type "j"
Sleep 200ms
Type "j"
Sleep 300ms
# Filter: the list narrows as each letter lands.
Type "/"
Type@70ms "garbage"
Sleep 300ms
Enter
Sleep 400ms
# Open the tearsheet.
Enter
Sleep 1.6s
Screenshot docs/gifs/vaults.png
Sleep 600ms
# Scroll to the monthly heatmap, then the checklist.
Type "j"
Sleep 250ms
Type "j"
Sleep 250ms
Type "j"
Sleep 250ms
Type "j"
# Payoff: pass, warn and fail marks with one-line reasons.
Sleep 2.4s
Escape
Sleep 500ms
# Clearing the filter restores the opening frame (the seam has a cause).
# If the selection is not on row 1 here in review, add: Type "g"
Type "/"
Escape
Sleep 1s

Hide
Escape
Ctrl+C
Ctrl+C
