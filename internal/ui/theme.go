package ui

import (
	"image/color"
	"log/slog"
	"os"
	"sort"
	"strings"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// Palette is a small set of colors used throughout the app. Kept minimal on
// purpose; tuned for a calm, neutral dark surface where contrast comes from
// typography and 1px borders rather than heavy fills.
type Palette struct {
	Bg            color.NRGBA
	BgHeader      color.NRGBA
	BgRowAlt      color.NRGBA // subtle hover/active row tint
	BgRowSelected color.NRGBA // stronger selection tint
	Text          color.NRGBA
	TextStrong    color.NRGBA
	TextDim       color.NRGBA
	TextMuted     color.NRGBA
	Accent        color.NRGBA
	AccentSoft    color.NRGBA // muted accent for soft hairlines / hints
	AccentText    color.NRGBA
	Link          color.NRGBA
	Unread        color.NRGBA
	Star          color.NRGBA
	Border        color.NRGBA
	BorderStrong  color.NRGBA
	StatusBg      color.NRGBA
}

func darkPalette() Palette {
	return Palette{
		Bg:            rgb(0x0d1017),
		BgHeader:      rgb(0x0d1017),
		BgRowAlt:      rgb(0x141821),
		BgRowSelected: rgb(0x1a1f2c),
		Text:          rgb(0xc8d0dc),
		TextStrong:    rgb(0xeef1f6),
		TextDim:       rgb(0x7c8595),
		TextMuted:     rgb(0x4f5663),
		Accent:        rgb(0x7aa2f7),
		AccentSoft:    rgb(0x3d5a99),
		AccentText:    rgb(0xffffff),
		Link:          rgb(0x89b4fa),
		Unread:        rgb(0x9ece6a),
		Star:          rgb(0xe0af68),
		Border:        rgb(0x1a1f29),
		BorderStrong:  rgb(0x252b38),
		StatusBg:      rgb(0x0a0c12),
	}
}

func lightPalette() Palette {
	return Palette{
		Bg:            rgb(0xffffff),
		BgHeader:      rgb(0xffffff),
		BgRowAlt:      rgb(0xf5f7f9),
		BgRowSelected: rgb(0xe8ebf0),
		Text:          rgb(0x24292f),
		TextStrong:    rgb(0x0969da),
		TextDim:       rgb(0x57606a),
		TextMuted:     rgb(0x8c959f),
		Accent:        rgb(0x0969da),
		AccentSoft:    rgb(0xafdaff),
		AccentText:    rgb(0xffffff),
		Link:          rgb(0x0969da),
		Unread:        rgb(0x1a7f37),
		Star:          rgb(0x9a6700),
		Border:        rgb(0xd0d7de),
		BorderStrong:  rgb(0xafb8c1),
		StatusBg:      rgb(0xf6f8fa),
	}
}

// FontStyle is the per-section typeface + size used when rendering labels.
// Face "" or Size 0 means "fall back to the theme default".
type FontStyle struct {
	Face string
	Size float32
}

// SectionFonts groups the user-configurable styles for each part of the UI.
type SectionFonts struct {
	Global    FontStyle
	List      FontStyle
	Message   FontStyle
	Compose   FontStyle
	StatusBar FontStyle
}

// Theme bundles the gioui Material theme with our palette.
type Theme struct {
	*material.Theme
	Pal   Palette
	BoldF font.Font
	MonoF font.Font

	// Faces lists the unique typeface names available to the shaper, sorted
	// alphabetically. The Settings screen cycles through this list.
	Faces []string
	// MonoFaces is the subset of Faces that contain mono in the typeface name.
	MonoFaces []string

	// Fonts holds the live per-section overrides. Settings mutates this in
	// place; the next Layout pass picks up the new values.
	Fonts SectionFonts
}

func newTheme() *Theme {
	mat := material.NewTheme()
	mat.TextSize = unit.Sp(13)

	collection := gofont.Collection()
	collection = append(collection, loadSystemFaces()...)
	mat.Shaper = text.NewShaper(text.WithCollection(collection))

	pal := darkPalette()
	mat.Palette.Bg = pal.Bg
	mat.Palette.Fg = pal.Text
	mat.Palette.ContrastBg = pal.Accent
	mat.Palette.ContrastFg = pal.AccentText

	monoFace := "Go Mono"
	faces, monoFaces := uniqueFaces(collection)

	uiDefault := FontStyle{}

	return &Theme{
		Theme: mat,
		Pal:   pal,
		BoldF: font.Font{Weight: font.Bold},
		MonoF: font.Font{Typeface: font.Typeface(monoFace)},
		Faces: faces,
		MonoFaces: monoFaces,
		Fonts: SectionFonts{
			Global:    FontStyle{Size: 13},
			List:      uiDefault,
			Message:   uiDefault,
			Compose:   uiDefault,
			StatusBar: uiDefault,
		},
	}
}

func (t *Theme) SetDarkMode(dark bool) {
	if dark {
		t.Pal = darkPalette()
	} else {
		t.Pal = lightPalette()
	}
	t.Theme.Palette.Bg = t.Pal.Bg
	t.Theme.Palette.Fg = t.Pal.Text
	t.Theme.Palette.ContrastBg = t.Pal.Accent
	t.Theme.Palette.ContrastFg = t.Pal.AccentText
}

// applyFont mutates a material.LabelStyle to honor a section's typeface and
// size, leaving zero fields untouched so theme defaults survive.
func (t *Theme) applyFont(lbl *material.LabelStyle, fs FontStyle) {
	face := fs.Face
	if face == "" {
		face = t.Fonts.Global.Face
	}
	if face != "" {
		lbl.Font.Typeface = font.Typeface(face)
	}

	size := fs.Size
	if size == 0 {
		size = t.Fonts.Global.Size
	}
	if size > 0 {
		lbl.TextSize = unit.Sp(size)
	}
}

func (t *Theme) applyEditorFont(ed *material.EditorStyle, fs FontStyle) {
	face := fs.Face
	if face == "" {
		face = t.Fonts.Global.Face
	}
	if face != "" {
		ed.Font.Typeface = font.Typeface(face)
	}

	size := fs.Size
	if size == 0 {
		size = t.Fonts.Global.Size
	}
	if size > 0 {
		ed.TextSize = unit.Sp(size)
	}
}

// uniqueFaces collapses the loaded font collection into a sorted, deduplicated
// list of typeface names.
func uniqueFaces(coll []font.FontFace) (faces []string, mono []string) {
	seen := map[string]bool{}
	for _, f := range coll {
		name := string(f.Font.Typeface)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		faces = append(faces, name)
	}
	sort.Strings(faces)
	for _, name := range faces {
		if strings.Contains(strings.ToLower(name), "mono") {
			mono = append(mono, name)
		}
	}
	if len(mono) == 0 {
		mono = faces
	}
	return faces, mono
}

// loadSystemFaces tries common system locations for sharper UI fonts.
func loadSystemFaces() []font.FontFace {
	candidates := []string{
		"/usr/share/fonts/inter/Inter-Regular.otf",
		"/usr/share/fonts/inter/Inter-Bold.otf",
		"/usr/share/fonts/TTF/Inter-Regular.ttf",
		"/usr/share/fonts/TTF/Inter-Bold.ttf",
		"/usr/share/fonts/truetype/inter/Inter-Regular.ttf",
		"/usr/share/fonts/ibm-plex/IBMPlexSans-Regular.otf",
		"/usr/share/fonts/ibm-plex/IBMPlexSans-Bold.otf",
		"/usr/share/fonts/jetbrains-mono/JetBrainsMono-Regular.ttf",
	}
	var out []font.FontFace
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		faces, err := opentype.ParseCollection(data)
		if err != nil {
			slog.Warn("ui font parse failed", "path", p, "error", err)
			continue
		}
		out = append(out, faces...)
	}
	return out
}

func rgb(hex uint32) color.NRGBA {
	return color.NRGBA{
		R: uint8(hex >> 16),
		G: uint8(hex >> 8),
		B: uint8(hex),
		A: 0xff,
	}
}
