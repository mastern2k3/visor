package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse_KeysAndComments(t *testing.T) {
	in := "# a comment\n\ntheme = traffic\nshadow = false\n"
	got := Parse(strings.NewReader(in))
	if got.Theme != "traffic" {
		t.Errorf("Theme = %q, want \"traffic\"", got.Theme)
	}
	if got.Shadow {
		t.Errorf("Shadow = true, want false")
	}
}

func TestParse_DefaultsWhenEmpty(t *testing.T) {
	got := Parse(strings.NewReader(""))
	if got.Theme != "silent" || !got.Shadow {
		t.Errorf("Parse(empty) = %+v, want {silent true}", got)
	}
}

func TestParse_UnknownThemeFallsBack(t *testing.T) {
	got := Parse(strings.NewReader("theme = nonsense\n"))
	if got.Theme != "silent" {
		t.Errorf("Theme = %q, want \"silent\" for unknown theme", got.Theme)
	}
}

func TestParse_IgnoresJunkLines(t *testing.T) {
	got := Parse(strings.NewReader("garbage\ntheme=tuned\nshadow\n"))
	if got.Theme != "tuned" {
		t.Errorf("Theme = %q, want \"tuned\"", got.Theme)
	}
	if !got.Shadow {
		t.Errorf("Shadow = false, want default true when value is malformed")
	}
}

func TestResolve_Precedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "visor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("theme = tuned\nshadow = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// File only.
	if got := Resolve("", nil); got.Theme != "tuned" || got.Shadow {
		t.Errorf("file-only Resolve = %+v, want {tuned false}", got)
	}

	// Env beats file.
	t.Setenv("VISOR_THEME", "traffic")
	t.Setenv("VISOR_SHADOW", "true")
	if got := Resolve("", nil); got.Theme != "traffic" || !got.Shadow {
		t.Errorf("env Resolve = %+v, want {traffic true}", got)
	}

	// Flag beats env.
	no := false
	if got := Resolve("silent", &no); got.Theme != "silent" || got.Shadow {
		t.Errorf("flag Resolve = %+v, want {silent false}", got)
	}
}

func TestSaveThenLoad_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := Save(Config{Theme: "traffic", Shadow: false}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := Load()
	if got.Theme != "traffic" || got.Shadow {
		t.Errorf("Load after Save = %+v, want {traffic false}", got)
	}
}

func TestLoad_MissingFileYieldsDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got := Load()
	if got.Theme != "silent" || !got.Shadow {
		t.Errorf("Load(missing) = %+v, want {silent true}", got)
	}
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		input string
		want  bool
		ok    bool
	}{
		// True variants
		{"true", true, true},
		{"on", true, true},
		{"yes", true, true},
		{"1", true, true},
		// False variants
		{"false", false, true},
		{"off", false, true},
		{"no", false, true},
		{"0", false, true},
		// Invalid
		{"", false, false},
		{"maybe", false, false},
		{"2", false, false},
		{"nope", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := parseBool(tt.input)
			if ok != tt.ok || (ok && got != tt.want) {
				t.Errorf("parseBool(%q) = (%v, %v), want (%v, %v)", tt.input, got, ok, tt.want, tt.ok)
			}
		})
	}
}
