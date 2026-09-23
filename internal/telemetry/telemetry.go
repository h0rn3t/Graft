// Package telemetry records graft's anonymous usage events under the contract
// in TELEMETRY.md: an allowlist of events and properties, bucketed values, a
// local queue flushed at most once a day by a detached child, and four gates
// (a baked key, DO_NOT_TRACK, CI, and the user's own choice) checked before
// every append and every send. Nothing here returns an error to a caller.
package telemetry

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
)

// bakedKey is set at release time with -ldflags "-X …/telemetry.bakedKey=…".
// Source builds carry none, so a fork or a local build can never send.
var bakedKey = ""

// bakedHost is the ingestion host, overridable at release time the same way.
var bakedHost = "https://events.nanonets.com"

const (
	// DocURL is the public telemetry contract.
	DocURL = "https://github.com/NanoNets/context-graph-engine/blob/main/TELEMETRY.md"
	// FlushTTL is the minimum time between flush attempts.
	FlushTTL        = 24 * time.Hour
	maxQueueBytes   = 256 * 1024
	trimToEvents    = 500
	sendTimeout     = 8 * time.Second
	ingestPath      = "/batch/"
	omittedKeyLabel = "<omitted — graft's own ingestion key>"
)

// Notice is the one-time first-run disclosure.
var Notice = "· graft collects anonymous usage stats (no code, no file paths, no queries).\n" +
	"  What exactly: " + DocURL + " — turn it off with `graft telemetry disable`."

// events maps each allowed event to its allowed properties.
var events = map[string][]string{
	"install":              {"global"},
	"first_run":            {},
	"init_completed":       {"agents", "consent"},
	"build_completed":      {"files_bucket", "langs", "mode", "duration_bucket", "incremental"},
	"build_failed":         {"stage", "code"},
	"query":                {"command", "surface", "hit"},
	"brain_signup_opened":  {},
	"brain_signup_settled": {"outcome", "duration_bucket"},
	"session_summary":      {"graft_reads_bucket", "source_reads_bucket", "saved_tokens_bucket", "graft_turns_bucket", "reported_turns_bucket"},
}

var trackedCommands = []string{"ask", "grep", "callers", "skeleton", "map", "check", "blast", "viz"}

// IsTrackedCommand reports whether a command's queries are counted.
func IsTrackedCommand(name string) bool {
	return slices.Contains(trackedCommands, name)
}

// Key is the ingestion key: GRAFT_POSTHOG_KEY, else the baked one.
func Key() string {
	if key := os.Getenv("GRAFT_POSTHOG_KEY"); key != "" {
		return key
	}
	return bakedKey
}

var trailingSlashes = regexp.MustCompile(`/+$`)

// Host is the ingestion host, without a trailing slash.
func Host() string {
	host := os.Getenv("GRAFT_POSTHOG_HOST")
	if host == "" {
		host = bakedHost
	}
	return trailingSlashes.ReplaceAllString(host, "")
}

// OffReason names the first closed gate, or "" when telemetry may run.
type OffReason string

// Gate reasons.
const (
	OffNoKey       OffReason = "no-key"
	OffDoNotTrack  OffReason = "do-not-track"
	OffCI          OffReason = "ci"
	OffDisabled    OffReason = "disabled"
	ciVariableName           = "CI"
)

// CIEnvVars lists every variable CI detection reads.
var CIEnvVars = []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "CIRCLECI", "TEAMCITY_VERSION", "JENKINS_URL", "TF_BUILD", "BUILD_NUMBER"}

func doNotTrack() bool {
	value, ok := os.LookupEnv("DO_NOT_TRACK")
	return ok && value != "" && value != "0"
}

