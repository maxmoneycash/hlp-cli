// Command shot renders ANSI terminal output (a `tmux capture-pane -p -e`
// dump, or any program's colored output) to a crisp PNG, framed like a
// macOS window in the spirit of Charm's freeze.
//
//	shot [flags] [input.ansi]    (reads stdin when no file is given)
//
// It is a docs and screenshot tool for hlp; it does not import hlp.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"strings"
)

func main() {
	var (
		out      = flag.String("o", "shot.png", "output PNG path")
		cols     = flag.Int("cols", 0, "grid width in cells (wraps longer lines; 0 = widest line)")
		rows     = flag.Int("rows", 0, "grid height in cells (0 = number of lines)")
		trim     = flag.Bool("trim", false, "drop trailing blank rows (after -rows)")
		fontSpec = flag.String("font", "", "font family: sf, jetbrains, menlo, sfnsmono, or a .ttf/.otf/.ttc path (default: first installed)")
		size     = flag.Float64("size", 14, "font size in points")
		lh       = flag.Float64("line-height", 1.22, "line height as a multiple of the font size")
		scale    = flag.Float64("scale", 2, "pixel scale (2 = retina)")
		title    = flag.String("title", "", "window title")
		noFrame  = flag.Bool("no-frame", false, "no window, shadow or backdrop: just the padded terminal")
		noShadow = flag.Bool("no-shadow", false, "no drop shadow")
		pad      = flag.Float64("pad", 20, "padding inside the window, in points")
		margin   = flag.Float64("margin", 64, "backdrop margin around the window, in points")
		radius   = flag.Float64("radius", 12, "window corner radius, in points")
		bdrop    = flag.String("backdrop", "dark", "backdrop: dark, light, warm, none (transparent), #hex or #top,#bottom")
		bg       = flag.String("bg", "#0a0a0b", "terminal default background")
		fg       = flag.String("fg", "#f4f3ef", "terminal default foreground")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: shot [flags] [input.ansi]\n\nRenders ANSI text to a framed PNG.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	var r io.Reader = bufio.NewReader(os.Stdin)
	if flag.NArg() > 0 && flag.Arg(0) != "-" {
		f, err := os.Open(flag.Arg(0))
		if err != nil {
			die(err)
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(r)
	if err != nil {
		die(err)
	}
	g := parseANSI(string(data), *cols, *rows)
	if *trim {
		h := g.h
		for h > 1 && blankRow(g, h-1) {
			h--
		}
		g.cells, g.h = g.cells[:h*g.w], h
	}

	fs, err := newFontSet(*fontSpec, *size**scale, *lh)
	if err != nil {
		die(err)
	}
	p := &painter{fs: fs}
	var ok bool
	if p.bg, ok = hexColor(*bg); !ok {
		die(fmt.Errorf("bad -bg %q", *bg))
	}
	if p.fg, ok = hexColor(*fg); !ok {
		die(fmt.Errorf("bad -fg %q", *fg))
	}
	for i, h := range ansi16 {
		p.pal[i] = mustHex(h)
	}
	p.lw = max(1, int(float64(fs.cellW)/8.5+0.5))

	img := compose(g, p, frameOpts{
		frame: !*noFrame, shadow: !*noShadow, title: *title, pad: *pad,
		margin: *margin, radius: *radius, backdrop: strings.ToLower(*bdrop), scale: *scale,
	})
	f, err := os.Create(*out)
	if err != nil {
		die(err)
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(f, img); err != nil {
		die(err)
	}
	if err := f.Close(); err != nil {
		die(err)
	}
	fmt.Fprintf(os.Stderr, "shot: %s  %dx%d px  (%dx%d cells, font %s, cell %dx%d px)\n",
		*out, img.Bounds().Dx(), img.Bounds().Dy(), g.w, g.h, fs.fam.name, fs.cellW, fs.cellH)
}

func blankRow(g *grid, y int) bool {
	for x := 0; x < g.w; x++ {
		c := g.at(x, y)
		if (c.text != "" && c.text != " ") || c.attrs.bg.kind != colDefault {
			return false
		}
	}
	return true
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "shot:", err)
	os.Exit(1)
}
