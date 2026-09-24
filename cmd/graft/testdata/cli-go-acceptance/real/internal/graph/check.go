package graph

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/sourcefiles"
)

const pendingCheckSample = 8

// GraphCheckResult describes structural drift and summary coverage in a wiring graph.
type GraphCheckResult struct {
	OK          bool     `json:"ok"`
	Missing     bool     `json:"missing"`
	Added       []string `json:"added"`
	Removed     []string `json:"removed"`
	Changed     []string `json:"changed"`
	Stale       []string `json:"stale"`
	Pending     int      `json:"pending"`
	PendingIDs  []string `json:"pendingIds"`
	Nodes       int      `json:"nodes"`
	Partial     bool     `json:"partial,omitzero"`
	Unsupported []string `json:"unsupported,omitzero"`
	Errors      []string `json:"errors,omitzero"`
}

// CheckGraph compares the committed graph with a fresh, read-only extraction.
// It uses the recorded --only-dir scope when available and preserves the graph
// and its sidecars on every path.
func CheckGraph(root, outDir string) (GraphCheckResult, error) {
	result := GraphCheckResult{
		Added:      make([]string, 0),
		Removed:    make([]string, 0),
		Changed:    make([]string, 0),
		Stale:      make([]string, 0),
		PendingIDs: make([]string, 0),
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return GraphCheckResult{}, fmt.Errorf("resolve repository root: %w", err)
	}
	if outDir == "" {
		outDir = filepath.Join(root, "graft")
	} else if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(root, outDir)
	}
	outDir = filepath.Clean(outDir)
	committed, err := Read(WiringPath(outDir))
	if err != nil {
		result.Missing = true
		return result, nil
	}
	fingerprint := ReadFingerprintScope(outDir)
	options := sourcefiles.Options{OutDir: outDir, NoReuse: true, NoSeed: true, NoCacheWrite: true}
	options.OnlyDirs = []string{} // Mark the scope resolved so BuildGraph does not reread its Go-only fingerprint.
	if fingerprint != nil && len(fingerprint.OnlyDirs) > 0 {
		options.OnlyDirs = slices.Clone(fingerprint.OnlyDirs)
	}
	current, err := BuildGraph(root, options)
	if err != nil {
		return GraphCheckResult{}, fmt.Errorf("extract current graph: %w", err)
	}
	if len(current.Unsupported) > 0 {
		result.Unsupported = slices.Clone(current.Unsupported)
	}
	if len(current.Errors) > 0 {
		result.Errors = slices.Clone(current.Errors)
	}
	result.Partial = len(current.Unsupported)+len(current.Errors) > 0

	committedByID := make(map[string]NodeV1, len(committed.Nodes))
	currentByID := make(map[string]string, len(current.Graph.Nodes))
	for _, node := range committed.Nodes {
		committedByID[node.ID] = node
	}
	result.Nodes = len(committedByID)
	for _, node := range current.Graph.Nodes {
		currentByID[node.ID] = node.BodyHash
	}
	for id, node := range committedByID {
		bodyHash, ok := currentByID[id]
		if !ok {
			result.Removed = append(result.Removed, id)
		} else if bodyHash != node.BodyHash {
			result.Changed = append(result.Changed, id)
		}
		if node.SummaryState == SummaryState("stale") {
			result.Stale = append(result.Stale, id)
		}
		if node.SummaryState == SummaryState("pending") {
			result.Pending++
			result.PendingIDs = append(result.PendingIDs, id)
		}
	}
	for _, node := range current.Graph.Nodes {
		if _, ok := committedByID[node.ID]; !ok {
			result.Added = append(result.Added, node.ID)
		}
	}
	slices.Sort(result.Added)
	slices.Sort(result.Removed)
	slices.Sort(result.Changed)
	slices.Sort(result.Stale)
	slices.Sort(result.PendingIDs)
	result.OK = len(result.Added)+len(result.Removed)+len(result.Changed)+len(result.Stale) == 0 && !result.Partial
	return result, nil
}

// FormatGraphCheckReport renders a graph freshness result for graft check.
func FormatGraphCheckReport(result GraphCheckResult) string {
	if result.Missing {
		return "graph check: NO GRAPH\n\nNo graft/.graph/wiring.json found. Run `graft build` first."
	}
	if result.OK {
		if result.Pending == 0 {
			return "graph check: OK — the wiring graph is in sync with the code."
		}
		percent := int(math.Round(float64(result.Nodes-result.Pending) / float64(result.Nodes) * 100))
		return "graph check: OK — the wiring graph is in sync with the code. (" + formatPendingCheckNote(result, percent) + ")"
	}

	status := "STALE"
	if result.Partial {
		status = "PARTIAL"
	}
	lines := []string{"graph check: " + status, ""}
	for _, group := range []struct {
		name   string
		paths  []string
		marker string
	}{
		{name: "changed", paths: result.Changed, marker: "~"},
		{name: "added", paths: result.Added, marker: "+"},
		{name: "removed", paths: result.Removed, marker: "-"},
		{name: "stale summaries", paths: result.Stale, marker: "!"},
	} {
		if len(group.paths) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s (%d):", group.name, len(group.paths)))
		for _, path := range group.paths {
			lines = append(lines, "  "+group.marker+" "+path)
		}
	}
	if len(result.Unsupported) > 0 {
		lines = append(lines, fmt.Sprintf("unsupported source files (%d):", len(result.Unsupported)))
		for _, path := range result.Unsupported {
			lines = append(lines, "  ! "+path)
		}
	}
	if len(result.Errors) > 0 {
		lines = append(lines, fmt.Sprintf("source extraction errors (%d):", len(result.Errors)))
		for _, message := range result.Errors {
			lines = append(lines, "  ! "+message)
		}
	}
	lines = append(lines, "")
	if result.Partial {
		lines = append(lines, "Run `graft build` to repair the source errors and refresh the graph.")
	} else {
		if len(result.Added)+len(result.Removed)+len(result.Changed) > 0 {
			lines = append(lines, "Run `graft build` to rebuild the structure, then commit graft/.")
		}
		if len(result.Stale) > 0 {
			lines = append(lines, "Run `graft build --deep` to refresh stale summaries.")
		}
	}
	return strings.Join(lines, "\n")
}

func formatPendingCheckNote(result GraphCheckResult, percent int) string {
	sample := result.PendingIDs
	if len(sample) > pendingCheckSample {
		sample = sample[:pendingCheckSample]
	}
	named := ""
	if len(sample) > 0 {
		named = ": " + strings.Join(sample, ", ")
		if len(result.PendingIDs) > pendingCheckSample {
			named += fmt.Sprintf(", … +%d more", len(result.PendingIDs)-pendingCheckSample)
		}
	}
	return fmt.Sprintf(
		"meaning tier %d%% complete — %d of %d node(s) pending%s. Run `graft build --deep` to summarize them; if a deep build already left these pending, that meaning pass failed — see that build's errors (re-running alone will not clear them)",
		percent, result.Pending, result.Nodes, named,
	)
}
