package cli

import (
	"context"
	"fmt"
	"io"
	"sync"

	"charm.land/huh/v2/spinner"
	"github.com/thewelshrich/schooner/internal/box"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

type progressRenderer struct {
	ctx      context.Context
	writer   io.Writer
	animated bool
	theme    *uitheme.Theme
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
}

func newProgressRenderer(ctx context.Context, writer io.Writer, animated bool, theme *uitheme.Theme) *progressRenderer {
	return &progressRenderer{ctx: ctx, writer: writer, animated: animated, theme: theme}
}

func (r *progressRenderer) Event(event box.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch event.State {
	case box.EventStarted:
		if !r.animated {
			_, _ = fmt.Fprintf(r.writer, "… %s\n", event.Message)
			return
		}
		stepContext, cancel := context.WithCancel(r.ctx)
		r.cancel = cancel
		r.done = make(chan struct{})
		go func(done chan struct{}) {
			defer close(done)
			_ = spinner.New().Type(spinner.Dots).Title(" " + event.Message).WithTheme(r.theme.Spinner()).WithOutput(r.writer).Context(stepContext).Run()
		}(r.done)
	case box.EventCompleted, box.EventFailed:
		r.stopLocked()
		symbol := "✓"
		if event.State == box.EventFailed {
			symbol = "✗"
		}
		role := uitheme.Success
		if event.State == box.EventFailed {
			role = uitheme.Error
		}
		symbol = r.theme.Style(role).Render(symbol)
		_, _ = fmt.Fprintf(r.writer, "%s %s\n", symbol, event.Message)
	}
}

func (r *progressRenderer) Close() { r.mu.Lock(); defer r.mu.Unlock(); r.stopLocked() }

func (r *progressRenderer) stopLocked() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.done != nil {
		<-r.done
		r.done = nil
	}
}