func inCI() bool {
	if value, ok := os.LookupEnv(ciVariableName); ok && value != "" && value != "0" && value != "false" {
		return true
	}
	for _, name := range CIEnvVars[1:] {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

// Off returns why telemetry is off for home, or "" when it may run.
func Off(home string) OffReason {
	switch {
	case Key() == "":
		return OffNoKey
	case doNotTrack():
		return OffDoNotTrack
	case inCI():
		return OffCI
	}
	if state, ok := readState(home); ok {
		if enabled, present := state.Get("enabled"); present && enabled == false {
			return OffDisabled
		}
	}
	return ""
}

// On reports whether every gate is open.
func On(home string) bool {
	return Off(home) == ""
}

// ExplainOff is the status line naming the closed gate.
func ExplainOff(reason OffReason) string {
	switch reason {
	case OffNoKey:
		return "off — this build has no telemetry key (a fork, or a local `npm run build`); nothing can be sent"
	case OffDoNotTrack:
		return "off — DO_NOT_TRACK is set in this environment"
	case OffCI:
		return "off — this looks like CI"
	default:
		return "off — you disabled it (`graft telemetry enable` to turn it back on)"
	}
}

func statePath(home string) string {
	return filepath.Join(home, ".graft", "telemetry.json")
}

func queuePath(home string) string {
	return filepath.Join(home, ".graft", "telemetry-queue.ndjson")
}

func readJSONFile(path string) (jsonjs.Value, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	value, err := jsonjs.Parse(data)
	return value, err == nil
}

func readState(home string) (*jsonjs.Object, bool) {
	value, ok := readJSONFile(statePath(home))
	if !ok {
		return nil, false
	}
	object, ok := jsonjs.AsObject(value)
	return object, ok
}

// newUUID returns a random version-4 UUID.
func newUUID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:]) // crypto/rand never fails on supported platforms
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// Field is one state key to set.
type Field struct {
	Key   string
	Value jsonjs.Value
}

// PatchState merges fields into ~/.graft/telemetry.json in order, minting an
// install id when the file is missing. A failed write keeps the state in
// memory only.
func PatchState(home string, fields ...Field) *jsonjs.Object {
	state, ok := readState(home)
	if ok {
		state = state.Clone()
	} else {
		state = jsonjs.NewObject()
		state.Set("installId", newUUID())
	}
	for _, field := range fields {
		state.Set(field.Key, field.Value)
	}
	_ = writeAtomic(statePath(home), []byte(jsonjs.Stringify(state, 2))) // unwritable home: in-memory only
	return state
}

// installID returns the machine's install id, minting it on first use.
func installID(home string) string {
	value, ok := readJSONFile(statePath(home))
	existing, _ := jsonjs.AsObject(value)
	if ok && existing != nil {
		if id, present := existing.Get("installId"); jsonjs.Truthy(id, present) {
			return jsonjs.String(id)
		}
	}
	id := newUUID()
	next := jsonjs.Spread(value, ok && value != nil)
	next.Set("installId", id)
	_ = writeAtomic(statePath(home), []byte(jsonjs.Stringify(next, 2))) // unwritable home: an ephemeral id
	return id
}

// ContextCacheDir is the repo's graft cache, honouring GRAFT_DIR.
func ContextCacheDir(repo string) string {
	override := os.Getenv("GRAFT_DIR")
	switch {
	case override == "":
		return filepath.Join(repo, "graft", ".cache")
	case filepath.IsAbs(override):
		return filepath.Join(override, ".cache")
	default:
		return filepath.Join(repo, override, ".cache")
	}
}

// repoID returns the checkout's random repo id, minting it on first use.
func repoID(repo string) string {
	path := filepath.Join(ContextCacheDir(repo), "telemetry-repo-id.json")
	if value, ok := readJSONFile(path); ok {
		if object, isObject := jsonjs.AsObject(value); isObject {
			if id, present := object.Get("repoId"); jsonjs.Truthy(id, present) {
				return jsonjs.String(id)
			}
		}
	}
	id := newUUID()
	record := jsonjs.NewObject()
	record.Set("repoId", id)
	_ = writeAtomic(path, []byte(jsonjs.Stringify(record, 2))) // unwritable cache: an ephemeral id
	return id
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporary.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

// Context carries the optional repo and agent host of one event.
type Context struct {
	Repo string
	// Host is claude-code, cursor, mcp, or cli; empty detects it.
	Host string
	// Home is the user's home directory.
	Home string
	// Version is the running graft version.
	Version string
}

func detectHost(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if _, ok := os.LookupEnv("CLAUDECODE"); ok {
		return "claude-code"
	}
	return "cli"
}

// platformName and archName report the platform in Node's vocabulary, the
// values every earlier event carried.
func platformName() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

func archName() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	default:
		return runtime.GOARCH
	}
}

