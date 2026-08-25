package box

import (
	"context"
	"slices"
	"testing"
)

type selectorFunc func(context.Context, []Record) (string, error)

func (f selectorFunc) Select(ctx context.Context, records []Record) (string, error) {
	return f(ctx, records)
}

func TestResolverPrecedence(t *testing.T) {
	store := newMemoryInventory()
	store.records["explicit"] = Record{ID: "box-explicit", Name: "explicit"}
	store.records["linked"] = Record{ID: "box-linked", Name: "linked"}
	store.records["default"] = Record{ID: "box-default", Name: "default", Default: true}
	resolver := NewResolver(store)

	selected, err := resolver.Resolve(t.Context(), SelectionRequest{ExplicitName: "explicit", LinkedBoxID: "box-linked"})
	if err != nil || selected.Name != "explicit" {
		t.Fatalf("explicit result=%+v err=%v", selected, err)
	}
	selected, err = resolver.Resolve(t.Context(), SelectionRequest{LinkedBoxID: "box-linked"})
	if err != nil || selected.Name != "linked" {
		t.Fatalf("linked result=%+v err=%v", selected, err)
	}
	selected, err = resolver.Resolve(t.Context(), SelectionRequest{})
	if err != nil || selected.Name != "default" {
		t.Fatalf("default result=%+v err=%v", selected, err)
	}
}

func TestResolverSoleBoxAndInteractiveAmbiguity(t *testing.T) {
	store := newMemoryInventory()
	store.records["sole"] = Record{ID: "box-sole", Name: "sole"}
	resolver := NewResolver(store)
	selected, err := resolver.Resolve(t.Context(), SelectionRequest{})
	if err != nil || selected.Name != "sole" {
		t.Fatalf("sole result=%+v err=%v", selected, err)
	}

	store.records["alpha"] = Record{ID: "box-alpha", Name: "alpha"}
	var candidates []string
	selected, err = resolver.Resolve(t.Context(), SelectionRequest{Selector: selectorFunc(func(_ context.Context, records []Record) (string, error) {
		for _, record := range records {
			candidates = append(candidates, record.Name)
		}
		return "sole", nil
	})})
	if err != nil || selected.Name != "sole" || !slices.Equal(candidates, []string{"alpha", "sole"}) {
		t.Fatalf("interactive result=%+v candidates=%v err=%v", selected, candidates, err)
	}
}

func TestResolverNonInteractiveAmbiguityIsDeterministic(t *testing.T) {
	store := newMemoryInventory()
	store.records["zeta"] = Record{ID: "box-zeta", Name: "zeta"}
	store.records["alpha"] = Record{ID: "box-alpha", Name: "alpha"}
	_, err := NewResolver(store).Resolve(t.Context(), SelectionRequest{})
	if ErrorCode(err) != "box_selection_ambiguous" {
		t.Fatalf("error=%v code=%s", err, ErrorCode(err))
	}
	target := err.(*Error)
	if target.Context["candidates"] != "alpha,zeta" {
		t.Fatalf("context=%v", target.Context)
	}
}

func TestResolverAuthoritativeMissingInputsDoNotFallThrough(t *testing.T) {
	store := newMemoryInventory()
	store.records["default"] = Record{ID: "box-default", Name: "default", Default: true}
	resolver := NewResolver(store)
	if _, err := resolver.Resolve(t.Context(), SelectionRequest{ExplicitName: "missing"}); !IsNotFound(err) {
		t.Fatalf("explicit error=%v", err)
	}
	if _, err := resolver.Resolve(t.Context(), SelectionRequest{LinkedBoxID: "box-missing"}); !IsNotFound(err) {
		t.Fatalf("linked error=%v", err)
	}
}
