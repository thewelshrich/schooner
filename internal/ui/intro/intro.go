// Package intro renders Schooner's interactive command heading.
package intro

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

const (
	word          = "schooner"
	settledWave   = "▁▂▄▆▆▄▂▁"
	frameDuration = 80 * time.Millisecond
)

// Options controls the interactive heading presentation.
type Options struct {
	Output   io.Writer
	Theme    *uitheme.Theme
	Animated bool
	Color    bool
}

// Show renders the Schooner heading once before an interactive workflow.
func Show(ctx context.Context, options Options) error {
	return show(ctx, options, frameDuration)
}

func show(ctx context.Context, options Options, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.Output == nil {
		return fmt.Errorf("render intro: output is required")
	}
	if options.Theme == nil {
		return fmt.Errorf("render intro: theme is required")
	}
	if !options.Animated {
		_, err := io.WriteString(options.Output, "\n"+settled(options)+"\n\n")
		return err
	}

	if _, err := io.WriteString(options.Output, "\n"+ansi.HideCursor); err != nil {
		return err
	}
	cursorHidden := true
	defer func() {
		if cursorHidden {
			_, _ = io.WriteString(options.Output, ansi.ShowCursor+"\n")
		}
	}()

	for frame := 0; frame <= len(word); frame++ {
		if frame > 0 {
			if _, err := io.WriteString(options.Output, ansi.CursorUp(2)); err != nil {
				return err
			}
		}
		value := settled(options)
		if frame < len(word) {
			value = scanning(options, frame)
		}
		if _, err := io.WriteString(options.Output, "\r"+strings.Replace(value, "\n", ansi.EraseLineRight+"\n", 1)+ansi.EraseLineRight+"\n"); err != nil {
			return err
		}
		if frame == len(word) {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}

	if _, err := io.WriteString(options.Output, ansi.ShowCursor+"\n"); err != nil {
		return err
	}
	cursorHidden = false
	return nil
}

func settled(options Options) string {
	if !options.Color {
		return word + "\n" + settledWave
	}
	primary := options.Theme.Style(uitheme.Primary)
	return primary.Bold(true).Render(word) + "\n" + primary.Faint(true).Render(settledWave)
}

func scanning(options Options, center int) string {
	primary := options.Theme.Style(uitheme.Primary)
	var heading, wave strings.Builder
	for index, letter := range word {
		value := string(letter)
		if index == center || index == center-1 {
			value = primary.Bold(true).Render(value)
		}
		heading.WriteString(value)

		distance := index - center
		if distance < 0 {
			distance = -distance
		}
		glyph := "▁"
		switch distance {
		case 0:
			glyph = "▆"
		case 1:
			glyph = "▄"
		case 2:
			glyph = "▂"
		}
		if distance <= 1 {
			glyph = primary.Render(glyph)
		} else {
			glyph = primary.Faint(true).Render(glyph)
		}
		wave.WriteString(glyph)
	}
	return heading.String() + "\n" + wave.String()
}
