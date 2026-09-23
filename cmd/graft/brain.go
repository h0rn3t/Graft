package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/brain"
	"github.com/NanoNets/context-graph-engine/internal/graph"
	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
	"github.com/NanoNets/context-graph-engine/internal/telemetry"
)

func parseBrainHandoff(value string) (brain.Link, error) {
	return brain.ParseHandoff(value)
}

// runBrain runs one `graft brain <subcommand>`.
func runBrain(sub string, parsed parsedFlags, stdout, stderr io.Writer) int {
	actionStarted()
	if sub == "connect" {
		dir := "."
		if len(parsed.positionals) > 1 {
			dir = parsed.positionals[1]
		}
		return runBrainConnect(parsed.positionals[0], dir, stderr)
	}
	repo, err := filepath.Abs(parsed.dir())
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	contextDir, _ := parsed.value("--dir")
	switch sub {
	case "pull":
		return runBrainPull(repo, stderr)
	case "status":
		return runBrainStatus(repo, contextDir, parsed.bools["--json"], stdout, stderr)
	case "push":
		return runBrainPush(repo, contextDir, !parsed.bools["--no-approve"], !parsed.bools["--no-watch"], stderr)
	default:
		if err := brain.ClearLink(repo); err != nil {
			writeDiagnostic(stderr, "%v\n", err)
			return 1
		}
		writeDiagnostic(stderr, "✓ detached the brain from %s\n", repo)
		return 0
	}
}

func runBrainConnect(handoff, dir string, stderr io.Writer) int {
	link, err := brain.ParseHandoff(handoff)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	repo, err := filepath.Abs(dir)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	home, _ := os.UserHomeDir() // an unknown home only narrows host detection
	result, err := brain.Connect(context.Background(), repo, link, home, nil, time.Now())
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	if result.Warning != "" {
		writeDiagnostic(stderr, "⚠ %s\n", result.Warning)
		return 0
	}
	if result.RuleCount == 0 {
		writeDiagnostic(stderr, "✓ attached this repo to the brain — it has no rules yet\n· run `graft brain push` to read this repository into it\n")
		return 0
	}
	writeDiagnostic(stderr, "✓ pulled %d rule(s) from %s\n", result.RuleCount, link.BrainID)
	for _, write := range result.Writes {
		writeDiagnostic(stderr, "✓ %s (%s)\n", write.Path, write.Action)
	}
	return 0
}

func runBrainPull(repo string, stderr io.Writer) int {
	home, _ := os.UserHomeDir() // an unknown home only narrows host detection
	result, linked, err := brain.Pull(context.Background(), repo, home, nil, time.Now())
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	if !linked {
		writeDiagnostic(stderr, "· no brain attached — run `graft brain connect <brainId>:<token>`\n")
		return 0
	}
	if result.Warning != "" {
		writeDiagnostic(stderr, "⚠ %s\n", result.Warning)
		return 1
	}
	writeDiagnostic(stderr, "✓ pulled %d rule(s)\n", result.RuleCount)
	for _, write := range result.Writes {
		writeDiagnostic(stderr, "✓ %s (%s)\n", write.Path, write.Action)
	}
	return 0
}

