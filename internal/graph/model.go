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

// SourceRef records a source file and the content hash captured for it.
type SourceRef struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// NodeLink is a context-node link to another node slug.
type NodeLink struct {
	To          string  `json:"to"`
	Relation    string  `json:"relation"`
	Description *string `json:"description,omitempty"`
}

// ContextNode is the on-disk context node contract.
type ContextNode struct {
	Name          string      `json:"name"`
	Slug          string      `json:"slug"`
	Type          string      `json:"type"`
	Summary       string      `json:"summary"`
	Sources       []SourceRef `json:"sources"`
	SourcesDigest string      `json:"sourcesDigest"`
	Links         []NodeLink  `json:"links"`
	Human         string      `json:"human"`
}

// ManifestNode is the fast-read node roster entry in a manifest.
type ManifestNode struct {
	Slug          string   `json:"slug"`
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Sources       []string `json:"sources"`
	SourcesDigest string   `json:"sourcesDigest"`
}

// Manifest is the generated context index and repository staleness record.
type Manifest struct {
	Version    int            `json:"version"`
	Model      string         `json:"model"`
	RepoDigest string         `json:"repoDigest"`
	Files      []SourceRef    `json:"files"`
	Nodes      []ManifestNode `json:"nodes"`
}

// ManifestVersion is the current context manifest schema version.
const ManifestVersion = 1

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
}

// EdgeV1 is a version-one graph edge.
type EdgeV1 struct {
	Source     string     `json:"source"`
	Target     string     `json:"target"`
	Relation   Relation   `json:"relation"`
	Confidence Confidence `json:"confidence"`
}

// ScopeV1 describes a ranking scope discovered below the graph root.
type ScopeV1 struct {
	Prefix  string   `json:"prefix"`
	Label   string   `json:"label"`
	Markers []string `json:"markers"`
}

// GraphMeta contains the version and aggregate values stored with a graph.
type GraphMeta struct {
	Version   int        `json:"version"`
	NodeCount int        `json:"nodeCount"`
	EdgeCount int        `json:"edgeCount"`
	Languages []string   `json:"languages"`
	Scopes    *[]ScopeV1 `json:"scopes,omitempty"`
}

// GraphV1 is the version-one persisted wiring graph.
type GraphV1 struct {
	Meta  GraphMeta `json:"meta"`
	Nodes []NodeV1  `json:"nodes"`
	Edges []EdgeV1  `json:"edges"`
}
