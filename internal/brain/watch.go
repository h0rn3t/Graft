package brain

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

// Stage states, as Trail's build screen names them.
const (
	stageWaiting = "waiting"
	stageDoing   = "doing"
	stageDone    = "done"
	stageFailed  = "failed"
)

var stages = []struct{ id, label string }{
	{"reach", "reaching the repository"},
	{"read", "reading its history"},
	{"mine", "mining the rules"},
	{"file", "filing them into the brain"},
}

// RepoState is the repo row a brain is building, as the public endpoint returns it.
type RepoState struct {
	Status       string
	ErrorMessage string
	RuleCount    int
	CommitCount  int
	ThreadCount  int
	FiledSoFar   int
	FoundSoFar   int
}

type stageView struct {
	id, label, state, detail string
}

type buildView struct {
	stages []stageView
	ready  bool
	done   bool
	err    string
}

func positive(value jsonjs.Value) int {
	if number, ok := value.(float64); ok && number > 0 {
		return int(number)
	}
	return 0
}

type progress struct {
	reached, readIt, hasRules, filed, failed bool
}

func progressOf(repo RepoState) progress {
	return progress{
		reached:  repo.Status != "pending",
		readIt:   repo.CommitCount > 0 || repo.ThreadCount > 0,
		hasRules: repo.FiledSoFar > 0 || repo.RuleCount > 0,
		filed:    repo.RuleCount > 0,
		failed:   repo.Status == "failed",
	}
}

// failedStage places a failure at the stage that did not complete.
func (p progress) failedStage() int {
	switch {
	case !p.failed:
		return -1
	case !p.reached || !p.readIt:
		return 0
	case !p.hasRules:
		return 2
	default:
		return 3
	}
}

func pick(done, doing bool) string {
	if done {
		return stageDone
	}
	if doing {
		return stageDoing
	}
	return stageWaiting
}

func (p progress) state(index int) string {
	failedAt := p.failedStage()
	if failedAt == index {
		return stageFailed
	}
	if failedAt >= 0 && index > failedAt {
		return stageWaiting
	}
	return []string{pick(p.reached, true), pick(p.readIt, p.reached), pick(p.hasRules, p.readIt), pick(p.filed, p.hasRules)}[index]
}

func stageDetail(id string, repo RepoState) string {
	switch {
	case id == "read" && (repo.CommitCount > 0 || repo.ThreadCount > 0):
		parts := make([]string, 0, 2)
		if repo.CommitCount > 0 {
			parts = append(parts, strconv.Itoa(repo.CommitCount)+" commits")
		}
		if repo.ThreadCount > 0 {
			parts = append(parts, strconv.Itoa(repo.ThreadCount)+" discussions")
		}
		return strings.Join(parts, ", ")
	case id == "mine" && repo.FoundSoFar > 0:
		return strconv.Itoa(repo.FoundSoFar) + " rules so far"
	case id == "file" && repo.RuleCount > 0:
		return strconv.Itoa(repo.RuleCount) + " rules"
	case id == "file" && repo.FiledSoFar > 0:
		return strconv.Itoa(repo.FiledSoFar) + " rules so far"
	}
	return ""
}

// stagesFrom decides which stage the work is in.
func stagesFrom(repo RepoState) buildView {
	p := progressOf(repo)
	view := buildView{ready: p.hasRules && !p.failed, done: repo.Status == "completed" && p.filed}
	for index, stage := range stages {
		view.stages = append(view.stages, stageView{id: stage.id, label: stage.label, state: p.state(index), detail: stageDetail(stage.id, repo)})
	}
	if p.failed {
		view.err = trimJS(repo.ErrorMessage)
		if view.err == "" {
			view.err = "the read stopped before it finished"
		}
	}
	return view
}

// fetchRepoState reads the repo row, or false on any failure.
func fetchRepoState(ctx context.Context, link Link) (RepoState, bool) {
	value, err := getJSON(ctx, link, "/api/public/brains/"+encodeURIComponent(link.BrainID)+"/repo")
	if err != nil {
		return RepoState{}, false
	}
	body := asObject(value)
	repoValue, _ := objectGet(body, "repo")
	repo, ok := jsonjs.AsObject(repoValue)
	if !ok {
		return RepoState{}, false
	}
	build := asObject(objectValue(body, "build"))
	message, _ := objectGet(repo, "error_message")
	text, _ := message.(string)
	status, _ := objectGet(repo, "status")
	return RepoState{
		Status: jsString(status), ErrorMessage: text,
		RuleCount: positive(objectValue(repo, "rule_count")), CommitCount: positive(objectValue(repo, "commit_count")),
		ThreadCount: positive(objectValue(repo, "thread_count")),
		FiledSoFar:  positive(objectValue(build, "filed_so_far")), FoundSoFar: positive(objectValue(build, "found_so_far")),
	}, true
}

// linesFor prints each stage the first time it settles.
func linesFor(view buildView, printed map[string]bool) []string {
	out := make([]string, 0)
	for _, stage := range view.stages {
		if stage.state != stageDone && stage.state != stageFailed {
			continue
		}
		key := stage.id + ":" + stage.state
		if printed[key] {
			continue
		}
		printed[key] = true
		mark := "✗"
		if stage.state == stageDone {
			mark = "✓"
		}
		line := "  " + mark + " " + stage.label
		if stage.detail != "" {
			line += " — " + stage.detail
		}
		out = append(out, line)
	}
	return out
}

// WatchOutcome is how watching a build ended.
type WatchOutcome string

// Watch outcomes.
const (
	WatchCompleted   WatchOutcome = "completed"
	WatchBuilding    WatchOutcome = "building"
	WatchFailed      WatchOutcome = "failed"
	WatchTimedOut    WatchOutcome = "timed_out"
	WatchUnreachable WatchOutcome = "unreachable"
)

// WatchOptions bounds a watch.
type WatchOptions struct {
	Timeout time.Duration
	Poll    time.Duration
	Write   func(string)
}

// WatchBuild holds until the brain has rules, printing each stage as it settles.
func WatchBuild(ctx context.Context, link Link, opts WatchOptions) WatchOutcome {
	if opts.Timeout == 0 {
		opts.Timeout = 15 * time.Minute
	}
	if opts.Poll == 0 {
		opts.Poll = 4 * time.Second
	}
	printed := make(map[string]bool)
	started := time.Now()
	everRead := false
	for {
		if repo, ok := fetchRepoState(ctx, link); ok {
			everRead = true
			view := stagesFrom(repo)
			for _, line := range linesFor(view, printed) {
				opts.Write(line)
			}
			switch {
			case view.err != "":
				opts.Write("✗ " + view.err)
				return WatchFailed
			case view.done:
				return WatchCompleted
			case view.ready:
				return WatchBuilding
			}
		}
		if time.Since(started) >= opts.Timeout {
			if everRead {
				return WatchTimedOut
			}
			return WatchUnreachable
		}
		select {
		case <-ctx.Done():
			return WatchUnreachable
		case <-time.After(opts.Poll):
		}
	}
}
