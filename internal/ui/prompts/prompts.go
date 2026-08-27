// Package prompts contains Schooner's Huh-backed interactive forms.
package prompts

import (
	"context"
	"errors"
	"fmt"
	"io"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/credentials"
	"github.com/thewelshrich/schooner/internal/provider"
	"github.com/thewelshrich/schooner/internal/ui/intro"
	"github.com/thewelshrich/schooner/internal/ui/spinner"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

var ErrAborted = errors.New("interactive prompt aborted")

// catalogSelectHeight bounds long DigitalOcean lists (title + ~8 options).
const catalogSelectHeight = 10

type AddDraft struct {
	Name           string
	SSHDestination string
	WorktreeRoot   string
}

type Options struct {
	Input      io.Reader
	Output     io.Writer
	Accessible bool
	Theme      *uitheme.Theme
	Summary    *ChoiceSummary
}

type Choice struct {
	Label string
	Value string
}

type ChoiceSummary struct {
	rows  []Choice
	index map[string]int
}

func NewChoiceSummary() *ChoiceSummary {
	return &ChoiceSummary{index: map[string]int{}}
}

func Add(ctx context.Context, options Options, draft AddDraft, nameSet, sshSet, rootSet, skipConfirm bool) (AddDraft, bool, error) {
	if draft.WorktreeRoot == "" {
		draft.WorktreeRoot = box.DefaultWorktreeRoot
	}
	var fields []huh.Field
	if !nameSet {
		fields = append(fields, huh.NewInput().Title("Name the box").Description("Lowercase letters, numbers, and hyphens").Value(&draft.Name).Validate(box.ValidateName))
	}
	if !sshSet {
		fields = append(fields, huh.NewInput().Title("SSH destination").Description("An SSH config alias or user@host").Value(&draft.SSHDestination).Validate(box.ValidateSSHDestination))
	}
	if !rootSet {
		fields = append(fields, huh.NewInput().Title("Remote worktree root").Value(&draft.WorktreeRoot).Validate(box.ValidateWorktreeRoot))
	}
	section(options, "Box details")
	if len(fields) > 0 {
		if err := run(ctx, options, huh.NewForm(huh.NewGroup(fields...))); err != nil {
			return AddDraft{}, false, err
		}
	}
	RecordChoices(options,
		Choice{Label: "Name", Value: draft.Name},
		Choice{Label: "SSH destination", Value: draft.SSHDestination},
		Choice{Label: "Worktree root", Value: draft.WorktreeRoot},
	)
	if skipConfirm {
		return draft, true, nil
	}

	if err := beginReview(ctx, options); err != nil {
		return AddDraft{}, false, err
	}
	renderKeyValues(options,
		Choice{Label: "Name", Value: draft.Name},
		Choice{Label: "Acquisition", Value: "Existing SSH"},
		Choice{Label: "SSH destination", Value: draft.SSHDestination},
		Choice{Label: "Worktree root", Value: draft.WorktreeRoot},
	)
	_, _ = fmt.Fprintf(options.Output, "\nSchooner will verify the host, establish identity, install missing Git/tmux prerequisites, and prepare the worktree root.\n\n")
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
		items = append(items, huh.NewOption(catalogOption(options, record.Name, record.SSHDestination), record.Name))
	}
	value := boxes[0].Name
	form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(title).Options(items...).Value(&value)))
	if err := run(ctx, options, form); err != nil {
		return "", err
	}
	return value, nil
}

func Pick(ctx context.Context, options Options, title string, choices []Choice) (string, error) {
	if len(choices) == 0 {
		return "", box.NewError("not_found", "no choices are available", nil)
	}
	items := make([]huh.Option[string], 0, len(choices))
	for _, choice := range choices {
		items = append(items, huh.NewOption(choice.Label, choice.Value))
	}
	value := choices[0].Value
	form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(title).Options(items...).Value(&value).Height(catalogSelectHeight)))
	if err := run(ctx, options, form); err != nil {
		return "", err
	}
	return value, nil
}

func Confirm(ctx context.Context, options Options, title, affirmative, negative string) (bool, error) {
	confirmed := false
	form := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(title).Affirmative(affirmative).Negative(negative).Value(&confirmed)))
	if err := run(ctx, options, form); err != nil {
		return false, err
	}
	return confirmed, nil
}

