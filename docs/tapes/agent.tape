# agent.gif (10 s): Claude Code calling hlp over MCP.
# Needs Claude Code installed and signed in on the recording machine (the
# owner records this one; nothing here calls a paid API in CI). Register once:
#   claude mcp add hlp -- hlp mcp
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

Output docs/gifs/agent.gif
Output docs/gifs/agent.mp4
Env HLP_CONFIG "/tmp/hlp-demo/config.toml"
Env PS1 "$ "

Require claude
Type "claude mcp list | grep hlp"
Enter
Sleep 1s
Type `claude -p "find vaults under 15% drawdown beating HLP this quarter; answer as a table"`
Enter
Sleep 8s