// Property is one event property, in the order the call site gives it.
type Property struct {
	Key   string
	Value string
}

// Track appends one allowed event to the local queue when every gate is open.
// Properties the contract does not list for the event are dropped.
func Track(event string, properties []Property, ctx Context) {
	if !On(ctx.Home) {
		return
	}
	allowed, ok := events[event]
	if !ok {
		return
	}
	props := jsonjs.NewObject()
	props.Set("app_version", ctx.Version)
	props.Set("os", platformName())
	props.Set("arch", archName())
	props.Set("ci", "false")
	props.Set("agent_host", detectHost(ctx.Host))
	if ctx.Repo != "" {
		props.Set("repo_id", repoID(ctx.Repo))
	}
	for _, property := range properties {
		if slices.Contains(allowed, property.Key) {
			props.Set(property.Key, property.Value)
		}
	}
	queued := jsonjs.NewObject()
	queued.Set("event", event)
	queued.Set("properties", props)
	queued.Set("timestamp", time.Now().UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"))
	queued.Set("distinct_id", installID(ctx.Home))
	enqueue(ctx.Home, queued)
}

// TrackFirstRunIfNew records first_run once per machine.
func TrackFirstRunIfNew(ctx Context) {
	if !On(ctx.Home) {
		return
	}
	if state, ok := readState(ctx.Home); ok {
		if at, present := state.Get("firstRunAt"); jsonjs.Truthy(at, present) {
			return
		}
	}
	Track("first_run", nil, ctx)
	PatchState(ctx.Home, Field{"firstRunAt", nowISO()})
}

func nowISO() string {
	return time.Now().UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

// FirstRunNotice returns the disclosure the first time it is due, and marks it shown.
func FirstRunNotice(home string) (string, bool) {
	if !On(home) {
		return "", false
	}
	if state, ok := readState(home); ok {
		if at, present := state.Get("noticeShownAt"); jsonjs.Truthy(at, present) {
			return "", false
		}
	}
	PatchState(home, Field{"noticeShownAt", nowISO()})
	return Notice, true
}

// SetEnabled records `graft telemetry enable|disable`; disabling also marks
// the notice shown, so an opted-out user is not told about telemetry again.
func SetEnabled(home string, enabled bool) {
	if enabled {
		PatchState(home, Field{"enabled", true})
		return
	}
	RecordConsent(home, false)
}

// RecordConsent stores the init picker's answer and marks the notice shown.
func RecordConsent(home string, consent bool) {
	PatchState(home, Field{"enabled", consent}, Field{"noticeShownAt", nowISO()})
}

func enqueue(home string, event jsonjs.Value) {
	path := queuePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, err = file.WriteString(jsonjs.Stringify(event, 0) + "\n")
	if closeErr := file.Close(); err != nil || closeErr != nil {
		return
	}
	trim(path)
}

// trim drops the oldest events once the queue exceeds its byte bound.
func trim(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= maxQueueBytes {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := slices.DeleteFunc(strings.Split(string(data), "\n"), func(line string) bool { return line == "" })
	kept := make([]string, 0, trimToEvents)
	size := 0
	for i := len(lines) - 1; i >= 0 && len(kept) < trimToEvents; i-- {
		size += len(lines[i]) + 1
		if size > maxQueueBytes {
			break
		}
		kept = append(kept, lines[i])
	}
	slices.Reverse(kept)
	text := ""
	if len(kept) > 0 {
		text = strings.Join(kept, "\n") + "\n"
	}
	_ = os.WriteFile(path, []byte(text), 0o644) // the next drain clears it anyway
}

func parseLines(text string) []jsonjs.Value {
	out := make([]jsonjs.Value, 0)
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if value, err := jsonjs.Parse([]byte(line)); err == nil {
			out = append(out, value)
		}
	}
	return out
}

// Peek reads the queue without draining it.
func Peek(home string) []jsonjs.Value {
	data, err := os.ReadFile(queuePath(home))
	if err != nil {
		return []jsonjs.Value{}
	}
	return parseLines(string(data))
}

func drain(home string) []jsonjs.Value {
	path := queuePath(home)
	taken := path + "." + strconv.Itoa(os.Getpid()) + ".sending"
	if os.Rename(path, taken) != nil {
		return nil
	}
	data, _ := os.ReadFile(taken) // a vanished file is an empty batch
	_ = os.Remove(taken)
	return parseLines(string(data))
}

func requeue(home string, batch []jsonjs.Value) {
	if len(batch) == 0 {
		return
	}
	path := queuePath(home)
	pending, _ := os.ReadFile(path) // nothing queued since the drain
	lines := make([]string, len(batch))
	for i, event := range batch {
		lines[i] = jsonjs.Stringify(event, 0)
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	if os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"+string(pending)), 0o644) != nil {
		return
	}
	trim(path)
}

// BuildBatch is the wire body without the key: each event with its
// properties, distinct id, and the anonymous-event marker.
func BuildBatch(queued []jsonjs.Value) *jsonjs.Object {
	batch := make([]jsonjs.Value, 0, len(queued))
	for _, value := range queued {
		event, _ := jsonjs.AsObject(value)
		get := func(key string) (jsonjs.Value, bool) {
			if event == nil {
				return nil, false
			}
			return event.Get(key)
		}
		name, _ := get("event")
		props, hasProps := get("properties")
		properties := jsonjs.Spread(props, hasProps && props != nil)
		if distinct, ok := get("distinct_id"); ok {
			properties.Set("distinct_id", distinct)
		}
		properties.Set("$process_person_profile", false)
		item := jsonjs.NewObject()
		item.Set("event", name)
		if timestamp, ok := get("timestamp"); ok {
			item.Set("timestamp", timestamp)
		}
		item.Set("properties", properties)
		batch = append(batch, item)
	}
	body := jsonjs.NewObject()
	body.Set("batch", batch)
	return body
}

// send posts the batch and reports whether it should go back on the queue:
// transport failures and 5xx retry, 4xx does not.
func send(batch []jsonjs.Value) (retry bool) {
	body := jsonjs.NewObject()
	body.Set("api_key", Key())
	value, _ := BuildBatch(batch).Get("batch")
	body.Set("batch", value)
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, Host()+ingestPath, strings.NewReader(jsonjs.Stringify(body, 0)))
	if err != nil {
		return true
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return true
	}
	_ = response.Body.Close()
	return response.StatusCode >= 500
}

// RunFlush drains the queue and sends it, requeueing a retryable failure.
func RunFlush(home string) {
	if !On(home) {
		return
	}
	batch := drain(home)
	if len(batch) == 0 {
		return
	}
	if send(batch) {
		requeue(home, batch)
	}
}

// MaybeFlushInBackground starts a detached `graft _telemetry-flush` when the
// queue holds events and a day has passed since the last attempt.
func MaybeFlushInBackground(home string, now time.Time) bool {
	if !On(home) {
		return false
	}
	if info, err := os.Stat(queuePath(home)); err != nil || info.Size() == 0 {
		return false
	}
	if state, ok := readState(home); ok {
		if at, present := state.Get("flushedAt"); present {
			if ms, isNumber := at.(float64); isNumber && now.UnixMilli()-int64(ms) < FlushTTL.Milliseconds() {
				return false
			}
		}
	}
	PatchState(home, Field{"flushedAt", float64(now.UnixMilli())})
	executable, err := os.Executable()
	if err != nil || strings.HasSuffix(strings.TrimSuffix(filepath.Base(executable), ".exe"), ".test") {
		return false
	}
	command := exec.Command(executable, "_telemetry-flush")
	if command.Start() != nil {
		return false
	}
	_ = command.Process.Release()
	return true
}

// FormatStatus renders `graft telemetry status`.
func FormatStatus(home string) string {
	reason := Off(home)
	lines := []string{"telemetry: on — anonymous, aggregate-only"}
	if reason != "" {
		lines[0] = "telemetry: " + ExplainOff(reason)
	}
	lines = append(lines, "  contract:  "+DocURL)
	if reason == "" || reason == OffDisabled {
		pending := len(Peek(home))
		plural := "s"
		if pending == 1 {
			plural = ""
		}
		lines = append(lines, "  endpoint:  "+Host(), fmt.Sprintf("  queued:    %d event%s waiting for the next daily flush", pending, plural))
	}
	if reason == OffDisabled {
		lines = append(lines, "  enable:    graft telemetry enable")
	} else {
		lines = append(lines, "  disable:   graft telemetry disable  (or set DO_NOT_TRACK=1)")
	}
	lines = append(lines, "  inspect:   graft telemetry debug   (prints the exact batch, sends nothing)")
	return strings.Join(lines, "\n")
}

// FormatDebug renders `graft telemetry debug`: the pending batch, never sent.
func FormatDebug(home string) string {
	queued := Peek(home)
	if len(queued) == 0 {
		return "telemetry: nothing queued.\n  Run a graft command first — events are written locally and flushed once a day."
	}
	body := jsonjs.NewObject()
	body.Set("api_key", omittedKeyLabel)
	batch, _ := BuildBatch(queued).Get("batch")
	body.Set("batch", batch)
	return fmt.Sprintf("telemetry: %d event(s) queued. This is the exact body a flush would POST\nto %s/batch/ — running this command sends nothing.\n\n%s", len(queued), Host(), jsonjs.Stringify(body, 2))
}

// ErrorCode maps an error to the closed failure taxonomy without letting its
// text through: only fixed errno values and fixed substrings are matched.
func ErrorCode(err error) string {
	switch {
	case err == nil:
		return "E_UNKNOWN"
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return "E_DISK"
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM), errors.Is(err, os.ErrPermission):
		return "E_PERMISSION"
	case errors.Is(err, syscall.ENOMEM):
		return "E_OOM"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "E_TIMEOUT"
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET):
		return "E_NETWORK"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout"), strings.Contains(message, "timed out"):
		return "E_TIMEOUT"
	case strings.Contains(message, "parse"), strings.Contains(message, "syntax"):
		return "E_PARSE"
	case strings.Contains(message, "lock"):
		return "E_LOCK"
	default:
		return "E_UNKNOWN"
	}
}

