// Package spinner draws a lightweight loading line without Bubble Tea.
// Huh's spinner probes terminal background and mode support; those replies
// leak into the shell when a spinner is cancelled between short progress steps.
package spinner

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/x/ansi"

	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Run animates title on writer until ctx is cancelled, then clears the line.
func Run(ctx context.Context, w io.Writer, theme *uitheme.Theme, title string) {
	if w == nil {
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	_, _ = io.WriteString(w, ansi.HideCursor)
	defer func() {
		_, _ = fmt.Fprintf(w, "\r%s", ansi.EraseLineRight)
		_, _ = io.WriteString(w, ansi.ShowCursor)
	}()
	draw := func(frame string) {
		if theme != nil && theme.HasColor() {
			frame = theme.Style(uitheme.Primary).Render(frame)
		}
		_, _ = fmt.Fprintf(w, "\r%s %s%s", frame, title, ansi.EraseLineRight)
	}
	i := 0
	draw(frames[0])
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			i++
			draw(frames[i%len(frames)])
		}
	}
}

// While runs action while animating title. When animation is off it prints a
// static placeholder line first.
func While(ctx context.Context, w io.Writer, theme *uitheme.Theme, title string, animated bool, action func(context.Context) error) error {
	if action == nil {
		return nil
	}
	if !animated {
		if w != nil {
			_, _ = fmt.Fprintf(w, "… %s\n", title)
		}
		return action(ctx)
	}
	spinCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(spinCtx, w, theme, title)
	}()
	err := action(ctx)
	cancel()
	<-done
	return err
}
