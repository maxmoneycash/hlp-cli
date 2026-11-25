package main

// font.go loads a monospace family (regular, bold, italic, bold italic) and
// a fallback chain for glyphs the main font lacks (symbols, CJK). Fonts are
// parsed with x/image/font/opentype, which handles TrueType, CFF (.otf) and
// collections (.ttc).

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
)

type style int

const (
	stRegular style = iota
	stBold
	stItalic
	stBoldItalic
)

// family holds one parsed font per style; a missing style is nil and falls
// back to regular (bold is then synthesised by over-striking).
type family struct {
	name  string
	fonts [4]*sfnt.Font
}

type fontSet struct {
	fam      family
	fallback []*sfnt.Font
	faces    map[faceKey]font.Face
	faceOf   map[font.Face]faceKey
	px       float64 // em size in pixels
	cellW    int
	cellH    int
	ascent   float64 // pixels, baseline offset from the top of the cell
}

type faceKey struct {
	f    *sfnt.Font
	size float64
}

var home, _ = os.UserHomeDir()

// Candidate families, best first. Each entry lists the regular, bold,
// italic and bold-italic files; a ".ttc" path with "#n" picks a face index.
var families = map[string][4]string{
	"sf": {
		"/System/Applications/Utilities/Terminal.app/Contents/Resources/Fonts/SF-Mono-Regular.otf",
		"/System/Applications/Utilities/Terminal.app/Contents/Resources/Fonts/SF-Mono-Bold.otf",
		"/System/Applications/Utilities/Terminal.app/Contents/Resources/Fonts/SF-Mono-RegularItalic.otf",
		"/System/Applications/Utilities/Terminal.app/Contents/Resources/Fonts/SF-Mono-BoldItalic.otf",
	},
	"jetbrains": {
		"JetBrainsMono-Regular.ttf", "JetBrainsMono-Bold.ttf",
		"JetBrainsMono-Italic.ttf", "JetBrainsMono-BoldItalic.ttf",
	},
	"menlo": {
		"/System/Library/Fonts/Menlo.ttc#0", "/System/Library/Fonts/Menlo.ttc#1",
		"/System/Library/Fonts/Menlo.ttc#2", "/System/Library/Fonts/Menlo.ttc#3",
	},
	"sfnsmono": {"/System/Library/Fonts/SFNSMono.ttf", "", "/System/Library/Fonts/SFNSMonoItalic.ttf", ""},
}

var familyOrder = []string{"sf", "jetbrains", "menlo", "sfnsmono"}

var fallbackFiles = []string{
	"/System/Library/Fonts/Menlo.ttc#0",
	"/System/Library/Fonts/SFNSMono.ttf",
	"/System/Library/Fonts/Apple Symbols.ttf",
	"/Library/Fonts/Arial Unicode.ttf",
	"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
	"/System/Library/Fonts/Hiragino Sans GB.ttc#0",
	"/System/Library/Fonts/STHeiti Light.ttc#0",
	"/System/Library/Fonts/AppleSDGothicNeo.ttc#0",
	"/System/Library/Fonts/CJKSymbolsFallback.ttc#0",
}

// resolve finds a font file: absolute paths as given, bare names in the
// user and system font folders.
func resolve(p string) string {
	if p == "" {
		return ""
	}
	name, idx, _ := strings.Cut(p, "#")
	cands := []string{name}
	if !filepath.IsAbs(name) {
		cands = []string{
			filepath.Join(home, "Library/Fonts", name),
			filepath.Join("/Library/Fonts", name),
			filepath.Join("/System/Library/Fonts", name),
			filepath.Join("/System/Library/Fonts/Supplemental", name),
			name,
		}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			if idx != "" {
				return c + "#" + idx
			}
			return c
		}
	}
	return ""
}

var parsed = map[string]*sfnt.Font{}

func loadFont(p string) (*sfnt.Font, error) {
	if f, ok := parsed[p]; ok {
		return f, nil
	}
	name, idxs, _ := strings.Cut(p, "#")
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var f *sfnt.Font
	if strings.HasSuffix(strings.ToLower(name), ".ttc") || strings.HasSuffix(strings.ToLower(name), ".otc") {
		c, err := opentype.ParseCollection(data)
		if err != nil {
			return nil, err
		}
		idx := 0
		fmt.Sscan(idxs, &idx)
		if idx >= c.NumFonts() {
			idx = 0
		}
		f, err = c.Font(idx)
		if err != nil {
			return nil, err
		}
	} else {
		f, err = opentype.Parse(data)
		if err != nil {
			return nil, err
		}
	}
	parsed[p] = f
	return f, nil
}