func ConfirmRemove(ctx context.Context, options Options, record box.Record) (bool, error) {
	section(options, "Remove box")
	renderKeyValues(options,
		Choice{Label: "Name", Value: record.Name},
		Choice{Label: "SSH destination", Value: record.SSHDestination},
		Choice{Label: "Remote machine", Value: "unchanged"},
	)
	_, _ = fmt.Fprintf(options.Output, "\nThe remote machine and its Schooner identity will remain unchanged. Disconnect source access first; removal never calls GitHub or deletes Box source keys.\n\n")
	confirmed := false
	form := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Remove this box?").Affirmative("Remove").Negative("Keep it").Value(&confirmed)))
	if err := run(ctx, options, form); err != nil {
		return false, err
	}
	return confirmed, nil
}

func ChooseAcquisition(ctx context.Context, options Options) (string, error) {
	section(options, "Acquisition")
	value := "ssh"
	form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("How should Schooner acquire this box?").Options(huh.NewOption("Existing SSH", "ssh"), huh.NewOption("DigitalOcean", "digitalocean")).Value(&value)))
	if err := run(ctx, options, form); err != nil {
		return "", err
	}
	label := "Existing SSH"
	if value == "digitalocean" {
		label = "DigitalOcean"
	}
	RecordChoices(options, Choice{Label: "Acquisition", Value: label})
	return value, nil
}

func ShowAcquisition(options Options, value string) {
	label := "Existing SSH"
	if value == "digitalocean" {
		label = "DigitalOcean"
	}
	RecordChoices(options, Choice{Label: "Acquisition", Value: label})
}

func ConnectDigitalOcean(ctx context.Context, options Options, name, token string) (string, string, bool, error) {
	section(options, "DigitalOcean credential")
	fields := []huh.Field{}
	if name == "" {
		fields = append(fields, huh.NewInput().Title("Credential profile name").Description("For example: personal or work").Value(&name).Validate(credentials.ValidateProfileName))
	}
	if token == "" {
		fields = append(fields, huh.NewInput().Title("DigitalOcean personal access token").Description("Use a Full Access token beginning with dop_v1_").EchoMode(huh.EchoModePassword).Value(&token))
	}
	storeSecret := token == ""
	fields = append(fields, huh.NewConfirm().Title("Store this token in the operating-system credential store?").Affirmative("Store securely").Negative("Use once").Value(&storeSecret))
	if err := run(ctx, options, huh.NewForm(huh.NewGroup(fields...))); err != nil {
		return "", "", false, err
	}
	storage := "Use for this process only"
	if storeSecret {
		storage = "Store in OS credential store"
	}
	RecordChoices(options,
		Choice{Label: "Credential profile", Value: "digitalocean/" + name},
		Choice{Label: "Credential storage", Value: storage},
	)
	return name, token, storeSecret, nil
}

func PickCredentialProfile(ctx context.Context, options Options, profiles []credentials.Profile) (provider.CredentialProfileRef, error) {
	if len(profiles) == 0 {
		return "", box.NewError("not_found", "no DigitalOcean credential profiles are connected", nil)
	}
	items := make([]huh.Option[string], 0, len(profiles))
	value := string(profiles[0].Ref)
	for _, profile := range profiles {
		label := catalogOption(options, string(profile.Ref), firstPromptValue(profile.AccountName, profile.AccountEmail, string(profile.Status)))
		items = append(items, huh.NewOption(label, string(profile.Ref)))
		if profile.Default {
			value = string(profile.Ref)
		}
	}
	section(options, "DigitalOcean credential")
	if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("DigitalOcean credential profile").Options(items...).Value(&value)))); err != nil {
		return "", err
	}
	RecordChoices(options, Choice{Label: "Credential profile", Value: value})
	return provider.CredentialProfileRef(value), nil
}

type ProvisionDraft struct {
	Name             string
	WorktreeRoot     string
	Region           string
	Size             string
	Image            string
	NetworkID        string
	AccessKeyIDs     []string
	LocalPublicKeys  []provider.PublicKey
	AutomaticBackups bool
	IPv6             bool
}

func ProvisionBasics(ctx context.Context, options Options, draft ProvisionDraft, nameSet, rootSet bool) (ProvisionDraft, error) {
	if draft.WorktreeRoot == "" {
		draft.WorktreeRoot = box.DefaultWorktreeRoot
	}
	var fields []huh.Field
	if !nameSet {
		fields = append(fields, huh.NewInput().Title("Name the box").Description("Lowercase letters, numbers, and hyphens").Value(&draft.Name).Validate(box.ValidateName))
	}
	if !rootSet {
		fields = append(fields, huh.NewInput().Title("Remote worktree root").Value(&draft.WorktreeRoot).Validate(box.ValidateWorktreeRoot))
	}
	section(options, "Box details")
	if len(fields) > 0 {
		if err := run(ctx, options, huh.NewForm(huh.NewGroup(fields...))); err != nil {
			return ProvisionDraft{}, err
		}
	}
	RecordChoices(options, Choice{Label: "Name", Value: draft.Name}, Choice{Label: "Worktree root", Value: draft.WorktreeRoot})
	return draft, nil
}