func runBrainStatus(repo, contextDir string, jsonOutput bool, stdout, stderr io.Writer) int {
	link, linked := brain.ReadLink(repo)
	cache, cached := brain.ReadRulesCache(repo)
	rules := cache.Rules
	var wiring *graph.GraphV1
	if loaded, err := graph.Read(graph.WiringPath(graphContextDir(repo, contextDir))); err == nil {
		wiring = loaded
	}
	pointers := make([]string, 0)
	if wiring != nil {
		for _, node := range wiring.Nodes {
			pointers = append(pointers, node.Path+":"+node.Span)
		}
	}
	applied := graph.ApplyBrainRules(pointers, rules, wiring)
	stale := make([]graph.AppliedRule, 0)
	for _, rule := range applied {
		if rule.Stale {
			stale = append(stale, rule)
		}
	}
	if jsonOutput {
		report := jsonjs.NewObject()
		report.Set("brainId", nil)
		if linked {
			report.Set("brainId", link.BrainID)
		}
		report.Set("cached", len(rules))
		report.Set("fetchedAt", nil)
		if cached {
			report.Set("fetchedAt", float64(cache.FetchedAt))
		}
		report.Set("anchored", len(applied))
		report.Set("stale", len(stale))
		if _, err := fmt.Fprintln(stdout, jsonjs.Stringify(report, 2)); err != nil {
			return 1
		}
		return 0
	}
	if !linked {
		writeDiagnostic(stderr, "· no brain attached — run `graft brain connect <brainId>:<token>`\n")
		return 0
	}
	pulled := ""
	if cached && cache.FetchedAt != 0 {
		pulled = ", pulled " + time.UnixMilli(cache.FetchedAt).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	writeDiagnostic(stderr, "brain %s\n  %d rule(s) cached%s\n", link.BrainID, len(rules), pulled)
	if wiring == nil {
		writeDiagnostic(stderr, "  no graph yet — run `graft build` to see which rules still match the code\n")
		return 0
	}
	writeDiagnostic(stderr, "  %d anchored to symbols in this repo\n", len(applied))
	if len(stale) > 0 {
		writeDiagnostic(stderr, "  %d describe code that has changed since:\n", len(stale))
	} else {
		writeDiagnostic(stderr, "  none describe code that has changed since\n")
	}
	for _, rule := range stale[:min(len(stale), 10)] {
		writeDiagnostic(stderr, "    - %s\n      %s\n", rule.Rule, rule.Pointer)
	}
	return 0
}

// connectBrainAfterInit attaches the --brain given to init once the graph exists.
func connectBrainAfterInit(repo, handoff string, ids []string, home string, stderr io.Writer) int {
	link, err := brain.ParseHandoff(handoff)
	if err != nil {
		writeDiagnostic(stderr, "✗ --brain: %v\n", err)
		return 1
	}
	result, err := brain.Connect(context.Background(), repo, link, home, ids, time.Now())
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return 1
	}
	if result.Warning != "" {
		writeDiagnostic(stderr, "⚠ brain: %s\n", result.Warning)
	} else {
		writeDiagnostic(stderr, "✓ brain: pulled %d rule(s) from %s\n", result.RuleCount, link.BrainID)
	}
	for _, write := range result.Writes {
		writeDiagnostic(stderr, "✓ brain rules: %s (%s)\n", write.Path, write.Action)
	}
	if result.RuleCount > 0 && len(result.Writes) == 0 {
		writeDiagnostic(stderr, "· no instruction file to write rules into — graft ask still carries them\n")
	}
	return 0
}

