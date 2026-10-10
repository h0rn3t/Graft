// Package graph defines the versioned wiring graph model used by graft.
package graph

// Kind identifies what a graph node represents.
type Kind string

// Confidence describes how an edge was resolved.
type Confidence string

// SummaryState describes the state of a node's meaning-layer data.
type SummaryState string

// Origin identifies the extractor that produced a node.
type Origin string

// Relation identifies the relationship represented by an edge.
type Relation string

// Crux is the meaning-layer excerpt selected for a node.
type Crux struct {
	Code string `json:"code"`
	Span string `json:"span"`
}

// NodeV1 is a version-one graph node.
type NodeV1 struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Kind         Kind         `json:"kind"`
	Path         string       `json:"path"`
	Span         string       `json:"span"`
	Signature    *string      `json:"signature"`
	Exported     bool         `json:"exported"`
	Origin       Origin       `json:"origin"`
	BodyHash     string       `json:"body_hash"`
	Chars        *int         `json:"chars,omitempty"`
	BodyText     *string      `json:"body_text,omitempty"`
	SummaryState SummaryState `json:"summary_state"`
	Summary      *string      `json:"summary"`
	Crux         *Crux        `json:"crux"`
	// Owner, Arity, and Variadic follow Crux because the TypeScript extractor
	// appends them last, and wiring.json keeps its key order.
	Owner    *string `json:"owner,omitempty"`
	Arity    *int    `json:"arity,omitempty"`
	Variadic *bool   `json:"variadic,omitempty"`
	// Complexity is the cyclomatic complexity of a function or method with a body.
	Complexity *int `json:"complexity,omitempty"`
	// Generated marks every node of a file whose header says a tool wrote it.
	Generated bool `json:"generated,omitzero"`
}

// EdgeV1 is a version-one graph edge.
type EdgeV1 struct {
	Source     string     `json:"source"`
	Target     string     `json:"target"`
	Relation   Relation   `json:"relation"`
	Confidence Confidence `json:"confidence"`
	// Line is the 1-based line, in the source's file, of the first call,
	// use or import the edge stands for. It is zero for an edge no single
	// line states: contains, or an implements the resolver derived.
	Line int `json:"line,omitzero"`
}

// ScopeV1 describes a ranking scope discovered below the graph root.
type ScopeV1 struct {
	Prefix  string   `json:"prefix"`
	Label   string   `json:"label"`
	Markers []string `json:"markers"`
}

// UnresolvedCalls counts, by reason, the calls the static resolver bound to no
// node. The language-server pass may bind some of them afterwards.
type UnresolvedCalls struct {
	// ReceiverUnknown is `x.f()` where the type of x is not known.
	ReceiverUnknown int `json:"receiverUnknown"`
	// MemberAmbiguous is `x.f()` where the type of x has several reachable f.
	MemberAmbiguous int `json:"memberAmbiguous"`
	// MemberNotInGraph is `x.f()` where the type of x, or its f, is not a node.
	MemberNotInGraph int `json:"memberNotInGraph"`
	// NameAmbiguous is `f()` with several reachable definitions of f.
	NameAmbiguous int `json:"nameAmbiguous"`
	// NameNotInGraph is `f()` or `pkg.f()` with no definition of f in the graph:
	// a builtin, or a library the repository does not contain.
	NameNotInGraph int `json:"nameNotInGraph"`
	// ExternalPackage is a Go `pkg.f()` call into a package outside the
	// repository, or `x.f()` on a value of a type such a package declares.
	ExternalPackage int `json:"externalPackage"`
}

// UnresolvedName counts the uses of one name the static resolver bound to no
// node, although the graph has a symbol of that name.
type UnresolvedName struct {
	// Untyped is `x.name` or `x.name()` where the type of x is not known.
	Untyped int `json:"untyped,omitzero"`
	// Ambiguous is a use of name that matched several definitions.
	Ambiguous int `json:"ambiguous,omitzero"`
}

// GraphMeta contains the version and aggregate values stored with a graph.
type GraphMeta struct {
	Version         int              `json:"version"`
	NodeCount       int              `json:"nodeCount"`
	EdgeCount       int              `json:"edgeCount"`
	Languages       []string         `json:"languages"`
	Scopes          *[]ScopeV1       `json:"scopes,omitempty"`
	UnresolvedCalls *UnresolvedCalls `json:"unresolvedCalls,omitempty"`
	// UnresolvedNames holds, by name, the uses the resolver could not bind:
	// the callers the graph lists for a symbol of that name may be fewer
	// than the code has. It is written even when empty, so a graph from
	// before it was recorded reads as nil.
	UnresolvedNames map[string]UnresolvedName `json:"unresolvedNames"`
}

// GraphV1 is the version-one persisted wiring graph.
type GraphV1 struct {
	Meta  GraphMeta `json:"meta"`
	Nodes []NodeV1  `json:"nodes"`
	Edges []EdgeV1  `json:"edges"`
}
