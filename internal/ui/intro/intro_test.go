package intro

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

func TestStaticPlainIntro(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := Show(t.Context(), Options{Output: &output, Theme: uitheme.New(uitheme.Auto, false)})
	if err != nil {
		t.Fatal(err)
	}
	if want := "\nschooner\n▁▂▄▆▆▄▂▁\n\n"; output.String() != want {
		t.Errorf("output = %q, want %q", output.String(), want)
	}
}

func TestAnimatedIntroSettlesAndRestoresCursor(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := show(t.Context(), Options{Output: &output, Theme: uitheme.New(uitheme.Auto, true), Animated: true, Color: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	value := output.String()
	if strings.Count(value, ansi.HideCursor) != 1 || strings.Count(value, ansi.ShowCursor) != 1 {
		t.Errorf("cursor control is not balanced: %q", value)
	}
	if got := strings.Count(value, ansi.CursorUp(2)); got != len(word) {
		t.Errorf("cursor rewrites = %d, want %d", got, len(word))
	}
	if !strings.Contains(value, "schooner") || !strings.Contains(value, settledWave) {
		t.Errorf("settled mark missing from output: %q", value)
	}
	for _, sequence := range []string{ansi.EraseScreenBelow, ansi.EraseScreenAbove, ansi.EraseEntireScreen, ansi.EraseEntireDisplay} {
		if strings.Contains(value, sequence) {
			t.Errorf("intro clears terminal content with %q", sequence)
		}
	}
}

func TestAnimatedIntroRestoresCursorWhenCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	output := &cancelingWriter{cancel: cancel}
	err := show(ctx, Options{Output: output, Theme: uitheme.New(uitheme.Auto, true), Animated: true, Color: true}, frameDuration)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if !strings.Contains(output.String(), ansi.HideCursor) || !strings.Contains(output.String(), ansi.ShowCursor) {
		t.Errorf("cursor was not restored: %q", output.String())
	}
}

type cancelingWriter struct {
	buffer bytes.Buffer
	cancel context.CancelFunc
	writes int
}

func (w *cancelingWriter) Write(value []byte) (int, error) {
	n, err := w.buffer.Write(value)
	w.writes++
	if w.writes == 2 {
		w.cancel()
	}
	return n, err
}

func (w *cancelingWriter) String() string { return w.buffer.String() }
