// Package spinner draws a lightweight loading line without Bubble Tea.
// Huh's spinner probes terminal background and mode support; those replies
// leak into the shell when a spinner is cancelled between short progress steps.
package spinner

import (
	"context"
	"fmt"
	"io"
	"strings"
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

// While runs action while animating title. When the action finishes, the
// spinner is replaced by a persistent success or failure mark so earlier
// steps remain visible.
func While(ctx context.Context, w io.Writer, theme *uitheme.Theme, title string, animated bool, action func(context.Context) error) error {
	if action == nil {
		return nil
	}
	if !animated {
		writePending(w, theme, title)
		err := action(ctx)
		writeResult(w, theme, title, err)
		return err
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
	writeResult(w, theme, title, err)
	return err
}

func writePending(w io.Writer, theme *uitheme.Theme, title string) {
	if w == nil {
		return
	}

	mark := "…"
	label := statusLabel(title)
	if theme != nil && theme.HasColor() {
		mark = theme.Style(uitheme.Primary).Render(mark)
		label = theme.Style(uitheme.Text).Render(label)
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", mark, label)
}

func writeResult(w io.Writer, theme *uitheme.Theme, title string, err error) {
	if w == nil {
		return
	}
	label := statusLabel(title)
	mark := "✓"
	role := uitheme.Success
	if err != nil {
		mark = "✗"
		role = uitheme.Error
	}
	if theme != nil && theme.HasColor() {
		mark = theme.Style(role).Bold(true).Render(mark)
		label = theme.Style(uitheme.Text).Render(label)
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", mark, label)
}

func statusLabel(title string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(title, "…"), "..."))
}
