package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/h0rn3t/Graft/internal/savings"
)

const (
	hookToolGraft  = "graft"
	hookToolSource = "source"
)

var hookGraftCommandPattern = regexp.MustCompile(`(?i)(^|[|&;]\s*)(npx\s+(-y\s+)?(@nanonets/)?)?graft(-dev)?\b`)
var hookDistCommandPattern = regexp.MustCompile(`dist[/\\]cli\.js`)
var hookSavingsFooterPattern = regexp.MustCompile(`\[graft\] tokens saved ≈ ([\d,]+)`)

type hookToolUse struct {
	Kind        string
	SavedTokens int
	Host        string
}

func isGraftMCPTool(name string) bool {
	name = strings.ToLower(name)
	for strings.Contains(name, "__") {
		name = strings.ReplaceAll(name, "__", ":")
	}
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == ':' || r == '.' || r == '/'
	})
	if len(parts) == 0 {
		return false
	}
	bare := parts[len(parts)-1]
	if _, ok := mcpAliases[bare]; ok {
		return true
	}
	return slices.ContainsFunc(mcpTools, func(tool mcpToolDefinition) bool {
		return strings.EqualFold(tool.Name, bare)
	})
}

func isMCPToolName(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "mcp") || strings.Contains(name, ":") || strings.Contains(name, "__")
}

func commandInvokesGraft(command string) bool {
	command = strings.TrimSpace(command)
	return hookDistCommandPattern.MatchString(command) || hookGraftCommandPattern.MatchString(command)
}

func classifyHookToolUse(name, command string) string {
	name = strings.ToLower(name)
	if name == "" {
		return ""
	}
	if isGraftMCPTool(name) {
		return hookToolGraft
	}
	if name == "read" || name == "grep" || name == "glob" || name == "search" {
		return hookToolSource
	}
	if (name == "bash" || name == "shell") && command != "" && commandInvokesGraft(command) {
		return hookToolGraft
	}
	return ""
}

func parseHookSavings(text string) int {
	total := 0
	for _, match := range hookSavingsFooterPattern.FindAllStringSubmatch(text, -1) {
		value, err := strconv.Atoi(strings.ReplaceAll(match[1], ",", ""))
		if err == nil {
			total += value
		}
	}
	return total
}

func recordHookToolUse(root, id string, use hookToolUse) error {
	if use.Kind == "" && use.SavedTokens <= 0 {
		return nil
	}
	return updateHookSession(root, id, func(session *sessionState) bool {
		switch use.Kind {
		case hookToolGraft:
			session.GraftReads++
			session.TurnUsedGraft = new(true)
		case hookToolSource:
			session.SourceReads++
		}
		if use.SavedTokens > 0 {
			session.SavedTokens += use.SavedTokens
		}
		if use.Host != "" && session.Host == nil {
			session.Host = new(use.Host)
		}
		return true
	})
}

func latestHookSession(root string) (string, sessionState, bool) {
	bestID := ""
	var bestTime int64
	for _, id := range listHookSessionIDs(root) {
		info, err := os.Stat(filepath.Join(hookSessionDir(root), id+".json"))
		if err != nil {
			continue
		}
		if bestID == "" || info.ModTime().UnixNano() > bestTime {
			bestID = id
			bestTime = info.ModTime().UnixNano()
		}
	}
	if bestID == "" {
		return "", sessionState{}, false
	}
	return bestID, readHookSession(root, bestID), true
}

func formatHookSessionStats(id string, session *sessionState) string {
	if session == nil {
		return "graft stats: no session recorded yet — use graft in an agent session, then look again."
	}
	total := session.GraftReads + session.SourceReads
	mix := "no retrieval yet"
	if total > 0 {
		mix = fmt.Sprintf("%.0f%% graft", math.Round(float64(session.GraftReads)/float64(total)*100))
	}
	lines := []string{
		"graft stats — session " + id,
		fmt.Sprintf("  graft reads:   %d", session.GraftReads),
		fmt.Sprintf("  source reads:  %d   (Read / Grep / Glob)", session.SourceReads),
		"  mix:           " + mix,
		"  tokens saved:  ~" + savings.Group(session.SavedTokens),
	}
	if session.InputCostMicros != nil && session.InputTokensBilled != nil {
		if usd, ok := savings.DollarsSaved(float64(session.SavedTokens), float64(*session.InputCostMicros), float64(*session.InputTokensBilled)); ok {
			lines = append(lines, "  value saved:   ~"+savings.FormatDollars(usd))
		}
	}
	if session.LastQuery != nil && *session.LastQuery != "" {
		lines = append(lines, "  last query:    "+*session.LastQuery)
	}
	return strings.Join(lines, "\n")
}