// loadFamily picks the requested family ("sf", "jetbrains", "menlo",
// "sfnsmono", a font file path, or "" for the first one installed).
func loadFamily(spec string) (family, error) {
	try := func(name string, files [4]string) (family, bool) {
		var fam family
		fam.name = name
		for i, f := range files {
			r := resolve(f)
			if r == "" {
				if i == 0 {
					return fam, false
				}
				continue
			}
			ft, err := loadFont(r)
			if err != nil {
				if i == 0 {
					return fam, false
				}
				continue
			}
			fam.fonts[i] = ft
		}
		return fam, true
	}
	if spec != "" {
		if files, ok := families[strings.ToLower(spec)]; ok {
			if fam, ok := try(spec, files); ok {
				return fam, nil
			}
			fmt.Fprintf(os.Stderr, "shot: font family %q not installed, falling back\n", spec)
		} else {
			if fam, ok := try(filepath.Base(spec), [4]string{spec}); ok {
				return fam, nil
			}
			fmt.Fprintf(os.Stderr, "shot: cannot load font %q, falling back\n", spec)
		}
	}
	for _, n := range familyOrder {
		if fam, ok := try(n, families[n]); ok {
			return fam, nil
		}
	}
	return family{}, fmt.Errorf("no monospace font found (tried %v)", familyOrder)
}

func newFontSet(spec string, px, lineHeight float64) (*fontSet, error) {
	fam, err := loadFamily(spec)
	if err != nil {
		return nil, err
	}
	fs := &fontSet{fam: fam, px: px, faces: map[faceKey]font.Face{}, faceOf: map[font.Face]faceKey{}}
	for _, f := range fallbackFiles {
		if r := resolve(f); r != "" {
			if ft, err := loadFont(r); err == nil {
				fs.fallback = append(fs.fallback, ft)
			}
		}
	}
	reg := fs.face(fam.fonts[stRegular])
	adv, ok := reg.GlyphAdvance('M')
	if !ok {
		adv, _ = reg.GlyphAdvance('0')
	}
	m := reg.Metrics()
	asc, desc := fix(m.Ascent), fix(m.Descent)
	fs.cellW = int(math.Round(fix(adv)))
	fs.cellH = int(math.Round(px * lineHeight))
	if min := int(math.Ceil(asc + desc)); fs.cellH < min {
		fs.cellH = min
	}
	// Centre the font's ascent+descent box in the cell.
	fs.ascent = math.Round((float64(fs.cellH)-(asc+desc))/2 + asc)
	return fs, nil
}

func (fs *fontSet) face(f *sfnt.Font) font.Face { return fs.sized(f, fs.px) }

func (fs *fontSet) sized(f *sfnt.Font, size float64) font.Face {
	k := faceKey{f, size}
	if fc, ok := fs.faces[k]; ok {
		return fc
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err)
	}
	fs.faces[k] = fc
	fs.faceOf[fc] = k
	return fc
}

// scaled returns face shrunk by factor (for over-wide fallback glyphs).
func (fs *fontSet) scaled(fc font.Face, factor float64) font.Face {
	k, ok := fs.faceOf[fc]
	if !ok {
		return fc
	}
	return fs.sized(k.f, k.size*factor)
}

var buf sfnt.Buffer

func hasGlyph(f *sfnt.Font, r rune) bool {
	i, err := f.GlyphIndex(&buf, r)
	return err == nil && i != 0
}

// pick returns the face to draw r with and whether bold must be faked.
func (fs *fontSet) pick(r rune, st style) (font.Face, bool) {
	want := fs.fam.fonts[st]
	fakeBold := false
	if want == nil {
		switch st {
		case stBoldItalic:
			if fs.fam.fonts[stItalic] != nil {
				want, fakeBold = fs.fam.fonts[stItalic], true
			} else if fs.fam.fonts[stBold] != nil {
				want = fs.fam.fonts[stBold]
			}
		case stBold:
			fakeBold = true
		}
		if want == nil {
			want = fs.fam.fonts[stRegular]
		}
	}
	if hasGlyph(want, r) {
		return fs.face(want), fakeBold
	}
	if want != fs.fam.fonts[stRegular] && hasGlyph(fs.fam.fonts[stRegular], r) {
		return fs.face(fs.fam.fonts[stRegular]), st == stBold || st == stBoldItalic
	}
	for _, f := range fs.fallback {
		if hasGlyph(f, r) {
			return fs.face(f), st == stBold || st == stBoldItalic
		}
	}
	return fs.face(fs.fam.fonts[stRegular]), fakeBold
}
