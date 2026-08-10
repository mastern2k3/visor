package render

import "testing"

func TestLoadFaces(t *testing.T) {
	f, err := LoadFaces()
	if err != nil {
		t.Skipf("no system font: %v", err)
	}
	if f.Name == nil || f.Meta == nil || f.Glyph == nil {
		t.Fatalf("LoadFaces returned nil face: %+v", f)
	}
	// The name face must be visibly larger than the meta face.
	nameH := f.Name.Metrics().Height.Ceil()
	metaH := f.Meta.Metrics().Height.Ceil()
	glyphH := f.Glyph.Metrics().Height.Ceil()
	if nameH <= metaH {
		t.Errorf("name face height %d must exceed meta face height %d", nameH, metaH)
	}
	// Glyph face (11pt) must fall between meta (9.5pt) and name (12.5pt) heights.
	if glyphH <= metaH || glyphH >= nameH {
		t.Errorf("glyph face height %d must fall between meta %d and name %d", glyphH, metaH, nameH)
	}
	// Ascent must be positive — this is what replaces the hardcoded baseline.
	if f.Name.Metrics().Ascent.Ceil() <= 0 {
		t.Errorf("name ascent = %d, want > 0", f.Name.Metrics().Ascent.Ceil())
	}
}

func TestFontPath(t *testing.T) {
	p, err := FontPath()
	if err != nil {
		t.Skipf("no system font: %v", err)
	}
	if p == "" {
		t.Errorf("FontPath returned empty path with nil error")
	}
}
