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
