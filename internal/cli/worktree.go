package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/boxtarget"
	"github.com/thewelshrich/schooner/internal/repository"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
)

func newWorktreeCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	var addBranch string
	command := &cobra.Command{
		Use:   "worktree",
		Short: "Discover and inspect live Git worktrees",
		Args:  cobra.NoArgs,
		RunE:  helpRun,
	}
	command.PersistentFlags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	command.AddCommand(
		&cobra.Command{
			Use: "list", Short: "List live Git worktrees", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				catalog, err := runWorktreeList(cmd.Context(), streams, global, targets, explicitBox)
				if err != nil {
					return executionError{cause: err}
				}
				return writeWorktreeList(cmd.OutOrStdout(), global.output, catalog)
			},
		},
		&cobra.Command{
			Use: "inspect <path>", Short: "Inspect one exact Git worktree path", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				inspection, err := runWorktreeInspect(cmd.Context(), streams, global, targets, explicitBox, args[0])
				if err != nil {
					return executionError{cause: err}
				}
				return writeWorktreeInspection(cmd.OutOrStdout(), global.output, inspection)
			},
		},
	)
	add := &cobra.Command{
		Use: "add <repository-path> <path>", Short: "Add an ordinary linked Git Worktree", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := runWorktreeMutation(cmd.Context(), streams, global, targets, explicitBox, "add", args, addBranch)
			if err != nil {
				return executionError{cause: err}
			}
			return writeLifecycleResult(cmd.OutOrStdout(), global.output, result)
		},
	}
	add.Flags().StringVar(&addBranch, "branch", "", "existing branch or ref to check out")
	command.AddCommand(add)
	command.AddCommand(
		&cobra.Command{
			Use: "remove <path>", Short: "Remove one clean linked Git Worktree", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				result, err := runWorktreeMutation(cmd.Context(), streams, global, targets, explicitBox, "remove", args, "")
				if err != nil {
					return executionError{cause: err}
				}
				return writeLifecycleResult(cmd.OutOrStdout(), global.output, result)
			},
		},
		&cobra.Command{
			Use: "prune", Short: "Prune stale Git Worktree registrations", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				result, err := runWorktreeMutation(cmd.Context(), streams, global, targets, explicitBox, "prune", nil, "")
				if err != nil {
					return executionError{cause: err}
				}
				return writeLifecycleResult(cmd.OutOrStdout(), global.output, result)
			},
		},
	)
	return command
}

func newCloneCommand(streams Streams, global *globalOptions, targets *boxtarget.Resolver) *cobra.Command {
	var explicitBox string
	var branch string
	command := &cobra.Command{
		Use: "clone <repository>", Short: "Clone a normal primary Git Worktree", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := runClone(cmd.Context(), streams, global, targets, explicitBox, args[0], branch)
			if err != nil {
				return executionError{cause: err}
			}
			return writeLifecycleResult(cmd.OutOrStdout(), global.output, result)
		},
	}
	command.Flags().StringVar(&explicitBox, "box", "", "box name (always uses OpenSSH)")
	command.Flags().StringVar(&branch, "branch", "", "branch or tag to check out")
	return command
}

func resolveBoxExecutionTarget(ctx context.Context, streams Streams, global *globalOptions, targets *boxtarget.Resolver, explicit string) (boxtarget.Target, error) {
	var selector box.Selector
	if interactionAllowed(streams, global) {
		selector = promptBoxSelector{options: promptOptions(streams, global), title: "Choose a box"}
	}
	target, err := targets.Resolve(ctx, boxtarget.ResolveRequest{ExplicitBox: explicit, Selector: selector, NonInteractive: !interactionAllowed(streams, global), NoInput: global.noInput})
	if errors.Is(err, prompts.ErrAborted) {
		return boxtarget.Target{}, abortError{cause: err}
	}
	if box.ErrorCode(err) == "invalid_input" {
		return boxtarget.Target{}, usageError{cause: err}
	}
	return target, err
}

func runWorktreeList(ctx context.Context, streams Streams, global *globalOptions, targets *boxtarget.Resolver, explicit string) (repository.Catalog, error) {
	target, err := resolveBoxExecutionTarget(ctx, streams, global, targets, explicit)
	if err != nil {
		return repository.Catalog{}, err
	}
	return target.ListWorktrees(ctx)
}

