# paper.gif (10 s): a local model's paper vault, a day in. PAPER on screen throughout.
#
# Pre-roll at least 24 hours before recording, outside VHS, on this machine:
#   ollama pull qwen3:8b
#   tmux new -s macro 'hlp paper start llm:examples/llm/macro-trader.yaml --id macro-demo --capital 10000 --llm'
#   hlp paper status --id macro-demo          # check it is running and logging decisions
# The tape reads the same session from a second process. If `paper status`
# cannot see the running session's decision log from here, record inside the
# tmux session's own hlp instead (storyboard "Fallback").
# Poster: docs/gifs/paper.png at 7.0 s (decision log rows readable).
Output docs/gifs/paper.gif
Output docs/gifs/paper.mp4
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

# Open on the sessions list: one row, the macro trader, running.
Hide
Type "clear && hlp --theme hlp"
Enter
Sleep 1.5s
Type "/paper list"
Enter
Sleep 1s
Show

Sleep 600ms
Type "/paper status --id macro-demo"
Sleep 200ms
Enter
# 2.1-3.8 status card: model, PAPER badge, equity vs HLP, positions.
Sleep 1.7s
# 3.8 open it full screen, then scroll down to the decision log.
Enter
Sleep 600ms
Type "j"
Sleep 300ms
Type "j"
Sleep 300ms
Type "j"
Sleep 300ms
Type "j"
# Payoff: time, target weights and the model's one-line reason per row.
Sleep 1.4s
Screenshot docs/gifs/paper.png
Sleep 1.6s
Escape
# Hold on the shell; the loop cuts shell to shell.
Sleep 1.4s

Hide
Ctrl+C
Ctrl+C
