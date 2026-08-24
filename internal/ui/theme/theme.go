// Package theme owns Schooner's terminal appearance.
package theme

import (
	"image/color"
	"sync/atomic"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"charm.land/lipgloss/v2"
)

// Mode controls how the light or dark palette is selected.
type Mode string

const (
	Auto  Mode = "auto"
	Light Mode = "light"
	Dark  Mode = "dark"
)

// Role identifies a semantic terminal colour.
type Role uint8

const (
	Primary Role = iota
	Text
	Muted
	Success
	Warning
	Error
	Offline
)

// Theme adapts Schooner's semantic palette to Huh and ordinary CLI output.
// Automatic mode uses the terminal's own foreground and ANSI palette, so it
// does not depend on terminal background detection.
type Theme struct {
	mode   Mode
	color  bool
	isDark atomic.Bool
}

// New returns a terminal theme.
func New(mode Mode, color bool) *Theme {
	t := &Theme{mode: mode, color: color}
	t.isDark.Store(mode != Light)
	return t
}

// HasColor reports whether this theme may emit ANSI styling.
func (t *Theme) HasColor() bool {
	return t != nil && t.color
}

// Form returns the Huh form theme for this terminal appearance.
func (t *Theme) Form() huh.Theme {
	return huh.ThemeFunc(func(detectedDark bool) *huh.Styles {
		if !t.color {
			return plainForm(false)
		}
		if t.mode == Auto {
			return terminalForm()
		}
		return coloredForm(t.resolve(detectedDark))
	})
}

// Spinner returns the Huh spinner theme for this terminal appearance.
func (t *Theme) Spinner() spinner.Theme {
	return spinner.ThemeFunc(func(detectedDark bool) *spinner.Styles {
		if !t.color {
			return &spinner.Styles{}
		}
		if t.mode == Auto {
			return &spinner.Styles{Spinner: lipgloss.NewStyle().Foreground(lipgloss.Color("6"))}
		}
		p := paletteFor(t.resolve(detectedDark))
		return &spinner.Styles{
			Spinner: lipgloss.NewStyle().Foreground(p.primary),
			Title:   lipgloss.NewStyle().Foreground(p.text),
		}
	})
}

// Style returns a style for non-Huh semantic output.
func (t *Theme) Style(role Role) lipgloss.Style {
	if !t.color {
		return lipgloss.NewStyle()
	}
	if t.mode == Auto {
		return terminalStyle(role)
	}
	p := paletteFor(t.isDark.Load())
	return lipgloss.NewStyle().Foreground(p.color(role))
}

func (t *Theme) resolve(detectedDark bool) bool {
	dark := detectedDark
	switch t.mode {
	case Light:
		dark = false
	case Dark:
		dark = true
	}
	t.isDark.Store(dark)
	return dark
}

func terminalStyle(role Role) lipgloss.Style {
	switch role {
	case Primary:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	case Success:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	case Warning:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	case Error:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	case Muted, Offline:
		return lipgloss.NewStyle().Faint(true)
	default:
		return lipgloss.NewStyle()
	}
}

type palette struct {
	primary, primaryForeground color.Color
	text, muted                color.Color
	success, warning, danger   color.Color
	offline                    color.Color
}

func paletteFor(dark bool) palette {
	if dark {
		return palette{
			primary:           lipgloss.Color("#00B8A6"),
			primaryForeground: lipgloss.Color("#010E0C"),
			text:              lipgloss.Color("#E2E5EA"),
			muted:             lipgloss.Color("#8E929B"),
			success:           lipgloss.Color("#46C06E"),
			warning:           lipgloss.Color("#EAB02F"),
			danger:            lipgloss.Color("#FB605C"),
			offline:           lipgloss.Color("#7A808D"),
		}
	}
	return palette{
		primary:           lipgloss.Color("#008377"),
		primaryForeground: lipgloss.Color("#F5FEFD"),
		text:              lipgloss.Color("#11141A"),
		muted:             lipgloss.Color("#646972"),
		success:           lipgloss.Color("#2EA55C"),
		warning:           lipgloss.Color("#AE6800"),
		danger:            lipgloss.Color("#BB0916"),
		offline:           lipgloss.Color("#737B8A"),
	}
}

func (p palette) color(role Role) color.Color {
	switch role {
	case Primary:
		return p.primary
	case Muted:
		return p.muted
	case Success:
		return p.success
	case Warning:
		return p.warning
	case Error:
		return p.danger
	case Offline:
		return p.offline
	default:
		return p.text
	}
}