func DigitalOceanProvision(ctx context.Context, options Options, draft ProvisionDraft, catalog provider.Catalog, localKeys []provider.PublicKey, regionSet, sizeSet, imageSet, networkSet, keysSet bool) (ProvisionDraft, error) {
	section(options, "Location")
	if !regionSet {
		items := make([]huh.Option[string], 0, len(catalog.Regions))
		for _, region := range catalog.Regions {
			items = append(items, huh.NewOption(catalogOption(options, region.Name, region.ID), region.ID))
		}
		if len(items) == 0 {
			return ProvisionDraft{}, box.NewError("unsupported", "DigitalOcean returned no available regions", nil)
		}
		if draft.Region == "" {
			draft.Region = catalog.Regions[0].ID
		}
		if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Region").Options(items...).Value(&draft.Region).Height(catalogSelectHeight)))); err != nil {
			return ProvisionDraft{}, err
		}
	}
	RecordChoices(options, Choice{Label: "Region", Value: draft.Region})

	section(options, "Droplet configuration")
	if !sizeSet {
		items := []huh.Option[string]{}
		for _, size := range catalog.Sizes {
			if containsPrompt(size.Regions, draft.Region) {
				items = append(items, huh.NewOption(sizeCatalogOption(options, size.ID, size.VCPUs, size.MemoryMB, size.Price.Monthly), size.ID))
				if draft.Size == "" {
					draft.Size = size.ID
				}
			}
		}
		if len(items) == 0 {
			return ProvisionDraft{}, box.NewError("unsupported", "DigitalOcean returned no sizes for this region", nil)
		}
		if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Size").Options(items...).Value(&draft.Size).Height(catalogSelectHeight)))); err != nil {
			return ProvisionDraft{}, err
		}
	}
	RecordChoices(options, Choice{Label: "Size", Value: draft.Size})

	if !imageSet {
		items := []huh.Option[string]{}
		for _, image := range catalog.Images {
			if len(image.Regions) == 0 || containsPrompt(image.Regions, draft.Region) {
				items = append(items, huh.NewOption(catalogOption(options, image.Name, image.ID), image.ID))
				if draft.Image == "" {
					draft.Image = image.ID
				}
			}
		}
		if len(items) == 0 {
			return ProvisionDraft{}, box.NewError("unsupported", "DigitalOcean returned no Ubuntu images for this region", nil)
		}
		if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Ubuntu image").Options(items...).Value(&draft.Image).Height(catalogSelectHeight)))); err != nil {
			return ProvisionDraft{}, err
		}
	}
	RecordChoices(options, Choice{Label: "Image", Value: draft.Image})

	if !networkSet {
		items := []huh.Option[string]{huh.NewOption("Default regional VPC", "")}
		for _, network := range catalog.Networks {
			if network.Region == draft.Region {
				items = append(items, huh.NewOption(catalogOption(options, network.Name, network.ID), network.ID))
			}
		}
		if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("VPC").Options(items...).Value(&draft.NetworkID).Height(catalogSelectHeight)))); err != nil {
			return ProvisionDraft{}, err
		}
	}
	RecordChoices(options, Choice{Label: "VPC", Value: firstPromptValue(draft.NetworkID, "default regional VPC")})

	section(options, "SSH access")
	if len(localKeys) > 0 {
		items := make([]huh.Option[provider.PublicKey], 0, len(localKeys))
		for _, key := range localKeys {
			items = append(items, huh.NewOption(catalogOption(options, key.Name, key.Fingerprint), key))
		}
		if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewMultiSelect[provider.PublicKey]().Title("Local SSH keys").Description("Public keys only; private keys never leave this machine").Options(items...).Limit(15).Value(&draft.LocalPublicKeys).Height(catalogSelectHeight)))); err != nil {
			return ProvisionDraft{}, err
		}
	}
	RecordChoices(options, Choice{Label: "Local SSH keys", Value: promptCount(len(draft.LocalPublicKeys))})

	if !keysSet && len(catalog.AccessKeys) > 0 {
		items := make([]huh.Option[string], 0, len(catalog.AccessKeys))
		for _, key := range catalog.AccessKeys {
			items = append(items, huh.NewOption(catalogOption(options, key.Name, key.Fingerprint), key.ID))
		}
		if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().Title("DigitalOcean account SSH keys").Description("Keys already registered with this DigitalOcean account").Options(items...).Limit(15).Value(&draft.AccessKeyIDs).Height(catalogSelectHeight)))); err != nil {
			return ProvisionDraft{}, err
		}
	}
	if len(draft.LocalPublicKeys)+len(draft.AccessKeyIDs) > 15 {
		return ProvisionDraft{}, box.NewError("invalid_input", "select at most 15 local and DigitalOcean account SSH keys combined", nil)
	}
	RecordChoices(options, Choice{Label: "DO account SSH keys", Value: promptCount(len(draft.AccessKeyIDs))})

	if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Enable automatic backups?").Value(&draft.AutomaticBackups)))); err != nil {
		return ProvisionDraft{}, err
	}
	RecordChoices(options, Choice{Label: "Automatic backups", Value: yesNo(draft.AutomaticBackups)})

	if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Enable IPv6?").Value(&draft.IPv6)))); err != nil {
		return ProvisionDraft{}, err
	}
	RecordChoices(options, Choice{Label: "IPv6", Value: yesNo(draft.IPv6)})

	return draft, nil
}

