package box

import (
	"context"
	"slices"
	"strings"
)

// SelectionStore is the local-state seam needed by deterministic box
// selection. The SQLite adapter and in-memory tests both satisfy it.
type SelectionStore interface {
	FindByName(context.Context, string) (Record, error)
	FindByID(context.Context, string) (Record, error)
	List(context.Context) ([]Record, error)
	SetDefault(context.Context, string) (Record, error)
}

// Selector is an optional interactive adapter used only after deterministic
// resolution has exhausted explicit, linked, default, and sole-box choices.
type Selector interface {
	Select(context.Context, []Record) (string, error)
}

type SelectionRequest struct {
	ExplicitName string
	LinkedBoxID  string
	Selector     Selector
}

type Resolver struct{ store SelectionStore }

func NewResolver(store SelectionStore) *Resolver { return &Resolver{store: store} }

func (r *Resolver) Use(ctx context.Context, name string) (Record, error) {
	if err := ValidateName(name); err != nil {
		return Record{}, invalid(err)
	}
	return r.store.SetDefault(ctx, name)
}

func (r *Resolver) Resolve(ctx context.Context, request SelectionRequest) (Record, error) {
	if request.ExplicitName != "" {
		if err := ValidateName(request.ExplicitName); err != nil {
			return Record{}, invalid(err)
		}
		return r.store.FindByName(ctx, request.ExplicitName)
	}
	if request.LinkedBoxID != "" {
		return r.store.FindByID(ctx, request.LinkedBoxID)
	}

	records, err := r.store.List(ctx)
	if err != nil {
		return Record{}, err
	}
	slices.SortFunc(records, func(left, right Record) int { return strings.Compare(left.Name, right.Name) })
	if len(records) == 0 {
		return Record{}, NewError("not_found", "no boxes are registered", nil)
	}
	var configured *Record
	for index := range records {
		if !records[index].Default {
			continue
		}
		if configured != nil {
			return Record{}, NewError("internal", "local inventory contains multiple default boxes", nil)
		}
		configured = &records[index]
	}
	if configured != nil {
		return *configured, nil
	}
	if len(records) == 1 {
		return records[0], nil
	}
	if request.Selector == nil {
		names := make([]string, len(records))
		for index := range records {
			names[index] = records[index].Name
		}
		return Record{}, &Error{
			Code:    "box_selection_ambiguous",
			Message: "multiple boxes are configured; specify a box or run \"schooner box use <name>\"",
			Context: map[string]string{"candidates": strings.Join(names, ",")},
		}
	}
	name, err := request.Selector.Select(ctx, records)
	if err != nil {
		return Record{}, err
	}
	for _, record := range records {
		if record.Name == name {
			return record, nil
		}
	}
	return Record{}, NewError("internal", "box selector returned a box outside the candidate set", nil)
}