func runWorktreeInspect(ctx context.Context, streams Streams, global *globalOptions, targets *boxtarget.Resolver, explicit, selector string) (repository.Inspection, error) {
	target, err := resolveBoxExecutionTarget(ctx, streams, global, targets, explicit)
	if err != nil {
		return repository.Inspection{}, err
	}
	return target.InspectWorktree(ctx, selector)
}

func runClone(ctx context.Context, streams Streams, global *globalOptions, targets *boxtarget.Resolver, explicit, source, branch string) (repository.MutationResult, error) {
	target, err := resolveBoxExecutionTarget(ctx, streams, global, targets, explicit)
	if err != nil {
		return repository.MutationResult{}, err
	}
	return target.CloneRepository(ctx, repository.CloneRequest{Source: source, Branch: branch})
}

func runWorktreeMutation(ctx context.Context, streams Streams, global *globalOptions, targets *boxtarget.Resolver, explicit, operation string, args []string, branch string) (repository.MutationResult, error) {
	target, err := resolveBoxExecutionTarget(ctx, streams, global, targets, explicit)
	if err != nil {
		return repository.MutationResult{}, err
	}
	switch operation {
	case "add":
		return target.AddWorktree(ctx, repository.AddRequest{RepositoryPath: args[0], Path: args[1], Branch: branch})
	case "remove":
		return target.RemoveWorktree(ctx, args[0])
	case "prune":
		return target.PruneWorktrees(ctx)
	default:
		return repository.MutationResult{}, box.NewError("internal", fmt.Sprintf("unknown Worktree mutation %q", operation), nil)
	}
}

type worktreeDocument struct {
	Path         string            `json:"path"`
	RelativePath string            `json:"relative_path"`
	GitDirectory string            `json:"git_directory"`
	Kind         string            `json:"kind"`
	Branch       string            `json:"branch,omitempty"`
	Detached     bool              `json:"detached"`
	HEAD         string            `json:"head,omitempty"`
	Status       repository.Status `json:"status"`
}

type repositoryDocument struct {
	CommonDirectory string             `json:"common_directory"`
	Origin          string             `json:"origin,omitempty"`
	Primary         *worktreeDocument  `json:"primary,omitempty"`
	Linked          []worktreeDocument `json:"linked"`
}

func documentRepository(value repository.Repository) repositoryDocument {
	result := repositoryDocument{CommonDirectory: value.CommonDirectory, Origin: value.Origin, Linked: make([]worktreeDocument, len(value.Linked))}
	if value.Primary != nil {
		primary := documentWorktree(*value.Primary)
		result.Primary = &primary
	}
	for index, linked := range value.Linked {
		result.Linked[index] = documentWorktree(linked)
	}
	return result
}

func documentWorktree(value repository.Worktree) worktreeDocument {
	return worktreeDocument{Path: value.Path, RelativePath: value.RelativePath, GitDirectory: value.GitDirectory, Kind: string(value.Kind), Branch: value.Branch, Detached: value.Detached, HEAD: value.HEAD, Status: value.Status}
}

func writeWorktreeList(writer io.Writer, output string, catalog repository.Catalog) error {
	if output == "json" {
		repositories := make([]repositoryDocument, len(catalog.Repositories))
		for index, value := range catalog.Repositories {
			repositories[index] = documentRepository(value)
		}
		return json.NewEncoder(writer).Encode(struct {
			SchemaVersion string               `json:"schema_version"`
			WorktreeRoot  string               `json:"worktree_root"`
			Repositories  []repositoryDocument `json:"repositories"`
			Warnings      []repository.Warning `json:"warnings"`
		}{"1", catalog.WorktreeRoot, repositories, catalog.Warnings})
	}
	if output != "human" {
		return fmt.Errorf("unsupported output format %q", output)
	}
	values := flattenWorktrees(catalog.Repositories)
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "PATH\tKIND\tBRANCH\tSTAGED\tUNSTAGED\tUNTRACKED\tCONFLICTED"); err != nil {
		return err
	}
	for _, value := range values {
		branch := value.Branch
		if value.Detached {
			branch = "(detached)"
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%d\t%d\t%d\n", humanSafe(value.RelativePath), humanSafe(string(value.Kind)), humanSafe(branch), value.Status.Staged, value.Status.Unstaged, value.Status.Untracked, value.Status.Conflicted); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	for _, warning := range catalog.Warnings {
		if _, err := fmt.Fprintf(writer, "Warning: %s: %s\n", humanSafe(warning.Path), humanSafe(warning.Message)); err != nil {
			return err
		}
	}
	return nil
}

