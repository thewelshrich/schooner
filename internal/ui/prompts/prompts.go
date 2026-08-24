// Package prompts contains Schooner's Huh-backed interactive forms.
package prompts

import (
	"context"
	"errors"
	"fmt"
	"io"

	"charm.land/huh/v2"
	"github.com/thewelshrich/schooner/internal/box"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

var ErrAborted = errors.New("interactive prompt aborted")

type AddDraft struct {
	Name           string
	SSHDestination string
	ProjectRoot    string
}

type Options struct {
	Input      io.Reader
	Output     io.Writer
	Accessible bool
	Theme      *uitheme.Theme
}

func Add(ctx context.Context, options Options, draft AddDraft, nameSet, sshSet, rootSet, skipConfirm bool) (AddDraft, bool, error) {
	if draft.ProjectRoot == "" {
		draft.ProjectRoot = box.DefaultProjectRoot
	}
	method := "existing-ssh"
	var groups []*huh.Group
	if !nameSet {
		groups = append(groups, huh.NewGroup(huh.NewInput().Title("Name the box").Description("Lowercase letters, numbers, and hyphens").Value(&draft.Name).Validate(box.ValidateName)).Title("Box"))
	}
	groups = append(groups, huh.NewGroup(huh.NewSelect[string]().Title("How should Schooner acquire this box?").Options(huh.NewOption("Existing SSH", "existing-ssh").Selected(true)).Value(&method)).Title("Acquisition"))
	var details []huh.Field
	if !sshSet {
		details = append(details, huh.NewInput().Title("SSH destination").Description("An SSH config alias or user@host").Value(&draft.SSHDestination).Validate(box.ValidateSSHDestination))
	}
	if !rootSet {
		details = append(details, huh.NewInput().Title("Remote project root").Value(&draft.ProjectRoot).Validate(box.ValidateProjectRoot))
	}
	if len(details) > 0 {
		groups = append(groups, huh.NewGroup(details...).Title("SSH details"))
	}
	if err := run(ctx, options, huh.NewForm(groups...).WithLayout(huh.LayoutStack)); err != nil {
		return AddDraft{}, false, err
	}
	if skipConfirm {
		return draft, true, nil
	}

	_, _ = fmt.Fprintf(options.Output, "\nReview\n  Name:         %s\n  Acquisition:  Existing SSH\n  Destination:  %s\n  Project root: %s\n\nSchooner will verify the host, establish identity, install missing Git/tmux prerequisites, and prepare the project root.\n\n", draft.Name, draft.SSHDestination, draft.ProjectRoot)
	confirmed := false
	confirmation := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Continue?").Affirmative("Yes").Negative("No").Value(&confirmed)))
	if err := run(ctx, options, confirmation); err != nil {
		return AddDraft{}, false, err
	}
	return draft, confirmed, nil
}

func PickBox(ctx context.Context, options Options, title string, boxes []box.Record) (string, error) {
	if len(boxes) == 0 {
		return "", box.NewError("not_found", "no boxes are registered", nil)
	}
	items := make([]huh.Option[string], 0, len(boxes))
	for _, record := range boxes {
		items = append(items, huh.NewOption(record.Name+"  "+record.SSHDestination, record.Name))
	}
	value := boxes[0].Name
	form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(title).Options(items...).Value(&value)))
	if err := run(ctx, options, form); err != nil {
		return "", err
	}
	return value, nil
}

func ConfirmRemove(ctx context.Context, options Options, record box.Record) (bool, error) {
	_, _ = fmt.Fprintf(options.Output, "\nRemove %s from Schooner?\n  SSH destination: %s\n  The remote machine and its Schooner identity will remain unchanged.\n\n", record.Name, record.SSHDestination)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Remove this box?").Affirmative("Remove").Negative("Keep it").Value(&confirmed)))
	if err := run(ctx, options, form); err != nil {
		return false, err
	}
	return confirmed, nil
}

func run(ctx context.Context, options Options, form *huh.Form) error {
	if options.Theme != nil {
		form = form.WithTheme(options.Theme.Form())
	}
	err := form.WithInput(options.Input).WithOutput(options.Output).WithAccessible(options.Accessible).RunWithContext(ctx)
	if errors.Is(err, huh.ErrUserAborted) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return ErrAborted
	}
	return err
}
