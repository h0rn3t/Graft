package blast

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/h0rn3t/Graft/internal/gitx"
)

// Owner is one person's claim on one area, from git history.
type Owner struct {
	Name    string  `json:"name"`
	Handle  string  `json:"handle,omitempty"`
	Commits int     `json:"commits"`
	Score   float64 `json:"score"`
	// Last is the most recent commit time in milliseconds since the epoch.
	Last int64 `json:"last"`
}

// Reviewer is one person's claim across the whole report.
type Reviewer struct {
	Owner
	Areas []string `json:"areas"`
}

// OwnerOptions controls owner lookup.
type OwnerOptions struct {
	// Exclude lists logins, names, or emails left out of suggestions.
	Exclude []string
	// Now is the reference time in milliseconds; zero means the current time.
	Now int64
}

const (
	halfLifeMS     = 120 * 24 * 60 * 60 * 1000
	ownerSince     = "36.months"
	maxCommits     = 400
	maxPathspec    = 80
	minShare       = 0.15
	maxPerArea     = 2
	affectedWeight = 0.6
	// ownerLookups bounds how many git log runs AttachOwners keeps in flight.
	ownerLookups = 4
	// MaxReviewers is how many people the tag line names.
	MaxReviewers = 3
)

var (
	githubNoreply = regexp.MustCompile(`(?i)^(?:\d+\+)?([A-Za-z0-9](?:-?[A-Za-z0-9]){0,38})@users\.noreply\.github\.com$`)
	botName       = regexp.MustCompile(`(?i)\[bot\]`)
	botEmail      = regexp.MustCompile(`(?i)\[bot\]@`)
	knownBot      = regexp.MustCompile(`(?i)^(github-actions|dependabot|renovate)(\[bot\])?$`)
	coAuthor      = regexp.MustCompile(`^(.*?)\s*<([^>]+)>$`)
)

// GitHubHandle extracts a GitHub handle from a noreply commit email.
func GitHubHandle(email string) (string, bool) {
	match := githubNoreply.FindStringSubmatch(strings.TrimSpace(email))
	if match == nil {
		return "", false
	}
	return match[1], true
}

func isBot(name, email string) bool {
	return botName.MatchString(name) || botEmail.MatchString(email) || knownBot.MatchString(strings.TrimSpace(name))
}

type ownerEntry struct {
	Owner
	seen map[string]bool
}

// OwnersFor returns the people behind files, best first. Files are relative
// to root, which may be a subdirectory of the repository.
func OwnersFor(ctx context.Context, root string, files []string, opts OwnerOptions) []Owner {
	paths := slices.Clone(files)
	sortCodeUnits(paths)
	if len(paths) > maxPathspec {
		paths = paths[:maxPathspec]
	}
	if len(paths) == 0 {
		return []Owner{}
	}
	now := opts.Now
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	args := append([]string{
		"log", "--no-merges", "--since=" + ownerSince, "-n", strconv.Itoa(maxCommits),
		// --relative names the files relative to root, as the graph does.
		"--format=\x01%H\x02%aI\x02%aN\x02%aE", "--name-only", "--relative", "--",
	}, paths...)
	out, err := gitx.Run(ctx, root, args...)
	if err != nil {
		return []Owner{}
	}
	keys, people := parseOwnerLog(out, paths, now)
	return rankOwners(keys, people, opts.Exclude)
}

type logCommit struct {
	hash, name, email string
	at                int64
	weight            float64
}

// parseOwnerLog folds git log --name-only output into one entry per person,
// keyed by lowercase handle or email, in first-seen order.
func parseOwnerLog(out string, paths []string, now int64) ([]string, map[string]*ownerEntry) {
	wanted := make(map[string]bool, len(paths))
	for _, path := range paths {
		wanted[path] = true
	}
	var commit *logCommit
	order := make([]string, 0)
	people := make(map[string]*ownerEntry)
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "\x01") {
			commit = parseLogHeader(line[1:], now)
			continue
		}
		path, ok := unquoteC(line)
		if commit == nil || !ok || !wanted[path] || isBot(commit.name, commit.email) {
			continue
		}
		handle, _ := GitHubHandle(commit.email)
		key := commit.email
		if handle != "" {
			key = handle
		}
		key = strings.ToLower(key)
		prev := people[key]
		if prev == nil {
			people[key] = &ownerEntry{
				Name: commit.name, Handle: handle, Commits: 1, Score: commit.weight, Last: commit.at,
				seen: map[string]bool{commit.hash: true},
			}
			order = append(order, key)
			continue
		}
		if prev.seen[commit.hash] {
			continue
		}
		prev.seen[commit.hash] = true
		prev.Commits++
		prev.Score += commit.weight
		prev.Last = max(prev.Last, commit.at)
	}
	return order, people
}

func parseLogHeader(header string, now int64) *logCommit {
	parts := strings.Split(header, "\x02")
	for len(parts) < 4 {
		parts = append(parts, "")
	}
	at, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return nil
	}
	ms := at.UnixMilli()
	return &logCommit{hash: parts[0], name: parts[2], email: parts[3], at: ms, weight: math.Pow(0.5, float64(now-ms)/halfLifeMS)}
}