func humanSafe(value string) string {
	quoted := strconv.QuoteToGraphic(value)
	return strings.TrimSuffix(strings.TrimPrefix(quoted, `"`), `"`)
}

func writeWorktreeInspection(writer io.Writer, output string, inspection repository.Inspection) error {
	if output == "json" {
		return json.NewEncoder(writer).Encode(struct {
			SchemaVersion string               `json:"schema_version"`
			WorktreeRoot  string               `json:"worktree_root"`
			Repository    repositoryDocument   `json:"repository"`
			Worktree      worktreeDocument     `json:"worktree"`
			Warnings      []repository.Warning `json:"warnings"`
		}{"1", inspection.WorktreeRoot, documentRepository(inspection.Repository), documentWorktree(inspection.Worktree), inspection.Warnings})
	}
	if output != "human" {
		return fmt.Errorf("unsupported output format %q", output)
	}
	value := inspection.Worktree
	branch := value.Branch
	if value.Detached {
		branch = "(detached)"
	}
	if _, err := fmt.Fprintf(writer, "Path: %s\nRelative path: %s\nKind: %s\nRepository: %s\nOrigin: %s\nGit directory: %s\nBranch: %s\nHEAD: %s\nStatus: %d staged, %d unstaged, %d untracked, %d conflicted\n", humanSafe(value.Path), humanSafe(value.RelativePath), humanSafe(string(value.Kind)), humanSafe(inspection.Repository.CommonDirectory), humanSafe(inspection.Repository.Origin), humanSafe(value.GitDirectory), humanSafe(branch), humanSafe(value.HEAD), value.Status.Staged, value.Status.Unstaged, value.Status.Untracked, value.Status.Conflicted); err != nil {
		return err
	}
	for _, warning := range inspection.Warnings {
		if _, err := fmt.Fprintf(writer, "Warning: %s: %s\n", humanSafe(warning.Path), humanSafe(warning.Message)); err != nil {
			return err
		}
	}
	return nil
}

func writeLifecycleResult(writer io.Writer, output string, result repository.MutationResult) error {
	if output == "json" {
		document := struct {
			SchemaVersion       string              `json:"schema_version"`
			Action              string              `json:"action"`
			Recovered           bool                `json:"recovered"`
			Repository          *repositoryDocument `json:"repository,omitempty"`
			Worktree            *worktreeDocument   `json:"worktree,omitempty"`
			Path                string              `json:"path,omitempty"`
			RepositoriesChecked int                 `json:"repositories_checked,omitempty"`
		}{SchemaVersion: "1", Action: result.Action, Recovered: result.Recovered, Path: result.Path, RepositoriesChecked: result.RepositoriesChecked}
		if result.Inspection != nil {
			repositoryValue := documentRepository(result.Inspection.Repository)
			worktreeValue := documentWorktree(result.Inspection.Worktree)
			document.Repository, document.Worktree = &repositoryValue, &worktreeValue
		}
		return json.NewEncoder(writer).Encode(document)
	}
	if output != "human" {
		return fmt.Errorf("unsupported output format %q", output)
	}
	switch result.Action {
	case "clone":
		_, err := fmt.Fprintf(writer, "Cloned %s\n", humanSafe(result.Path))
		return err
	case "worktree_add":
		_, err := fmt.Fprintf(writer, "Added Worktree %s\n", humanSafe(result.Path))
		return err
	case "worktree_remove":
		_, err := fmt.Fprintf(writer, "Removed Worktree %s\n", humanSafe(result.Path))
		return err
	case "worktree_prune":
		_, err := fmt.Fprintf(writer, "Pruned Worktree registrations for %d repositories\n", result.RepositoriesChecked)
		return err
	default:
		return fmt.Errorf("unsupported lifecycle action %q", result.Action)
	}
}

func flattenWorktrees(repositories []repository.Repository) []repository.Worktree {
	result := make([]repository.Worktree, 0)
	for _, value := range repositories {
		if value.Primary != nil {
			result = append(result, *value.Primary)
		}
		result = append(result, value.Linked...)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RelativePath < result[j].RelativePath })
	return result
}
