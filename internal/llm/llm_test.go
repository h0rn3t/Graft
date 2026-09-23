package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func noSleep(context.Context, time.Duration) error { return nil }

func TestCallToolOpenAIRetriesAndFallsBack(t *testing.T) {
	var mu sync.Mutex
	bodies := make([]map[string]any, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		call := len(bodies)
		mu.Unlock()
		switch call {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
		case 2:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."}}`)
		default:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"type":"function","function":{"name":"t","arguments":"{\"ok\":true}"}}]}}]}`)
		}
	}))
	defer server.Close()

	client, err := New(Config{Provider: ProviderOpenAI, APIKey: "k", Model: "m", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	client.sleep = noSleep
	zero := 0.0
	args, err := client.CallTool(context.Background(), Request{System: "s", User: "u", Tool: Tool{Name: "t", Parameters: map[string]any{"type": "object"}}, Temperature: &zero, MaxTokens: 10})
	if err != nil || string(args) != `{"ok":true}` {
		t.Fatalf("CallTool() = (%s, %v), want the tool arguments", args, err)
	}
	if len(bodies) != 3 {
		t.Fatalf("CallTool() sent %d requests, want 3 (429 retry, then max_tokens fallback)", len(bodies))
	}
	if _, ok := bodies[2]["max_tokens"]; ok || bodies[2]["max_completion_tokens"] != float64(10) {
		t.Errorf("fallback request = %v, want max_completion_tokens instead of max_tokens", bodies[2])
	}
}

func TestCallToolErrorsMatchSDKWording(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	defer server.Close()
	tests := []struct {
		provider Provider
		want     string
	}{
		{ProviderOpenAI, "401 invalid x-api-key"},
		{ProviderAnthropic, `401 {"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`},
	}
	for _, tt := range tests {
		client, err := New(Config{Provider: tt.provider, APIKey: "k", Model: "m", BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.CallTool(context.Background(), Request{Tool: Tool{Name: "t"}})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || err.Error() != tt.want {
			t.Errorf("%s CallTool() error = %v, want %q", tt.provider, err, tt.want)
		}
	}

	client, err := New(Config{Provider: ProviderOpenAI, BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	client.retries = 0
	if _, err := client.CallTool(context.Background(), Request{Tool: Tool{Name: "t"}}); err == nil || err.Error() != "Connection error." {
		t.Errorf("CallTool() on a closed port error = %v, want Connection error.", err)
	}
	if _, err := New(Config{Provider: "nope"}); err == nil || !strings.Contains(err.Error(), "unknown provider: nope") {
		t.Errorf("New(nope) error = %v, want unknown provider", err)
	}
}

func TestResolveConfigPrecedence(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "legacy")
	for _, name := range []string{"GRAFT_PROVIDER", "GRAFT_API_KEY", "GRAFT_MODEL", "GRAFT_OPENROUTER_MODEL", "ORCAROUTER_MODEL", "GRAFT_BASE_URL", "OPENROUTER_BASE_URL", "ORCAROUTER_BASE_URL", "ORCAROUTER_API_KEY"} {
		unsetEnv(t, name)
	}
	cfg := ResolveConfig()
	if cfg.Provider != ProviderOpenAI || cfg.APIKey != "legacy" || cfg.BaseURL != openRouterBaseURL || cfg.Headers["X-Title"] != "graft" || cfg.Model != "openai/gpt-4o-mini" {
		t.Errorf("ResolveConfig() with only OPENROUTER_API_KEY = %+v, want the OpenRouter fallback", cfg)
	}
}

// unsetEnv removes name for the test and restores it afterwards.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}