// DurationBucket labels a duration coarsely.
func DurationBucket(elapsed time.Duration) string {
	seconds := elapsed.Seconds()
	switch {
	case seconds < 1:
		return "<1s"
	case seconds < 5:
		return "1-5s"
	case seconds < 30:
		return "5-30s"
	case seconds < 120:
		return "30s-2m"
	case seconds < 600:
		return "2-10m"
	default:
		return "10m+"
	}
}

// FilesBucket labels a repository's file count.
func FilesBucket(files int) string {
	switch {
	case files <= 0:
		return "0"
	case files < 50:
		return "1-49"
	case files < 200:
		return "50-199"
	case files < 1000:
		return "200-999"
	case files < 5000:
		return "1000-4999"
	default:
		return "5000+"
	}
}

var safeLanguage = regexp.MustCompile(`^[a-z0-9+#._-]+$`)

// LangsValue normalizes, filters, sorts, dedupes, and caps a language set.
func LangsValue(languages []string) string {
	clean := make([]string, 0, len(languages))
	for _, language := range languages {
		value := strings.ToLower(strings.TrimSpace(language))
		if value != "" && len(value) <= 24 && safeLanguage.MatchString(value) && !slices.Contains(clean, value) {
			clean = append(clean, value)
		}
	}
	slices.Sort(clean)
	return strings.Join(clean[:min(len(clean), 8)], ",")
}
