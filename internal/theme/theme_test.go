package theme

import (
	"strings"
	"testing"

	"github.com/muesli/termenv"
)

func TestNamesAndDefault(t *testing.T) {
	names := Names()
	if len(names) < 5 {
		t.Fatalf("expected several built-in themes, got %v", names)
	}
	if names[0] != Default() || Default() != "dark" {
		t.Fatalf("default should be dark and listed first, got %q first=%q", Default(), names[0])
	}
	for _, n := range names {
		if !Has(n) {
			t.Errorf("Has(%q) = false", n)
		}
	}
	if Has("nope") {
		t.Error("Has reported an unknown theme")
	}
}

func TestGetUnknown(t *testing.T) {
	if _, ok := Get("does-not-exist"); ok {
		t.Fatal("Get returned ok for an unknown theme")
	}
	if _, ok := Get("dracula"); !ok {
		t.Fatal("Get(dracula) not ok")
	}
}

func TestSetChangesCurrent(t *testing.T) {
	orig := Current().Name
	t.Cleanup(func() { Set(orig) })

	if !Set("nord") {
		t.Fatal("Set(nord) returned false")
	}
	if Current().Name != "nord" {
		t.Fatalf("Current = %q, want nord", Current().Name)
	}
	if Set("bogus") {
		t.Fatal("Set(bogus) returned true")
	}
	if Current().Name != "nord" {
		t.Fatal("failed Set changed the current theme")
	}
}

func TestTrueColorSequences(t *testing.T) {
	orig := Current().Name
	t.Cleanup(func() { SetColorProfile(termenv.EnvColorProfile()); Set(orig) })

	SetColorProfile(termenv.TrueColor)
	th, _ := Get("dracula")
	// #bd93f9 → rgb(189,147,249)
	if !strings.Contains(th.Accent, "38;2;189;147;249") {
		t.Errorf("dracula accent = %q, want truecolor rgb", th.Accent)
	}
	if th.Reset != "\033[0m" {
		t.Errorf("reset = %q", th.Reset)
	}
	// Muted carries the faint attribute (2) in front of the color.
	if !strings.HasPrefix(th.Muted, "\033[2;") {
		t.Errorf("muted = %q, want faint prefix", th.Muted)
	}
	// Thinking is faint + italic + color.
	if !strings.HasPrefix(th.Thinking, "\033[2;3;") {
		t.Errorf("thinking = %q, want faint+italic prefix", th.Thinking)
	}
}

func TestAsciiProfileDisablesColor(t *testing.T) {
	orig := Current().Name
	t.Cleanup(func() { SetColorProfile(termenv.EnvColorProfile()); Set(orig) })

	SetColorProfile(termenv.Ascii)
	th, _ := Get("dracula")
	for name, s := range map[string]string{
		"accent": th.Accent, "success": th.Success, "error": th.Error,
		"warning": th.Warning, "muted": th.Muted, "thinking": th.Thinking, "reset": th.Reset,
	} {
		if s != "" {
			t.Errorf("%s = %q, want empty under Ascii profile", name, s)
		}
	}
	if got := th.Paint(th.Error, "boom"); got != "boom" {
		t.Errorf("Paint under Ascii = %q, want plain text", got)
	}
}

func TestMonochromeIsAttributesOnly(t *testing.T) {
	orig := Current().Name
	t.Cleanup(func() { SetColorProfile(termenv.EnvColorProfile()); Set(orig) })

	SetColorProfile(termenv.TrueColor)
	th, _ := Get("monochrome")
	if th.Accent != "" || th.Success != "" {
		t.Errorf("monochrome should have no colored roles: accent=%q success=%q", th.Accent, th.Success)
	}
	if th.Muted != "\033[2m" {
		t.Errorf("monochrome muted = %q, want faint-only", th.Muted)
	}
	if th.Thinking != "\033[2;3m" {
		t.Errorf("monochrome thinking = %q, want faint+italic only", th.Thinking)
	}
}

func TestPaintWraps(t *testing.T) {
	orig := Current().Name
	t.Cleanup(func() { SetColorProfile(termenv.EnvColorProfile()); Set(orig) })

	SetColorProfile(termenv.ANSI)
	th, _ := Get("dark")
	got := th.Paint(th.Accent, "❯")
	if !strings.HasPrefix(got, th.Accent) || !strings.HasSuffix(got, th.Reset) || !strings.Contains(got, "❯") {
		t.Errorf("Paint = %q, want accent+text+reset", got)
	}
}
