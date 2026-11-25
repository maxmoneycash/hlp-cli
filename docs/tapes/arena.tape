# arena.gif (12 s): two local models, a Pine script and a thesis on one live
# feed, same $10k of paper money. PAPER on screen.
#
# BLOCKED for the launch take: arenas cannot resume yet (each run gets a random
# id; storyboard "Engineer actions" 1). Until `hlp arena --id` exists, this tape
# records a fresh arena whose curves start flat at $10,000, which fails the
# storyboard's acceptance. Once it lands:
#   1. Pre-roll at least 48 hours before recording:
#        ollama pull qwen3:8b && ollama pull llama3.1:8b
#        tmux new -s arena 'hlp arena examples/arena/demo.yaml --llm --id launch-arena'
#   2. Append " --id launch-arena" to the /arena line below.
# Poster: docs/gifs/arena.png at 5.5 s. Framerate 24 if the GIF exceeds 2 MB.
Output docs/gifs/arena.gif
Output docs/gifs/arena.mp4
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

Hide
Type "clear && hlp --theme hlp"
Enter
Sleep 1.5s
Type "/markets --sort volume --limit 6"
Enter
Sleep 1.5s
Show

Sleep 500ms
# A long path: type it fast so it does not eat the take.
Type@20ms "/arena examples/arena/demo.yaml --llm"
Sleep 200ms
Enter
# 1.9-3.4 arena card: entrants, ranks, returns, PAPER badge.
Sleep 1.5s
# 3.4 full screen: leaderboard over overlaid equity curves, ticking with marks.
Enter
Sleep 2.1s
Screenshot docs/gifs/arena.png
Sleep 500ms
# Scroll to each entrant's latest decision: model reason, Pine signal, thesis rebalance.
Type "j"
Sleep 300ms
Type "j"
Sleep 3s
Escape
# Hold on the shell; the arena card keeps ticking. Cut shell to shell.
Sleep 2.4s

Hide
Ctrl+C
Ctrl+C
