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
	edges, _, _ := resolveEdges(got.nodes, got.rawEdges, nil)
	for _, want := range []EdgeV1{
		{Source: "stack.go#Stack.Twice", Target: "stack.go#Stack.Push", Relation: "calls", Confidence: "extracted", Line: 13},
		{Source: "stack.go#build", Target: "stack.go#Stack.Push", Relation: "calls", Confidence: "extracted", Line: 15},
	} {
		if !slices.Contains(edges, want) {
			t.Errorf("resolveEdges(extractFile(%q)) = %#v, want %#v", "stack.go", edges, want)
		}
	}
}

func TestGoValueNodes(t *testing.T) {
	const source = "package app\n\n" +
		"const Limit = 3\n\n" +
		"const (\n\tKindA Kind = iota\n\tKindB\n)\n\n" +
		"var hits, misses int\n\n" +
		"var _ = setup()\n\n" +
		"type Box struct {\n\tName, note string `json:\"name\"`\n\tBase\n\tsub struct{ inner int }\n}\n\n" +
		"func (b *Box) Fill() {\n\tconst local = 1\n\tvar count int\n\t_, _ = local, count\n}\n"
	got, err := extractFile("app.go", source)
	if err != nil {
		t.Fatalf("extractFile(%q, source) error = %v, want nil", "app.go", err)
	}
	type value struct {
		id, kind, owner, span, signature string
		exported                         bool
	}
	var values []value
	for _, node := range got.nodes {
		if !askValueKind(node.Kind) {
			continue
		}
		owner, signature := "", ""
		if node.Owner != nil {
			owner = *node.Owner
		}
		if node.Signature != nil {
			signature = *node.Signature
		}
		values = append(values, value{node.ID, string(node.Kind), owner, node.Span, signature, node.Exported})
	}
	want := []value{
		{"app.go#Limit", "constant", "", "L3-L3", "const Limit = 3", true},
		{"app.go#KindA", "constant", "", "L6-L6", "const KindA Kind = iota", true},
		{"app.go#KindB", "constant", "", "L7-L7", "const KindB", true},
		{"app.go#hits", "variable", "", "L10-L10", "var hits, misses int", false},
		{"app.go#misses", "variable", "", "L10-L10", "var hits, misses int", false},
		{"app.go#Box.Name", "field", "Box", "L15-L15", "Name, note string `json:\"name\"`", true},
		{"app.go#Box.note", "field", "Box", "L15-L15", "Name, note string `json:\"name\"`", false},
		{"app.go#Box.sub", "field", "Box", "L17-L17", "sub struct{ inner int }", false},
	}
	if !slices.Equal(values, want) {
		t.Errorf("extractFile(%q) value nodes = %+v, want %+v (no blank, embedded, nested or local ones)", "app.go", values, want)
	}
	contains := func(source, target string) bool {
		return slices.ContainsFunc(got.rawEdges, func(edge rawEdge) bool {
			return edge.relation == "contains" && edge.source == source && edge.targetID == target
		})
	}
	if !contains("app.go#Box", "app.go#Box.Name") || !contains("app.go", "app.go#Limit") {
		t.Errorf("extractFile(%q) raw edges = %+v, want Box to contain Box.Name and the file Limit", "app.go", got.rawEdges)
	}
}
