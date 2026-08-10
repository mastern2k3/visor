package render

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

// Text sizes for the two panel lines. Rendered at 72 DPI so points map 1:1
// to pixels, matching the geometry constants in tab.go.
const (
	NamePt  = 12.5 // line 1: project name
	MetaPt  = 9.5  // line 2: state words, elapsed, path
	GlyphPt = 11   // capsule glyph
)

// Faces holds the parsed faces the renderer needs. Baselines are derived from
// these metrics rather than hardcoded, replacing the old empirical constant.
type Faces struct {
	Name  font.Face
	Meta  font.Face
	Glyph font.Face
}

// FontPath resolves a monospaced TrueType/OpenType font path, preferring
// fontconfig and falling back to the hardcoded candidates in font.go.
func FontPath() (string, error) {
	out, err := exec.Command("fc-match", "-f", "%{file}\n",
		"monospace:fontformat=TrueType").Output()
	if err == nil {
		if p := strings.TrimSpace(string(out)); p != "" {
			if _, serr := os.Stat(p); serr == nil {
				return p, nil
			}
		}
	}
	for _, p := range fontCandidates {
		if _, serr := os.Stat(p); serr == nil {
			return p, nil
		}
	}
	return "", errors.New("no mono TTF font found: fc-match not on PATH (or returned non-TTF) and no fallback paths under /usr/share/fonts matched")
}

// LoadFaces resolves the system mono font and derives the three faces.
func LoadFaces() (*Faces, error) {
	p, err := FontPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read font %s: %w", p, err)
	}
	sf, err := opentype.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("parse font %s: %w", p, err)
	}
	mk := func(pt float64) (font.Face, error) {
		return opentype.NewFace(sf, &opentype.FaceOptions{
			Size: pt, DPI: 72, Hinting: font.HintingFull,
		})
	}
	nameFace, err := mk(NamePt)
	if err != nil {
		return nil, err
	}
	metaFace, err := mk(MetaPt)
	if err != nil {
		return nil, err
	}
	glyphFace, err := mk(GlyphPt)
	if err != nil {
		return nil, err
	}
	return &Faces{Name: nameFace, Meta: metaFace, Glyph: glyphFace}, nil
}
