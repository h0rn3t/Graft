package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

// openGates enables every gate for home and clears the CI and DNT variables.
func openGates(t *testing.T, host string) string {
	t.Helper()
	t.Setenv("GRAFT_POSTHOG_KEY", "phc_test")
	t.Setenv("GRAFT_POSTHOG_HOST", host)
	for _, name := range append([]string{"DO_NOT_TRACK", "CLAUDECODE"}, CIEnvVars...) {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	return t.TempDir()
}

func TestGatesCloseInOrder(t *testing.T) {
	home := openGates(t, "http://127.0.0.1:1")
	if got := Off(home); got != "" {
		t.Fatalf("Off() with every gate open = %q, want on", got)
	}
	t.Setenv("CI", "false")
	if got := Off(home); got != "" {
		t.Errorf("Off() with CI=false = %q, want on", got)
	}
	SetEnabled(home, false)
	if got := Off(home); got != OffDisabled {
		t.Errorf("Off() after disable = %q, want %q", got, OffDisabled)
	}
	t.Setenv("BUILDKITE", "1")
	if got := Off(home); got != OffCI {
		t.Errorf("Off() under BUILDKITE = %q, want %q", got, OffCI)
	}
	t.Setenv("DO_NOT_TRACK", "1")
	if got := Off(home); got != OffDoNotTrack {
		t.Errorf("Off() under DO_NOT_TRACK = %q, want %q", got, OffDoNotTrack)
	}
	t.Setenv("GRAFT_POSTHOG_KEY", "")
	if got := Off(home); got != OffNoKey {
		t.Errorf("Off() without a key = %q, want %q", got, OffNoKey)
	}
	Track("query", []Property{{Key: "command", Value: "ask"}}, Context{Home: home})
	if queued := Peek(home); len(queued) != 0 {
		t.Errorf("Track() with telemetry off queued %d events, want none", len(queued))
	}
}

func TestTrackDropsWhatTheContractDoesNotList(t *testing.T) {
	home := openGates(t, "http://127.0.0.1:1")
	repo := t.TempDir()
	Track("query", []Property{{Key: "command", Value: "ask"}, {Key: "path", Value: "/secret/src.go"}}, Context{Repo: repo, Home: home, Version: "1.0.0"})
	Track("made_up", nil, Context{Home: home})
	queued := Peek(home)
	if len(queued) != 1 {
		t.Fatalf("Peek() = %d events, want only the allowed one", len(queued))
	}
	line := jsonjs.Stringify(queued[0], 0)
	if strings.Contains(line, "secret") || strings.Contains(line, repo) || strings.Contains(line, "phc_test") {
		t.Errorf("queued event %s carries a path or the key", line)
	}
	if !strings.Contains(line, `"command":"ask"`) || !strings.Contains(line, `"repo_id":"`) {
		t.Errorf("queued event %s, want the command and a random repo id", line)
	}
}

func TestQueueTrimKeepsTheNewestWithinTheByteBound(t *testing.T) {
	home := openGates(t, "http://127.0.0.1:1")
	path := queuePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"event":"query","pad":"` + strings.Repeat("x", 1000) + `"}` + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(line, 300)), 0o644); err != nil {
		t.Fatal(err)
	}
	Track("first_run", nil, Context{Home: home})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxQueueBytes || !strings.Contains(string(data), `"event":"first_run"`) {
		t.Errorf("trimmed queue is %d bytes (max %d), newest event kept = %v", len(data), maxQueueBytes, strings.Contains(string(data), "first_run"))
	}
}

func TestRunFlushRetriesOnlyWhatCanSucceed(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		closed      bool
		wantQueued  int
		wantRequest bool
	}{
		{name: "delivered", status: http.StatusOK, wantQueued: 0, wantRequest: true},
		{name: "server error is retried", status: http.StatusBadGateway, wantQueued: 1, wantRequest: true},
		{name: "client error is dropped", status: http.StatusUnauthorized, wantQueued: 0, wantRequest: true},
		{name: "transport failure is retried", closed: true, wantQueued: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			var body atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				data, _ := io.ReadAll(r.Body)
				body.Store(string(data))
				w.WriteHeader(tt.status)
			}))
			host := server.URL
			if tt.closed {
				server.Close()
			} else {
				defer server.Close()
			}
			home := openGates(t, host)
			Track("query", []Property{{Key: "command", Value: "map"}}, Context{Home: home})
			RunFlush(home)
			if got := len(Peek(home)); got != tt.wantQueued {
				t.Errorf("queue after flush = %d events, want %d", got, tt.wantQueued)
			}
			if got := requests.Load() > 0; got != tt.wantRequest {
				t.Errorf("flush sent a request = %v, want %v", got, tt.wantRequest)
			}
			if sent, _ := body.Load().(string); tt.wantRequest && (!strings.Contains(sent, `"api_key":"phc_test"`) || !strings.Contains(sent, `"$process_person_profile":false`)) {
				t.Errorf("flush body = %s, want the key and anonymous events", sent)
			}
		})
	}
}

func TestRunFlushWithAnEmptyQueueSendsNothing(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1) }))
	defer server.Close()
	home := openGates(t, server.URL)
	RunFlush(home)
	if requests.Load() != 0 {
		t.Errorf("RunFlush() on an empty queue sent %d requests, want none", requests.Load())
	}
	if got := FormatDebug(home); !strings.HasPrefix(got, "telemetry: nothing queued.") {
		t.Errorf("FormatDebug() = %q, want the empty-queue message", got)
	}
}

func TestIsTrackedCommandExcludesViz(t *testing.T) {
	if IsTrackedCommand("viz") {
		t.Error("IsTrackedCommand(viz) = true, want false")
	}
	for _, command := range []string{"ask", "grep", "callers", "skeleton", "map", "check", "blast"} {
		if !IsTrackedCommand(command) {
			t.Errorf("IsTrackedCommand(%q) = false, want true", command)
		}
	}
}
