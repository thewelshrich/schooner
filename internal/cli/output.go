package cli

import (
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"

	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

type summaryRow struct {
	Label, Value string
	Emphasize    bool
}

func writeReadySummary(w io.Writer, theme *uitheme.Theme, title string, rows []summaryRow) error {
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	mark, heading := "✓", title
	if theme != nil && theme.HasColor() {
		mark = theme.Style(uitheme.Success).Bold(true).Render("✓")
		heading = theme.Style(uitheme.Text).Bold(true).Render(title)
	}
	if _, err := fmt.Fprintf(w, "%s %s\n", mark, heading); err != nil {
		return err
	}
	return writeSummaryRows(w, theme, rows)
}

func writeActionSummary(w io.Writer, theme *uitheme.Theme, title string, rows []summaryRow) error {
	return writeNamedSummary(w, theme, title, rows, false)
}

func writeFeaturedSummary(w io.Writer, theme *uitheme.Theme, title string, rows []summaryRow) error {
	return writeNamedSummary(w, theme, title, rows, true)
}

func writeNamedSummary(w io.Writer, theme *uitheme.Theme, title string, rows []summaryRow, featured bool) error {
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	heading := title
	if theme != nil && theme.HasColor() {
		style := theme.Style(uitheme.Text).Bold(true)
		if featured {
			style = theme.Style(uitheme.Primary).Bold(true)
		}
		heading = style.Render(title)
	}
	if _, err := fmt.Fprintln(w, heading); err != nil {
		return err
	}
	return writeSummaryRows(w, theme, rows)
}

func writeSummaryRows(w io.Writer, theme *uitheme.Theme, rows []summaryRow) error {
	if len(rows) == 0 {
		return nil
	}
	width := 0
	for _, row := range rows {
		width = max(width, len(row.Label))
	}
	for _, row := range rows {
		label := fmt.Sprintf("%-*s", width, row.Label)
		value := row.Value
		if theme != nil && theme.HasColor() {
			label = theme.Style(uitheme.Muted).Render(label)
			if row.Emphasize {
				value = theme.Style(uitheme.Primary).Bold(true).Render(value)
			} else {
				value = theme.Style(uitheme.Text).Render(value)
			}
		}
		if _, err := fmt.Fprintf(w, "  %s  %s\n", label, value); err != nil {
			return err
		}
	}
	return nil
}

func writeExplanation(w io.Writer, theme *uitheme.Theme, message string) error {
	if message == "" {
		return nil
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	body := lipgloss.Wrap(message, 72, "")
	if theme != nil && theme.HasColor() {
		body = theme.Style(uitheme.Muted).Render(body)
	}
	if _, err := fmt.Fprintln(w, body); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

func writeMutedNotice(w io.Writer, theme *uitheme.Theme, message string) error {
	line := message
	if theme != nil && theme.HasColor() {
		line = theme.Style(uitheme.Muted).Render(message)
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

func writeCancelled(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Cancelled. No changes made.")
}

func writeWarningLine(w io.Writer, theme *uitheme.Theme, message string) error {
	prefix, body := "Warning:", message
	if theme != nil && theme.HasColor() {
		prefix = theme.Style(uitheme.Warning).Render(prefix)
		body = theme.Style(uitheme.Text).Render(message)
	}
	_, err := fmt.Fprintf(w, "%s %s\n", prefix, body)
	return err
}

func writeTable(w io.Writer, theme *uitheme.Theme, headers []string, rows [][]string) error {
	return writeTableWithRoles(w, theme, headers, rows, nil)
}

func writeTableWithRoles(w io.Writer, theme *uitheme.Theme, headers []string, rows [][]string, roles [][]*uitheme.Role) error {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	writeCell := func(value string, width int, header bool, role *uitheme.Role) string {
		cell := fmt.Sprintf("%-*s", width, value)
		if theme != nil && theme.HasColor() {
			if header {
				return theme.Style(uitheme.Muted).Render(cell)
			}
			if role != nil {
				return theme.Style(*role).Render(cell)
			}
			return theme.Style(uitheme.Text).Render(cell)
		}
		return cell
	}
	line := make([]string, len(headers))
	for i, header := range headers {
		line[i] = writeCell(header, widths[i], true, nil)
	}
	if _, err := fmt.Fprintln(w, strings.Join(line, "  ")); err != nil {
		return err
	}
	for rowIndex, row := range rows {
		for i, cell := range row {
			var role *uitheme.Role
			if roles != nil && rowIndex < len(roles) && i < len(roles[rowIndex]) {
				role = roles[rowIndex][i]
			}
			line[i] = writeCell(cell, widths[i], false, role)
		}
		if _, err := fmt.Fprintln(w, strings.Join(line, "  ")); err != nil {
			return err
		}
	}
	return nil
}

func yesNoValue(value bool) string {
	if value {
		return "Yes"
	}
	return "No"
}
