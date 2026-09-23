package graph

import (
	"fmt"
	"strings"
)

// graphScopes is every consumer's view of a graph's scopes: an absent list is
// the canonical single root scope, as the TypeScript scopesOfGraph returns.
func graphScopes(graph GraphV1) []ScopeV1 {
	if graph.Meta.Scopes != nil {
		return *graph.Meta.Scopes
	}
	return []ScopeV1{{Prefix: "", Label: "", Markers: []string{}}}
}

// scopeOf returns the scope owning path: the first non-root prefix match in
// scope order, else the root scope.
func scopeOf(path string, scopes []ScopeV1) ScopeV1 {
	for _, scope := range scopes {
		if scope.Prefix == "" {
			continue
		}
		if path == scope.Prefix || strings.HasPrefix(path, scope.Prefix+"/") {
			return scope
		}
	}
	for _, scope := range scopes {
		if scope.Prefix == "" {
			return scope
		}
	}
	return ScopeV1{Prefix: "", Label: "", Markers: []string{}}
}

// ScopeLabel renders a scope prefix for display: "(root)" or "prefix/".
func ScopeLabel(prefix string) string {
	if prefix == "" {
		return "(root)"
	}
	return prefix + "/"
}

// scopesHereClause names the scopes of a genuinely multi-scope graph, for a
// zero-hit note or an --in miss; it is empty otherwise.
func scopesHereClause(scopes []ScopeV1) string {
	if len(scopes) <= 1 {
		return ""
	}
	labels := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		labels = append(labels, ScopeLabel(scope.Prefix))
	}
	return " — scopes here: " + strings.Join(labels, " · ")
}

// assertPrefixIndexed rejects a normalized --in prefix no indexed node sits
// under, with the TypeScript CLI's message.
func assertPrefixIndexed(graph GraphV1, prefix string) error {
	if prefix == "" {
		return nil
	}
	for _, node := range graph.Nodes {
		if pathUnderPrefix(node.Path, prefix) {
			return nil
		}
	}
	return fmt.Errorf(`nothing indexed under "%s/"%s (or any path prefix)`, prefix, scopesHereClause(graphScopes(graph)))
}
