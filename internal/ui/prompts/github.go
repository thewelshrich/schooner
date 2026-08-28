package prompts

import (
	"context"
	"fmt"
)

type GitHubConnectDraft struct {
	BoxName         string
	AccountLogin    string
	NeedsDeviceFlow bool
}

type GitHubDisconnectDraft struct {
	BoxName      string
	AccountLogin string
	KeyTitle     string
	LastBox      bool
}

func ConfirmGitHubCloneRecovery(ctx context.Context, options Options, boxName, repository string) (bool, error) {
	featuredSection(options, "GitHub authentication")
	renderKeyValues(options,
		Choice{Label: "Box", Value: firstPromptValue(boxName, "this Box")},
		Choice{Label: "Repository", Value: firstPromptValue(repository, "GitHub")},
	)
	explain(options, "This Box could not authenticate to GitHub. Schooner already tried the Git and SSH configuration on the Box. A dedicated Box SSH key lets this machine clone without copying keys from your laptop.")
	return Confirm(ctx, options, "Create a dedicated Box key?", "Add Box key", "I'll fix Git on the Box")
}

func ConfirmGitHubConnect(ctx context.Context, options Options, draft GitHubConnectDraft) (bool, error) {
	boxName := firstPromptValue(draft.BoxName, "this Box")
	keyTitle := "Schooner / " + firstPromptValue(draft.BoxName, "Box")
	featuredSection(options, "GitHub access")
	if draft.NeedsDeviceFlow {
		rows := []Choice{{Label: "Box", Value: boxName}}
		if draft.AccountLogin != "" {
			rows = append(rows, Choice{Label: "Account", Value: draft.AccountLogin + "  (needs reauthorization)"})
		}
		rows = append(rows,
			Choice{Label: "GitHub App", Value: "Schooner"},
			Choice{Label: "Permission", Value: "Git SSH keys: read and write"},
			Choice{Label: "Token", Value: "this machine's credential store"},
			Choice{Label: "Private key", Value: "created on the Box, never copied here"},
		)
		renderKeyValues(options, rows...)
		explain(options, "GitHub will ask you to authorize the Schooner GitHub App. That permission is how Schooner adds this Box's public key to your account. It cannot read your repositories. Schooner will not copy SSH keys from this laptop, and it will not put a GitHub token on the Box.")
		return Confirm(ctx, options, "Authorize Schooner with GitHub?", "Authorize in browser", "Cancel")
	}
	account := firstPromptValue(draft.AccountLogin, "your GitHub account")
	renderKeyValues(options,
		Choice{Label: "Box", Value: boxName},
		Choice{Label: "Account", Value: account + "  (already authorized on this machine)"},
		Choice{Label: "SSH key", Value: keyTitle + "  (will be created on the Box)"},
	)
	explain(options, fmt.Sprintf("Schooner will create an SSH key on this Box and add only its public half to %s. The private key stays on the Box. Your laptop keys are not copied.", account))
	return Confirm(ctx, options, "Add this Box key?", "Add Box key", "Cancel")
}

func ConfirmGitHubDisconnect(ctx context.Context, options Options, draft GitHubDisconnectDraft) (bool, error) {
	boxName := firstPromptValue(draft.BoxName, "this Box")
	keyTitle := firstPromptValue(draft.KeyTitle, "Schooner / "+firstPromptValue(draft.BoxName, "Box"))
	featuredSection(options, "Disconnect GitHub")
	rows := []Choice{{Label: "Box", Value: boxName}}
	if draft.AccountLogin != "" {
		rows = append(rows, Choice{Label: "Account", Value: draft.AccountLogin})
	}
	rows = append(rows, Choice{Label: "SSH key", Value: keyTitle})
	renderKeyValues(options, rows...)
	explanation := "Schooner will remove this Box's public key from GitHub, then delete the private key on the Box. Other Boxes keep their keys. Your GitHub login is unchanged."
	if draft.LastBox {
		explanation += " This is the last connected Box, so the saved GitHub authorization on this laptop will be removed too."
	}
	explain(options, explanation)
	return Confirm(ctx, options, "Revoke this Box's GitHub SSH key?", "Disconnect", "Keep connected")
}
