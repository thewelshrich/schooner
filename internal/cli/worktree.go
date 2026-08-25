package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/config"
	invsqlite "github.com/thewelshrich/schooner/internal/inventory/sqlite"
	"github.com/thewelshrich/schooner/internal/repository"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
	"github.com/thewelshrich/schooner/internal/runtime/host"
)

func newWorktreeCommand(streams Streams, global *globalOptions) *cobra.Command {
	var explicitBox string
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
				catalog, err := runWorktreeList(cmd.Context(), streams, global, explicitBox)
				if err != nil {
					return executionError{cause: err}
				}
				return writeWorktreeList(cmd.OutOrStdout(), global.output, catalog)
			},
		},
		&cobra.Command{
			Use: "inspect <path>", Short: "Inspect one exact Git worktree path", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				inspection, err := runWorktreeInspect(cmd.Context(), streams, global, explicitBox, args[0])
				if err != nil {
					return executionError{cause: err}
				}
				return writeWorktreeInspection(cmd.OutOrStdout(), global.output, inspection)
			},
		},
	)
	return command
}

type worktreeTarget struct {
	direct     *host.Runtime
	remote     *application
	record     box.Record
	close      func()
	configured config.Host
	identity   string
}

func resolveWorktreeTarget(ctx context.Context, streams Streams, global *globalOptions, explicit string) (worktreeTarget, error) {
	if explicit == "" {
		local := host.New(hostBuildInfo(global.build))
		if hello, helloErr := local.Hello(); helloErr == nil {
			configured, configErr := config.ReadDefault()
			if configErr != nil {
				return worktreeTarget{}, box.NewError("conflict", "direct Box configuration is unavailable; run box setup from a workstation", configErr)
			}
			if record, found := matchingLocalRecord(ctx, streams, global, hello.BoxIdentity); found {
				if configured.WorktreeRoot != record.WorktreeRoot {
					return worktreeTarget{}, box.NewError("conflict", fmt.Sprintf("direct Box worktree root differs from local inventory; run \"schooner box setup %s\" from a workstation", record.Name), nil)
				}
				return worktreeTarget{direct: local, record: record, configured: configured, identity: hello.BoxIdentity}, nil
			}
			return worktreeTarget{direct: local, configured: configured, identity: hello.BoxIdentity}, nil
		}
	}
	services, closeServices, err := openApplication(ctx, streams, global.build)
	if err != nil {
		return worktreeTarget{}, err
	}
	record, err := resolveCommandBox(ctx, services.boxResolver, streams, global, explicit, "Choose a box")
	if err != nil {
		closeServices()
		return worktreeTarget{}, err
	}
	if record.RuntimePath == "" {
		closeServices()
		return worktreeTarget{}, box.NewError("host_runtime_missing", fmt.Sprintf("the box does not have a host runtime; run \"schooner box setup %s\"", record.Name), nil)
	}
	return worktreeTarget{remote: services, record: record, close: closeServices}, nil
}

// matchingLocalRecord performs the optional drift check only when a local
// inventory already exists. Direct host observation never creates inventory
// and remains available if unrelated local inventory cannot be opened.
func matchingLocalRecord(ctx context.Context, streams Streams, global *globalOptions, identity string) (box.Record, bool) {
	path, err := invsqlite.DefaultPath()
	if err != nil {
		return box.Record{}, false
	}
	if _, err = os.Stat(path); err != nil {
		return box.Record{}, false
	}
	services, closeServices, err := openApplication(ctx, streams, global.build)
	if err != nil {
		return box.Record{}, false
	}
	defer closeServices()
	records, err := services.boxes.List(ctx)
	if err != nil {
		return box.Record{}, false
	}
	for _, record := range records {
		if record.RemoteIdentity == identity {
			return record, true
		}
	}
	return box.Record{}, false
}

func runWorktreeList(ctx context.Context, streams Streams, global *globalOptions, explicit string) (repository.Catalog, error) {
	target, err := resolveWorktreeTarget(ctx, streams, global, explicit)
	if err != nil {
		return repository.Catalog{}, err
	}
	if target.close != nil {
		defer target.close()
	}
	if target.direct != nil {
		result, err := target.direct.ListWorktrees(ctx, hostruntime.NewWorktreeRequest("", target.identity))
		return result.Catalog, err
	}
	connection := box.Connection{Destination: target.record.SSHDestination, IdentityFile: target.record.IdentityFile, BatchMode: !interactionAllowed(streams, global)}
	catalog, err := target.remote.ssh.ListWorktrees(ctx, connection, box.HostRuntime{Path: target.record.RuntimePath}, target.record.RemoteIdentity)
	if err != nil {
		return repository.Catalog{}, err
	}
	if err = validateRemoteWorktreeRoot(target.record, catalog.WorktreeRoot); err != nil {
		return repository.Catalog{}, err
	}
	return catalog, nil
}

func runWorktreeInspect(ctx context.Context, streams Streams, global *globalOptions, explicit, selector string) (repository.Inspection, error) {
	target, err := resolveWorktreeTarget(ctx, streams, global, explicit)
	if err != nil {
		return repository.Inspection{}, err
	}
	if target.close != nil {
		defer target.close()
	}
	if target.direct != nil {
		result, err := target.direct.InspectWorktree(ctx, hostruntime.NewWorktreeRequest(selector, target.identity))
		return result.Inspection, err
	}
	connection := box.Connection{Destination: target.record.SSHDestination, IdentityFile: target.record.IdentityFile, BatchMode: !interactionAllowed(streams, global)}
	inspection, err := target.remote.ssh.InspectWorktree(ctx, connection, box.HostRuntime{Path: target.record.RuntimePath}, target.record.RemoteIdentity, selector)
	if err != nil {
		return repository.Inspection{}, err
	}
	if err = validateRemoteWorktreeRoot(target.record, inspection.WorktreeRoot); err != nil {
		return repository.Inspection{}, err
	}
	return inspection, nil
}

func validateRemoteWorktreeRoot(record box.Record, actual string) error {
	if actual != record.WorktreeRoot {
		return box.NewError("conflict", fmt.Sprintf("remote Box worktree root differs from local inventory; run \"schooner box setup %s\"", record.Name), nil)
	}
	return nil
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
