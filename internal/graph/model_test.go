package graph

import (
	"encoding/json"
	"testing"
)

func TestNodeJSONPreservesOptionalAndNullableFields(t *testing.T) {
	owner := "Cache"
	chars := 12
	bodyText := "return value"
	variadic := true
	node := NodeV1{
		ID:           "src/cache.ts#Cache.get",
		Name:         "get",
		Kind:         "method",
		Owner:        &owner,
		Path:         "src/cache.ts",
		Span:         "L10-L14",
		Signature:    nil,
		Exported:     true,
		Origin:       "ast",
		BodyHash:     "hash",
		Chars:        &chars,
		BodyText:     &bodyText,
		Variadic:     &variadic,
		SummaryState: "pending",
		Summary:      nil,
		Crux:         nil,
	}

	data, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("json.Marshal(NodeV1) error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(NodeV1) error = %v", err)
	}
	if got["owner"] != owner {
		t.Errorf("NodeV1 JSON owner = %v, want %q", got["owner"], owner)
	}
	if got["body_text"] != bodyText {
		t.Errorf("NodeV1 JSON body_text = %v, want %q", got["body_text"], bodyText)
	}
	if _, ok := got["signature"]; !ok || got["signature"] != nil {
		t.Errorf("NodeV1 JSON signature = %v, want explicit null", got["signature"])
	}
	if _, ok := got["summary"]; !ok || got["summary"] != nil {
		t.Errorf("NodeV1 JSON summary = %v, want explicit null", got["summary"])
	}
	if _, ok := got["crux"]; !ok || got["crux"] != nil {
		t.Errorf("NodeV1 JSON crux = %v, want explicit null", got["crux"])
	}
	if _, ok := got["arity"]; ok {
		t.Errorf("NodeV1 JSON arity is present, want omitted optional field")
	}
}

func TestContextAndManifestJSONUseWireFieldNames(t *testing.T) {
	description := "generated link"
	contextNode := ContextNode{
		Name:          "Cache",
		Slug:          "cache",
		Type:          "service",
		Summary:       "Stores values.",
		Sources:       []SourceRef{{Path: "src/cache.ts", Hash: "abc"}},
		SourcesDigest: "digest",
		Links:         []NodeLink{{To: "storage", Relation: "contains", Description: &description}},
		Human:         "Keep notes here.",
	}
	manifest := Manifest{
		Version:    ManifestVersion,
		Model:      "test-model",
		RepoDigest: "repo-digest",
		Files:      []SourceRef{{Path: "src/cache.ts", Hash: "abc"}},
		Nodes:      []ManifestNode{{Slug: "cache", Name: "Cache", Type: "service", Sources: []string{"src/cache.ts"}, SourcesDigest: "digest"}},
	}

	data, err := json.Marshal(struct {
		Node     ContextNode `json:"node"`
		Manifest Manifest    `json:"manifest"`
	}{Node: contextNode, Manifest: manifest})
	if err != nil {
		t.Fatalf("json.Marshal(context contracts) error = %v", err)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(context contracts) error = %v", err)
	}
	if got["node"]["sourcesDigest"] != "digest" {
		t.Errorf("ContextNode JSON sourcesDigest = %v, want %q", got["node"]["sourcesDigest"], "digest")
	}
	if got["manifest"]["repoDigest"] != "repo-digest" {
		t.Errorf("Manifest JSON repoDigest = %v, want %q", got["manifest"]["repoDigest"], "repo-digest")
	}
}
