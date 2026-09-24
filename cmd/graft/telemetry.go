package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/savings"
	"github.com/NanoNets/context-graph-engine/internal/telemetry"
	"github.com/NanoNets/context-graph-engine/internal/upkeep"
)

// queryNote carries what a query command resolved, for its `query` event.
var queryNote struct {
	repo string
	hit  string
}

// noteQuery records the repository a query answered from.
func noteQuery(repo string) {
	queryNote.repo = repo
	// Every retrieval command funnels through here, so this is where the
	// session's input-token rate is priced before a formatter needs it.
	savings.SetInputRate(savings.SessionInputRate(repo))
}

// noteHit records whether a query found anything.
func noteHit(found bool) {
	queryNote.hit = "no"
	if found {
		queryNote.hit = "yes"
	}
}

// noteQueryRoot records the root a query command answers from, resolved like
// the TypeScript queryRoot: an explicit dir as given, else the nearest indexed
// ancestor, else the working directory.
func noteQueryRoot(opts callersOptions) {
	root := opts.root
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return
		}
		root = nearestGraftRoot(cwd, opts.contextDir)
	}
	if absolute, err := filepath.Abs(root); err == nil {
		noteQuery(absolute)
	}
}

// pendingAction is the preAction hook for the current command, run once the
// command's arguments have parsed, as commander runs its hook.
var pendingAction func()

// actionStarted runs the pending preAction hook, at most once.
func actionStarted() {
	if pendingAction != nil {
		action := pendingAction
		pendingAction = nil
		action()
	}
}

// upkeepSkipped are the commands that own the upgrade story, or must keep
// stderr quiet at startup.
var upkeepSkipped = []string{
	"version", "upgrade", "_update-check", "_brain-refresh", "_telemetry-flush",
	"_hook", "_statusline", "_sync-run", "_install", "mcp",
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// preAction runs before every command except upkeepSkipped: a background
// update check and its nudge, then the one-time telemetry notice, first_run,
// and the daily flush.
func preAction(command string, stderr io.Writer) {
	if command == "" || slices.Contains(upkeepSkipped, command) {
		return
	}
	home := homeDir()
	if home == "" {
		return
	}
	now := time.Now()
	upkeep.MaybeRefreshInBackground(home, now)
	for _, line := range upkeep.StartupLines(currentVersion(), home) {
		writeDiagnostic(stderr, "%s\n", line)
	}
	if notice, ok := telemetry.FirstRunNotice(home); ok {
		writeDiagnostic(stderr, "%s\n", notice)
	}
	telemetry.TrackFirstRunIfNew(telemetry.Context{Home: home, Version: currentVersion()})
	telemetry.MaybeFlushInBackground(home, now)
}

// postAction queues the `query` event for a tracked command that succeeded;
// a failing command exits before its event, as in the TypeScript CLI.
func postAction(command string, status int) {
	if status != 0 || !telemetry.IsTrackedCommand(command) {
		return
	}
	properties := []telemetry.Property{{Key: "command", Value: command}, {Key: "surface", Value: "cli"}}
	if queryNote.hit != "" {
		properties = append(properties, telemetry.Property{Key: "hit", Value: queryNote.hit})
	}
	telemetry.Track("query", properties, telemetry.Context{Repo: queryNote.repo, Home: homeDir(), Version: currentVersion()})
}

// runTelemetry implements `graft telemetry [status|enable|disable|debug]`.
func runTelemetry(parsed parsedFlags, stdout, stderr io.Writer) int {
	action := "status"
	if len(parsed.positionals) > 0 {
		action = parsed.positionals[0]
	}
	actionStarted()
	home := homeDir()
	var output string
	switch action {
	case "status":
		output = telemetry.FormatStatus(home)
	case "enable":
		telemetry.SetEnabled(home, true)
		output = "telemetry: on — anonymous, aggregate-only. `graft telemetry status` for details."
	case "disable":
		telemetry.SetEnabled(home, false)
		output = "telemetry: off. Nothing further will be recorded or sent."
	case "debug":
		output = telemetry.FormatDebug(home)
	default:
		writeDiagnostic(stderr, "✗ unknown action \"%s\" — expected status, enable, disable, or debug\n", action)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, output); err != nil {
		return 1
	}
	return 0
}

// telemetryOffered reports whether the init picker offers the consent row:
// only when telemetry could run, or the user turned it off themselves.
func telemetryOffered() bool {
	reason := telemetry.Off(homeDir())
	return reason == "" || reason == telemetry.OffDisabled
}

func recordTelemetryConsent(consent bool) {
	telemetry.RecordConsent(homeDir(), consent)
}

// trackInitCompleted queues init_completed with the sorted agent list and
// the consent answer, or "unasked" when no picker ran.
func trackInitCompleted(repo string, ids []string, consent *bool) {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	answer := "unasked"
	if consent != nil {
		answer = fmt.Sprint(*consent)
	}
	telemetry.Track("init_completed", []telemetry.Property{
		{Key: "agents", Value: strings.Join(sorted, ",")},
		{Key: "consent", Value: answer},
	}, telemetry.Context{Repo: repo, Home: homeDir(), Version: currentVersion()})
}
