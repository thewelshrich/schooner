package theme

import (
	"image/color"
	"strings"
	"testing"
)

func TestFormPaletteSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mode         Mode
		detectedDark bool
		want         color.RGBA
	}{
		{name: "auto ignores light detection", mode: Auto, want: color.RGBA{R: 0x00, G: 0x80, B: 0x80, A: 0xff}},
		{name: "auto ignores dark detection", mode: Auto, detectedDark: true, want: color.RGBA{R: 0x00, G: 0x80, B: 0x80, A: 0xff}},
		{name: "force light", mode: Light, detectedDark: true, want: color.RGBA{R: 0x00, G: 0x83, B: 0x77, A: 0xff}},
		{name: "force dark", mode: Dark, want: color.RGBA{R: 0x00, G: 0xb8, B: 0xa6, A: 0xff}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			styles := New(tt.mode, true).Form().Theme(tt.detectedDark)
			assertColor(t, styles.Focused.Title.GetForeground(), tt.want)
		})
	}
}

func TestAutomaticThemeUsesTerminalForeground(t *testing.T) {
	t.Parallel()

	styles := New(Auto, true).Form().Theme(false)
	if rendered := styles.Focused.Option.Render("option"); rendered != "option" {
		t.Errorf("ordinary option overrides terminal foreground: %q", rendered)
	}
	if rendered := styles.Focused.Description.Render("description"); !strings.Contains(rendered, "\x1b[2m") {
		t.Errorf("description = %q, want terminal faint styling", rendered)
	}
}

func TestSpinnerUsesSameSemanticPalette(t *testing.T) {
	t.Parallel()

	appearance := New(Dark, true)
	styles := appearance.Spinner().Theme(false)
	assertColor(t, styles.Spinner.GetForeground(), color.RGBA{R: 0x00, G: 0xb8, B: 0xa6, A: 0xff})
	assertColor(t, appearance.Style(Success).GetForeground(), color.RGBA{R: 0x46, G: 0xc0, B: 0x6e, A: 0xff})
}

func TestAutomaticSpinnerUsesTerminalCyan(t *testing.T) {
	t.Parallel()

	appearance := New(Auto, true)
	styles := appearance.Spinner().Theme(false)
	assertColor(t, styles.Spinner.GetForeground(), color.RGBA{R: 0x00, G: 0x80, B: 0x80, A: 0xff})
	assertColor(t, appearance.Style(Success).GetForeground(), color.RGBA{R: 0x00, G: 0x80, B: 0x00, A: 0xff})
}

func TestNoColorThemeEmitsNoANSI(t *testing.T) {
	t.Parallel()

	appearance := New(Dark, false)
	form := appearance.Form().Theme(true)
	spinner := appearance.Spinner().Theme(true)
	rendered := strings.Join([]string{
		form.Focused.Title.Render("Title"),
		form.Focused.FocusedButton.Render("Continue"),
		form.Focused.ErrorMessage.Render("Error"),
		spinner.Spinner.Render("..."),
		appearance.Style(Error).Render("x"),
	}, "")
	if strings.Contains(rendered, "\x1b[") {
		t.Fatalf("no-color output contains ANSI escapes: %q", rendered)
	}
}

func assertColor(t *testing.T, got color.Color, want color.RGBA) {
	t.Helper()
	r, g, b, a := got.RGBA()
	actual := color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
	if actual != want {
		t.Errorf("color = %#v, want %#v", actual, want)
	}
}