// rankOwners drops excluded people and those under the share floor, then
// returns the heaviest few.
func rankOwners(keys []string, people map[string]*ownerEntry, exclude []string) []Owner {
	excluded := make(map[string]bool)
	for _, value := range exclude {
		if trimmed := strings.ToLower(strings.TrimSpace(value)); trimmed != "" {
			excluded[trimmed] = true
		}
	}
	kept := make([]Owner, 0)
	for _, key := range keys {
		owner := people[key].Owner
		if excluded[key] || excluded[strings.ToLower(owner.Name)] || (owner.Handle != "" && excluded[strings.ToLower(owner.Handle)]) {
			continue
		}
		kept = append(kept, owner)
	}
	total := 0.0
	for _, owner := range kept {
		total += owner.Score
	}
	kept = slices.DeleteFunc(kept, func(owner Owner) bool { return total != 0 && owner.Score/total < minShare })
	compare := localeCompare()
	slices.SortStableFunc(kept, func(a, b Owner) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), b.Commits-a.Commits, compare(a.Name, b.Name))
	})
	if len(kept) > maxPerArea {
		kept = kept[:maxPerArea]
	}
	return kept
}

// DiffAuthors lists everyone who authored or co-authored a commit in base..HEAD,
// the commits on HEAD that base does not have. It returns nil when git fails.
func DiffAuthors(ctx context.Context, root, base string) []string {
	if checkRef(base) != nil {
		return nil
	}
	out, err := gitx.Run(ctx, root, "log", "--format=%aN%n%aE%n%(trailers:key=Co-authored-by,valueonly)", "--end-of-options", base+"..HEAD", "--")
	if err != nil {
		return nil
	}
	names := make([]string, 0)
	add := func(name string) {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for raw := range strings.SplitSeq(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if match := coAuthor.FindStringSubmatch(line); match != nil {
			if match[1] != "" {
				add(match[1])
			}
			add(match[2])
			continue
		}
		add(line)
	}
	return names
}

// LocalIdentity returns the git user name and email configured in root.
func LocalIdentity(ctx context.Context, root string) []string {
	values := make([]string, 0, 2)
	for _, key := range []string{"user.name", "user.email"} {
		out, _ := gitx.Run(ctx, root, "config", key) // an unset key is no identity
		if value := strings.TrimSpace(out); value != "" {
			values = append(values, value)
		}
	}
	return values
}

type rankedReviewer struct {
	Reviewer
	weighted float64
}

// ownerLookup is one area or module whose owners AttachOwners looks up.
type ownerLookup struct {
	label  string
	files  []string
	weight float64
	owners **[]Owner
}

// AttachOwners fills in owners on the areas, then the modules, that the owner
// table has room for, and ranks the report's reviewers from them. Rows past
// the table keep nil owners.
func AttachOwners(ctx context.Context, root string, report *Report, opts OwnerOptions) {
	lookups := make([]ownerLookup, 0, len(report.Areas)+len(report.Modules))
	for _, area := range report.Areas {
		lookups = append(lookups, ownerLookup{label: area.Label, files: area.Files, weight: 1, owners: &area.Owners})
	}
	for _, module := range report.Modules {
		lookups = append(lookups, ownerLookup{label: module.Label, files: module.Files, weight: affectedWeight, owners: &module.Owners})
	}
	lookups = lookups[:min(len(lookups), maxOwnerRows)]

	found := make([][]Owner, len(lookups))
	slots := make(chan struct{}, ownerLookups)
	var wg sync.WaitGroup
	for i, lookup := range lookups {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			found[i] = OwnersFor(ctx, root, lookup.files, opts)
		})
	}
	wg.Wait()

	order := make([]string, 0)
	ranked := make(map[string]*rankedReviewer)
	for i, lookup := range lookups {
		owners := found[i]
		*lookup.owners = &owners
		label, weight := lookup.label, lookup.weight
		for _, owner := range owners {
			key := owner.Name
			if owner.Handle != "" {
				key = owner.Handle
			}
			key = strings.ToLower(key)
			prev := ranked[key]
			if prev == nil {
				ranked[key] = &rankedReviewer{Owner: owner, Areas: []string{label}, weighted: owner.Score * weight}
				order = append(order, key)
				continue
			}
			prev.Areas = append(prev.Areas, label)
			prev.Commits += owner.Commits
			prev.Score += owner.Score
			prev.weighted += owner.Score * weight
			prev.Last = max(prev.Last, owner.Last)
			if prev.Handle == "" && owner.Handle != "" {
				prev.Handle = owner.Handle
			}
		}
	}
	all := make([]*rankedReviewer, 0, len(order))
	for _, key := range order {
		all = append(all, ranked[key])
	}
	compare := localeCompare()
	slices.SortStableFunc(all, func(a, b *rankedReviewer) int {
		return cmp.Or(cmp.Compare(b.weighted, a.weighted), b.Commits-a.Commits, compare(a.Name, b.Name))
	})
	reviewers := make([]Reviewer, 0, MaxReviewers)
	for _, reviewer := range all[:min(len(all), MaxReviewers)] {
		reviewers = append(reviewers, reviewer.Reviewer)
	}
	report.Reviewers = &reviewers
}

// SinceLabel renders how long ago last was, such as "9d ago" or "3mo ago".
func SinceLabel(last, now int64) string {
	days := max(0, int(math.Round(float64(now-last)/(24*60*60*1000))))
	switch {
	case days < 1:
		return "today"
	case days == 1:
		return "yesterday"
	case days < 31:
		return fmt.Sprintf("%dd ago", days)
	}
	months := int(math.Round(float64(days) / 30))
	if months < 24 {
		return fmt.Sprintf("%dmo ago", months)
	}
	return fmt.Sprintf("%dy ago", int(math.Round(float64(days)/365)))
}

// Mention renders "@handle", or the bare name when git carries no handle.
func Mention(owner Owner) string {
	if owner.Handle != "" {
		return "@" + owner.Handle
	}
	return owner.Name
}