// runBrainPush reads this checkout and sends its digest to the brain,
// signing up for one first when the repo has none, then follows the build.
func runBrainPush(repo, contextDir string, approve, watch bool, stderr io.Writer) int {
	ctx := context.Background()
	owner, name, here := brain.RepoSlug(repo)
	link, linked := brain.ReadLink(repo)
	if !linked {
		if !here {
			writeDiagnostic(stderr, "✗ this directory has no GitHub `origin` remote — graft can only push a GitHub repository today\n")
			return 1
		}
		signedUp, ok := signUpForBrain(repo, owner+"/"+name, stderr)
		if !ok {
			return 1
		}
		link = signedUp
	}
	expected, hasExpected := brain.FetchExpectedRepo(ctx, link)
	if hasExpected && here && !brain.SameRepo(expected.Slug, owner+"/"+name) {
		writeDiagnostic(stderr, "✗ this brain is for %s, but you are in %s/%s\n  cd into %s and run this again, or attach a different brain here.\n", expected.Slug, owner, name, expected.Slug)
		return 1
	}
	var wiring *graph.GraphV1
	if loaded, err := graph.Read(graph.WiringPath(graphContextDir(repo, contextDir))); err == nil {
		wiring = loaded
	} else {
		writeDiagnostic(stderr, "· no graph yet — run `graft build` first so rules can be anchored to symbols\n")
	}
	label := "this repository"
	switch {
	case hasExpected:
		label = expected.Slug
	case here:
		label = owner + "/" + name
	}
	writeDiagnostic(stderr, "· reading %s — commit messages, pull-request discussion and the docs in the tree.\n  No file contents leave this machine.\n", label)
	digest, warning, err := brain.BuildDigest(ctx, repo, wiring, approve)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	if warning != "" {
		writeDiagnostic(stderr, "⚠ %s\n", warning)
	}
	writeDiagnostic(stderr, "· %d commits, %d discussions, %d symbols, %d stated sources\n", len(digest.Commits), len(digest.Threads), len(digest.Symbols), len(digest.Sources))
	if _, err := brain.PushDigest(ctx, link, digest); err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		return 1
	}
	brainLabel := link.BrainID
	if hasExpected && expected.BrainName != "" {
		brainLabel = "“" + expected.BrainName + "”"
	}
	writeDiagnostic(stderr, "✓ sent %s/%s to %s\n", digest.Owner, digest.Name, brainLabel)
	if !watch {
		writeDiagnostic(stderr, "· it is being mined into rules now — a few minutes. Watch it finish in your browser.\n  The rules reach this repo on their own; nothing else to run.\n")
		return 0
	}
	writeDiagnostic(stderr, "· building the brain — Ctrl-C detaches, it keeps going without you\n")
	outcome := brain.WatchBuild(ctx, link, brain.WatchOptions{Write: func(line string) { writeDiagnostic(stderr, "%s\n", line) }})
	switch outcome {
	case brain.WatchCompleted:
		writeDiagnostic(stderr, "✓ the brain is built — its rules reach this repo on their own; nothing else to run\n")
	case brain.WatchBuilding:
		writeDiagnostic(stderr, "✓ the brain has its first rules — they reach this repo on their own; nothing else to run\n  The rest of the history is still being read. Watch it fill in your browser.\n")
	case brain.WatchFailed:
		writeDiagnostic(stderr, "  Nothing was lost — the brain is still there. Run `graft brain push` again to retry the read.\n")
		return 1
	case brain.WatchUnreachable:
		writeDiagnostic(stderr, "· could not reach Trail to follow the build — it is still running. Watch it finish in your browser.\n")
	default:
		writeDiagnostic(stderr, "· still building after 15 minutes — it has not failed, it is just long. Watch it finish in your browser.\n")
	}
	return 0
}

// signUpForBrain sends the user through Trail's signup in the browser and
// catches the handoff on loopback, saving the link it brings back.
func signUpForBrain(repo, slug string, stderr io.Writer) (brain.Link, bool) {
	handoff, err := brain.StartHandoff()
	if err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return brain.Link{}, false
	}
	target := brain.SignupURL(slug, handoff.Port, handoff.State)
	started := time.Now()
	ctx := telemetry.Context{Repo: repo, Home: homeDir(), Version: currentVersion()}
	telemetry.Track("brain_signup_opened", nil, ctx)
	settled := func(outcome string) {
		telemetry.Track("brain_signup_settled", []telemetry.Property{
			{Key: "outcome", Value: outcome}, {Key: "duration_bucket", Value: telemetry.DurationBucket(time.Since(started))},
		}, ctx)
	}
	writeDiagnostic(stderr, "· %s has no brain yet. Opening your browser to make one:\n  %s\n", slug, target)
	if !isTerminal(os.Stderr) {
		writeDiagnostic(stderr, "· not a terminal — open that link, then run `graft brain connect <brainId>:<token>` here\n")
		settled("no_tty")
		handoff.Close()
		return brain.Link{}, false
	}
	brain.OpenBrowser(target)
	writeDiagnostic(stderr, "· waiting for you to finish signing up…\n")
	link, err := handoff.Wait(brain.HandoffTimeout)
	if err != nil {
		writeDiagnostic(stderr, "✗ %v\n", err)
		if failure, ok := brain.AsHandoffError(err); ok {
			settled(string(failure.Reason))
		}
		return brain.Link{}, false
	}
	if err := brain.WriteLink(repo, link); err != nil {
		writeDiagnostic(stderr, "%v\n", err)
		return brain.Link{}, false
	}
	writeDiagnostic(stderr, "✓ brain connected to %s\n", slug)
	settled("linked")
	return link, true
}
