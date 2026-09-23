package brain

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultWebBaseURL = "https://app.trailhq.com"
	// HandoffTimeout is how long the loopback listener waits for the browser.
	HandoffTimeout = 5 * time.Minute
	callbackPath   = "/graft/callback"
)

// HandoffFailure categorizes a failed handoff for telemetry.
type HandoffFailure string

// Handoff failures.
const (
	HandoffTimedOut    HandoffFailure = "timed_out"
	HandoffBadCallback HandoffFailure = "bad_callback"
)

// HandoffError is a failed handoff: the sentence for the user, and its category.
type HandoffError struct {
	Message string
	Reason  HandoffFailure
}

func (err *HandoffError) Error() string {
	return err.Message
}

// Handoff is a loopback listener waiting for Trail to redirect back with a
// brain id and token that echo its random state.
type Handoff struct {
	Port   int
	State  string
	server *http.Server
	result chan handoffResult
	once   sync.Once
}

type handoffResult struct {
	link Link
	err  error
}

// WebBaseURL is the Trail front end, GRAFT_BRAIN_URL taking precedence.
func WebBaseURL() string {
	return trailingSlashes.ReplaceAllString(cmp.Or(os.Getenv("GRAFT_BRAIN_URL"), defaultWebBaseURL), "")
}

// BuildURL is the Trail screen that shows a brain being built.
func BuildURL(brainID string) string {
	return WebBaseURL() + "/get-started?step=build&brain=" + encodeURIComponent(brainID)
}

// SignupURL sends the browser to Trail for a repo's brain.
func SignupURL(repo string, port int, state string) string {
	query := url.Values{}
	query.Set("step", "repo")
	query.Set("graft_repo", repo)
	query.Set("graft_port", strconv.Itoa(port))
	query.Set("graft_state", state)
	// URLSearchParams keeps insertion order; url.Values.Encode sorts keys.
	ordered := []string{"step", "graft_repo", "graft_port", "graft_state"}
	parts := make([]string, len(ordered))
	for i, key := range ordered {
		parts[i] = key + "=" + formEscape(query.Get(key))
	}
	return WebBaseURL() + "/get-started?" + strings.Join(parts, "&")
}

// formEscape encodes like URLSearchParams: spaces as '+', and '*', '-', '.',
// '_' left bare.
func formEscape(value string) string {
	escaped := url.QueryEscape(value)
	return strings.NewReplacer("%2A", "*", "~", "%7E").Replace(escaped)
}

func donePage(ok bool) string {
	message := "That handoff did not match this terminal. Run `graft brain push` again."
	if ok {
		message = "Your brain is connected. Return to your terminal — the push is already running."
	}
	return `<!doctype html><meta charset="utf-8"><title>graft</title><body style="font:15px/1.5 system-ui,sans-serif;margin:3rem auto;max-width:32rem;color:#1F2129"><p>` + message + `</p><p style="color:#676767">You can close this tab.</p>`
}

// StartHandoff listens on 127.0.0.1 on a free port.
func StartHandoff() (*Handoff, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	handoff := &Handoff{Port: listener.Addr().(*net.TCPAddr).Port, State: base64.RawURLEncoding.EncodeToString(raw), result: make(chan handoffResult, 1)}
	handoff.server = &http.Server{Handler: http.HandlerFunc(handoff.serve), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = handoff.server.Serve(listener) }() // Serve returns once the handoff closes
	return handoff, nil
}

func (handoff *Handoff) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != callbackPath {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found")) // the browser is the only reader
		return
	}
	query := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(handoff.State)) != 1 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(donePage(false))) // the browser is the only reader
		return
	}
	brainID, token := trimJS(query.Get("brain")), trimJS(query.Get("token"))
	if brainID == "" || token == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(donePage(false))) // the browser is the only reader
		handoff.settle(handoffResult{err: &HandoffError{Message: "the browser came back without a brain id and token", Reason: HandoffBadCallback}})
		return
	}
	w.Header().Set("Location", BuildURL(brainID))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusSeeOther)
	handoff.settle(handoffResult{link: Link{BrainID: brainID, Token: token}})
}

func (handoff *Handoff) settle(result handoffResult) {
	select {
	case handoff.result <- result:
	default:
	}
}

// Wait returns the handed-over link, or a HandoffError on timeout or a bad
// callback. The listener is closed either way.
func (handoff *Handoff) Wait(timeout time.Duration) (Link, error) {
	defer handoff.Close()
	select {
	case result := <-handoff.result:
		return result.link, result.err
	case <-time.After(timeout):
		return Link{}, &HandoffError{Message: "timed out waiting for the browser — run `graft brain push` again, or use the link above", Reason: HandoffTimedOut}
	}
}

// Close stops listening; safe to call more than once.
func (handoff *Handoff) Close() {
	handoff.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = handoff.server.Shutdown(ctx) // an unclean shutdown only drops the listener
	})
}

// OpenBrowser opens url, best effort; GRAFT_NO_BROWSER disables it.
func OpenBrowser(target string) {
	if os.Getenv("GRAFT_NO_BROWSER") != "" {
		return
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("cmd", "/c", "start", "", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	if command.Start() == nil {
		_ = command.Process.Release()
	}
}

// AsHandoffError reports err's handoff category.
func AsHandoffError(err error) (*HandoffError, bool) {
	var handoffErr *HandoffError
	ok := errors.As(err, &handoffErr)
	return handoffErr, ok
}
