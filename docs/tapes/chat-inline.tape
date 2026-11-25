# chat-inline.gif (8 s): plain words in the shell, no model needed.
# The intent parser turns "how is btc doing" into a mini chart with funding and OI.
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

Output docs/gifs/chat-inline.gif
Output docs/gifs/chat-inline.mp4
Env HLP_CONFIG "/tmp/hlp-demo/config.toml"
Env PS1 "$ "

Hide
Type "hlp"
Enter
Sleep 1s
Show

Type "how's btc doing"
Sleep 300ms
Enter
Sleep 2.4s
Type "funding"
Enter
Sleep 2.4s

Hide
Ctrl+C
Ctrl+C