func ConfirmProvision(ctx context.Context, options Options, profile provider.CredentialProfileRef, draft ProvisionDraft, catalog provider.Catalog, resume bool) (bool, error) {
	monthly := 0.0
	for _, size := range catalog.Sizes {
		if size.ID == draft.Size {
			monthly = size.Price.Monthly
		}
	}
	if draft.AutomaticBackups {
		monthly *= 1.2
	}
	if err := beginReview(ctx, options); err != nil {
		return false, err
	}
	renderKeyValues(options,
		Choice{Label: "Name", Value: draft.Name},
		Choice{Label: "Acquisition", Value: "DigitalOcean"},
		Choice{Label: "Profile", Value: string(profile)},
		Choice{Label: "Region", Value: draft.Region},
		Choice{Label: "Size", Value: draft.Size},
		Choice{Label: "Image", Value: draft.Image},
		Choice{Label: "VPC", Value: firstPromptValue(draft.NetworkID, "default regional VPC")},
		Choice{Label: "Local SSH keys", Value: promptCount(len(draft.LocalPublicKeys))},
		Choice{Label: "DO account SSH keys", Value: promptCount(len(draft.AccessKeyIDs))},
		Choice{Label: "Automatic backups", Value: yesNo(draft.AutomaticBackups)},
		Choice{Label: "IPv6", Value: yesNo(draft.IPv6)},
		Choice{Label: "Worktree root", Value: draft.WorktreeRoot},
		Choice{Label: "Estimate", Value: fmt.Sprintf("$%.2f/month, billed by DigitalOcean", monthly)},
	)
	if resume {
		_, _ = fmt.Fprintf(options.Output, "\nSchooner will resume creating this Droplet, trust its OpenSSH host key on first contact if needed, establish direct access, and prepare it as a box.\n\n")
	} else {
		_, _ = fmt.Fprintf(options.Output, "\nSchooner will create a billable Droplet, trust its new OpenSSH host key on first contact, establish direct access, and prepare it as a box.\n\n")
	}
	confirmed := false
	title, affirmative := "Create this Droplet?", "Create"
	if resume {
		title, affirmative = "Resume creating this Droplet?", "Resume"
	}
	if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(title).Affirmative(affirmative).Negative("Cancel").Value(&confirmed)))); err != nil {
		return false, err
	}
	return confirmed, nil
}

func beginReview(ctx context.Context, options Options) error {
	if !options.Accessible && options.Output != nil {
		clearScreen(options)
		if options.Theme != nil {
			if err := intro.Show(ctx, intro.Options{
				Output:   options.Output,
				Theme:    options.Theme,
				Animated: false,
				Color:    chrome(options),
			}); err != nil {
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					return ErrAborted
				}
				return err
			}
		}
	}
	section(options, "Review")
	return nil
}

func clearScreen(options Options) {
	if options.Accessible || options.Output == nil {
		return
	}
	_, _ = fmt.Fprint(options.Output, ansi.EraseEntireScreen+ansi.CursorHomePosition)
}

func section(options Options, title string) {
	line := title
	if chrome(options) {
		line = options.Theme.Style(uitheme.Text).Bold(true).Render(title)
	}
	_, _ = fmt.Fprintf(options.Output, "\n%s\n", line)
}

