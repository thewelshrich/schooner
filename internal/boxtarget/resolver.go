// Package boxtarget resolves where Repository, Worktree, and Session behavior
// runs and hides whether execution is direct or uses the on-demand SSH runtime.
package boxtarget

import (
	"context"
	"fmt"
	"io"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/config"
	hostruntime "github.com/thewelshrich/schooner/internal/runtime/host"
	sshruntime "github.com/thewelshrich/schooner/internal/runtime/ssh"
)

// Inventory is the local-state seam used only while resolving a target.
type Inventory interface {
	box.SelectionStore
	io.Closer
}

// Options are the explicitly composed dependencies of Resolver.
type Options struct {
	Direct                func() *hostruntime.Runtime
	Remote                *sshruntime.Runtime
	OpenInventory         func(context.Context) (Inventory, error)
	OpenExistingInventory func(context.Context) (Inventory, bool, error)
	ReadHostConfig        func() (config.Host, error)
}

// ResolveRequest carries Box selection and invocation policy. Selector is nil
// when interactive Box selection is unavailable.
type ResolveRequest struct {
	ExplicitBox    string
	LinkedBoxID    string
	Selector       box.Selector
	NonInteractive bool
	NoInput        bool
}

// Resolver applies Box selection policy and binds one execution adapter.
type Resolver struct{ options Options }

func NewResolver(options Options) *Resolver {
	if options.ReadHostConfig == nil {
		options.ReadHostConfig = config.ReadDefault
	}
	return &Resolver{options: options}
}

// Resolve returns an immutable target. Inventory is opened lazily and closed
// before the target crosses the interface.
func (r *Resolver) Resolve(ctx context.Context, request ResolveRequest) (Target, error) {
	if r == nil || r.options.Direct == nil || r.options.Remote == nil || r.options.OpenInventory == nil || r.options.OpenExistingInventory == nil {
		return Target{}, box.NewError("internal", "Box execution target is not configured", nil)
	}
	if err := ctx.Err(); err != nil {
		return Target{}, err
	}
	if request.ExplicitBox == "" {
		if target, ok, err := r.resolveDirect(ctx, request.NonInteractive); ok || err != nil {
			return target, err
		}
	}
	return r.resolveRemote(ctx, request)
}

func (r *Resolver) resolveDirect(ctx context.Context, nonInteractive bool) (Target, bool, error) {
	local := r.options.Direct()
	if local == nil {
		return Target{}, false, nil
	}
	hello, err := local.Hello()
	if err != nil {
		return Target{}, false, nil
	}
	configured, err := r.options.ReadHostConfig()
	if err != nil {
		return Target{}, true, box.NewError("conflict", "direct Box configuration is unavailable; run box setup from a workstation", err)
	}
	record := r.matchingLocalRecord(ctx, hello.BoxIdentity)
	if record.Name != "" && configured.WorktreeRoot != record.WorktreeRoot {
		return Target{}, true, box.NewError("conflict", fmt.Sprintf("direct Box worktree root differs from local inventory; run \"schooner box setup %s\" from a workstation", record.Name), nil)
	}
	state := &targetState{
		boxName:      record.Name,
		boxIdentity:  hello.BoxIdentity,
		worktreeRoot: configured.WorktreeRoot,
		direct:       true,
	}
	state.run = directAdapter{runtime: local, state: state, nonInteractive: nonInteractive}
	return Target{state: state}, true, nil
}

func (r *Resolver) matchingLocalRecord(ctx context.Context, identity string) box.Record {
	inventory, exists, err := r.options.OpenExistingInventory(ctx)
	if err != nil || !exists {
		return box.Record{}
	}
	defer func() { _ = inventory.Close() }()
	records, err := inventory.List(ctx)
	if err != nil {
		return box.Record{}
	}
	for _, record := range records {
		if record.RemoteIdentity == identity {
			return record
		}
	}
	return box.Record{}
}

func (r *Resolver) resolveRemote(ctx context.Context, request ResolveRequest) (Target, error) {
	inventory, err := r.options.OpenInventory(ctx)
	if err != nil {
		return Target{}, err
	}
	defer func() { _ = inventory.Close() }()
	record, err := box.NewResolver(inventory).Resolve(ctx, box.SelectionRequest{
		ExplicitName: request.ExplicitBox,
		LinkedBoxID:  request.LinkedBoxID,
		Selector:     request.Selector,
	})
	if err != nil {
		return Target{}, err
	}
	if record.RuntimePath == "" {
		return Target{}, box.NewError("host_runtime_missing", fmt.Sprintf("the box does not have a host runtime; run \"schooner box setup %s\"", record.Name), nil)
	}
	state := &targetState{
		boxName:      record.Name,
		boxIdentity:  record.RemoteIdentity,
		worktreeRoot: record.WorktreeRoot,
	}
	state.run = sshAdapter{
		runtime:              r.options.Remote,
		state:                state,
		interactiveBatchMode: request.NoInput,
		connection: box.Connection{
			Destination:  record.SSHDestination,
			IdentityFile: record.IdentityFile,
			BatchMode:    request.NonInteractive,
		},
		installed: box.HostRuntime{Path: record.RuntimePath},
	}
	return Target{state: state}, nil
}