func coloredForm(dark bool) *huh.Styles {
	t := huh.ThemeBase(dark)
	p := paletteFor(dark)

	t.Focused.Base = t.Focused.Base.BorderForeground(p.primary)
	t.Focused.Card = t.Focused.Base
	t.Focused.Title = lipgloss.NewStyle().Foreground(p.primary).Bold(true)
	t.Focused.NoteTitle = t.Focused.Title
	t.Focused.Description = lipgloss.NewStyle().Foreground(p.muted)
	t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(p.danger)
	t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(p.danger)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(p.primary)
	t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(p.primary)
	t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(p.primary)
	// Leave option text uncolored so catalog rows can keep split label/meta styling.
	t.Focused.Option = lipgloss.NewStyle()
	t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(p.primary)
	t.Focused.SelectedOption = lipgloss.NewStyle().Bold(true)
	t.Focused.SelectedPrefix = lipgloss.NewStyle().Foreground(p.success).SetString("✓ ")
	t.Focused.UnselectedOption = lipgloss.NewStyle()
	t.Focused.UnselectedPrefix = lipgloss.NewStyle().Foreground(p.muted).SetString("• ")
	t.Focused.FocusedButton = lipgloss.NewStyle().Padding(0, 2).Foreground(p.primaryForeground).Background(p.primary).Bold(true)
	t.Focused.BlurredButton = lipgloss.NewStyle().Padding(0, 2).Foreground(p.muted)
	t.Focused.Next = t.Focused.FocusedButton
	t.Focused.TextInput.Cursor = lipgloss.NewStyle().Foreground(p.primary)
	t.Focused.TextInput.CursorText = lipgloss.NewStyle().Foreground(p.primaryForeground).Background(p.primary)
	t.Focused.TextInput.Placeholder = lipgloss.NewStyle().Foreground(p.muted)
	t.Focused.TextInput.Prompt = lipgloss.NewStyle().Foreground(p.primary)
	t.Focused.TextInput.Text = lipgloss.NewStyle().Foreground(p.text)

	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.Title = lipgloss.NewStyle().Foreground(p.text).Bold(true)
	t.Blurred.NoteTitle = t.Blurred.Title
	t.Blurred.SelectSelector = lipgloss.NewStyle().SetString("  ")
	t.Blurred.MultiSelectSelector = lipgloss.NewStyle().SetString("  ")
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()
	t.Blurred.TextInput.Prompt = lipgloss.NewStyle().Foreground(p.muted)

	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description
	t.Help.ShortKey = lipgloss.NewStyle().Foreground(p.primary)
	t.Help.FullKey = t.Help.ShortKey
	t.Help.ShortDesc = lipgloss.NewStyle().Foreground(p.muted)
	t.Help.FullDesc = t.Help.ShortDesc
	t.Help.ShortSeparator = lipgloss.NewStyle().Foreground(p.muted)
	t.Help.FullSeparator = t.Help.ShortSeparator
	t.Help.Ellipsis = t.Help.ShortSeparator
	return t
}

func terminalForm() *huh.Styles {
	t := huh.ThemeBase(false)
	primary := terminalStyle(Primary)
	muted := terminalStyle(Muted)
	success := terminalStyle(Success)

	t.Focused.Base = t.Focused.Base.BorderForeground(lipgloss.Color("6"))
	t.Focused.Card = t.Focused.Base
	t.Focused.Title = primary.Bold(true)
	t.Focused.NoteTitle = t.Focused.Title
	t.Focused.Description = muted
	t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(lipgloss.Color("1"))
	t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(lipgloss.Color("1"))
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(lipgloss.Color("6"))
	t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(lipgloss.Color("6"))
	t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(lipgloss.Color("6"))
	t.Focused.Option = lipgloss.NewStyle()
	t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(lipgloss.Color("6"))
	t.Focused.SelectedOption = lipgloss.NewStyle().Bold(true)
	t.Focused.SelectedPrefix = success.SetString("✓ ")
	t.Focused.UnselectedOption = lipgloss.NewStyle()
	t.Focused.UnselectedPrefix = muted.SetString("• ")
	t.Focused.FocusedButton = lipgloss.NewStyle().Padding(0, 2).Reverse(true).Bold(true)
	t.Focused.BlurredButton = lipgloss.NewStyle().Padding(0, 2)
	t.Focused.Next = t.Focused.FocusedButton
	t.Focused.TextInput.Cursor = primary
	t.Focused.TextInput.CursorText = lipgloss.NewStyle().Reverse(true)
	t.Focused.TextInput.Placeholder = muted
	t.Focused.TextInput.Prompt = primary
	t.Focused.TextInput.Text = lipgloss.NewStyle()

	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.Title = lipgloss.NewStyle().Bold(true)
	t.Blurred.NoteTitle = t.Blurred.Title
	t.Blurred.SelectSelector = lipgloss.NewStyle().SetString("  ")
	t.Blurred.MultiSelectSelector = lipgloss.NewStyle().SetString("  ")
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()
	t.Blurred.TextInput.Prompt = muted

	t.Group.Title = t.Focused.Title
	t.Group.Description = muted
	t.Help.ShortKey = primary
	t.Help.FullKey = primary
	t.Help.ShortDesc = muted
	t.Help.FullDesc = muted
	t.Help.ShortSeparator = muted
	t.Help.FullSeparator = muted
	t.Help.Ellipsis = muted
	return t
}

func plainForm(dark bool) *huh.Styles {
	t := huh.ThemeBase(dark)
	button := lipgloss.NewStyle().Padding(0, 2)
	t.Focused.FocusedButton = button
	t.Focused.BlurredButton = button
	t.Focused.TextInput.Placeholder = lipgloss.NewStyle()
	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.SelectSelector = lipgloss.NewStyle().SetString("  ")
	t.Blurred.MultiSelectSelector = lipgloss.NewStyle().SetString("  ")
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()
	t.Help.ShortKey = lipgloss.NewStyle()
	t.Help.FullKey = lipgloss.NewStyle()
	t.Help.ShortDesc = lipgloss.NewStyle()
	t.Help.FullDesc = lipgloss.NewStyle()
	t.Help.ShortSeparator = lipgloss.NewStyle()
	t.Help.FullSeparator = lipgloss.NewStyle()
	t.Help.Ellipsis = lipgloss.NewStyle()
	return t
}
