package graph

import (
	"slices"
	"testing"
)

func TestGoGenericReceiverOwnsMethod(t *testing.T) {
	const source = "package stack\n\n" +
		"type Stack[T any] struct{ items []T }\n\n" +
		"func (s *Stack[T]) Push(v T) { s.items = append(s.items, v) }\n\n" +
		"func (s Stack[T]) Get() T { return s.items[0] }\n\n" +
		"type Pair[K comparable, V any] struct{}\n\n" +
		"func (p *Pair[K, V]) Get() K { var zero K; return zero }\n\n" +
		"func (s *Stack[T]) Twice(v T) { s.Push(v); s.Push(v) }\n\n" +
		"func build() { s := &Stack[int]{}; s.Push(1) }\n"
	got, err := extractFile("stack.go", source)
	if err != nil {
		t.Fatalf("extractFile(%q, source) error = %v, want nil", "stack.go", err)
	}
	for _, want := range []struct{ id, owner string }{
		{"stack.go#Stack.Push", "Stack"},
		{"stack.go#Stack.Get", "Stack"},
		{"stack.go#Pair.Get", "Pair"},
	} {
		index := slices.IndexFunc(got.nodes, func(node NodeV1) bool { return node.ID == want.id })
		if index < 0 {
			t.Errorf("extractFile(%q) nodes = %#v, want %q", "stack.go", got.nodes, want.id)
			continue
		}
		if owner := got.nodes[index].Owner; owner == nil || *owner != want.owner {
			t.Errorf("extractFile(%q) %s owner = %v, want %q", "stack.go", want.id, owner, want.owner)
		}
	}
	edges := resolveEdges(got.nodes, got.rawEdges, nil)
	for _, want := range []EdgeV1{
		{Source: "stack.go#Stack.Twice", Target: "stack.go#Stack.Push", Relation: "calls", Confidence: "extracted"},
		{Source: "stack.go#build", Target: "stack.go#Stack.Push", Relation: "calls", Confidence: "extracted"},
	} {
		if !slices.Contains(edges, want) {
			t.Errorf("resolveEdges(extractFile(%q)) = %#v, want %#v", "stack.go", edges, want)
		}
	}
}