func RecordChoices(options Options, choices ...Choice) {
	if options.Summary == nil {
		return
	}
	for _, choice := range choices {
		if position, exists := options.Summary.index[choice.Label]; exists {
			options.Summary.rows[position] = choice
			continue
		}
		options.Summary.index[choice.Label] = len(options.Summary.rows)
		options.Summary.rows = append(options.Summary.rows, choice)
	}
	renderKeyValues(options, choices...)
}

func renderKeyValues(options Options, rows ...Choice) {
	if len(rows) == 0 {
		return
	}
	labelWidth := 0
	for _, row := range rows {
		labelWidth = max(labelWidth, len(row.Label))
	}
	for _, row := range rows {
		label := fmt.Sprintf("%-*s", labelWidth, row.Label)
		value := row.Value
		if chrome(options) {
			label = options.Theme.Style(uitheme.Muted).Render(label)
			value = options.Theme.Style(uitheme.Text).Render(value)
		}
		_, _ = fmt.Fprintf(options.Output, "  %s  %s\n", label, value)
	}
}

func chrome(options Options) bool {
	return !options.Accessible && options.Theme.HasColor()
}

func yesNo(value bool) string {
	if value {
		return "Yes"
	}
	return "No"
}

func promptCount(count int) string {
	if count == 0 {
		return "none"
	}
	return fmt.Sprintf("%d", count)
}

// Wait shows a loading spinner while action runs against a remote API.
func Wait(ctx context.Context, options Options, title string, action func(context.Context) error) error {
	if action == nil {
		return nil
	}
	if options.Output != nil {
		_, _ = fmt.Fprintln(options.Output)
	}
	err := spinner.While(ctx, options.Output, options.Theme, title, chrome(options), action)
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return ErrAborted
	}
	return err
}

// catalogOption renders a primary label with a muted secondary token (slug, fingerprint, price).
func catalogOption(options Options, label, detail string) string {
	if detail == "" {
		return label
	}
	if !chrome(options) {
		return label + "  " + detail
	}
	return label + "  " + options.Theme.Style(uitheme.Muted).Render(detail)
}

// sizeCatalogOption keeps specs muted and paints the monthly price in primary.
func sizeCatalogOption(options Options, id string, vcpus, memoryMB int, monthly float64) string {
	specs := fmt.Sprintf("%d vCPU · %d MB", vcpus, memoryMB)
	price := fmt.Sprintf("$%.2f/mo", monthly)
	if !chrome(options) {
		return id + "  " + specs + " · " + price
	}
	return id + "  " + options.Theme.Style(uitheme.Muted).Render(specs+" · ") + options.Theme.Style(uitheme.Primary).Render(price)
}

func ConfirmDestroy(ctx context.Context, options Options, record box.Record) (bool, error) {
	section(options, "Destroy box")
	renderKeyValues(options,
		Choice{Label: "Name", Value: record.Name},
		Choice{Label: "Provider", Value: "DigitalOcean"},
		Choice{Label: "Droplet ID", Value: record.ProviderResourceID},
	)
	_, _ = fmt.Fprintf(options.Output, "\nThe provider resource will be deleted before local inventory is removed. Disconnect source access first; destruction never calls GitHub.\n\n")
	confirmed := false
	if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Destroy this Droplet?").Affirmative("Destroy permanently").Negative("Keep it").Value(&confirmed)))); err != nil {
		return false, err
	}
	return confirmed, nil
}

func ConfirmProviderDisconnect(ctx context.Context, options Options, ref string) (bool, error) {
	confirmed := false
	if err := run(ctx, options, huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Disconnect "+ref+" locally?").Description("The token remains active in DigitalOcean until you revoke it there.").Affirmative("Disconnect").Negative("Keep it").Value(&confirmed)))); err != nil {
		return false, err
	}
	return confirmed, nil
}

func ConfirmDatabaseDestroy(ctx context.Context, options Options) (bool, error) {
	section(options, "Destroy local database")
	renderKeyValues(options,
		Choice{Label: "Local inventory", Value: "forgotten"},
		Choice{Label: "DigitalOcean resources", Value: "unchanged"},
		Choice{Label: "Credential-store entries", Value: "unchanged"},
		Choice{Label: "Migration backups", Value: "unchanged"},
		Choice{Label: "SSH identities", Value: "unchanged"},
	)
	_, _ = fmt.Fprintf(options.Output, "\nAll locally recorded boxes, provider profiles, and recovery operations will be forgotten.\n\n")
	confirmed := false
	form := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Destroy local database?").Affirmative("Destroy permanently").Negative("Keep it").Value(&confirmed)))
	if err := run(ctx, options, form); err != nil {
		return false, err
	}
	return confirmed, nil
}

func firstPromptValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func containsPrompt(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
