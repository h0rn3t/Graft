package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func runStats(opts callersOptions, stdout io.Writer) int {
	root := opts.root
	if root == "" {
		root = "."
	}
	contextDir := os.Getenv("GRAFT_DIR")
	if contextDir == "" {
		contextDir = filepath.Join(root, "graft")
	} else if !filepath.IsAbs(contextDir) {
		contextDir = filepath.Join(root, contextDir)
	}
	sessionDir := filepath.Join(contextDir, ".cache", "session")
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		entries = nil
	}
	var bestName string
	var bestTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err == nil && (bestName == "" || info.ModTime().After(bestTime)) {
			bestName, bestTime = entry.Name(), info.ModTime()
		}
	}
	if bestName == "" {
		if opts.jsonOutput {
			_, err = io.WriteString(stdout, "null\n")
		} else {
			_, err = io.WriteString(stdout, "graft stats: no session recorded yet — use graft in an agent session, then look again.\n")
		}
		if err != nil {
			return 1
		}
		return 0
	}
	id := strings.TrimSuffix(bestName, ".json")
	data, err := os.ReadFile(filepath.Join(sessionDir, bestName))
	if err != nil || !json.Valid(data) || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		data = []byte(`{"lastQuery":null,"perAgentQuery":{},"graftReads":0,"sourceReads":0,"savedTokens":0,"injectedPointers":[],"nudges":0}`)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return 1
	}
	if opts.jsonOutput {
		idJSON, _ := json.Marshal(id)
		body := bytes.TrimSpace(data)
		merged := append([]byte(`{"id":`), idJSON...)
		if len(body) > 2 {
			merged = append(merged, ',')
			merged = append(merged, body[1:]...)
		} else {
			merged = append(merged, '}')
		}
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, merged, "", "  "); err != nil {
			return 1
		}
		_, err := fmt.Fprintln(stdout, formatted.String())
		if err != nil {
			return 1
		}
		return 0
	}
	readNumber := func(key string) float64 {
		var value float64
		_ = json.Unmarshal(values[key], &value)
		return value
	}
	graft, source, saved := readNumber("graftReads"), readNumber("sourceReads"), readNumber("savedTokens")
	mix := "no retrieval yet"
	if total := graft + source; total != 0 {
		mix = fmt.Sprintf("%.0f%% graft", math.Round(graft/total*100))
	}
	lines := []string{
		"graft stats — session " + id,
		fmt.Sprintf("  graft reads:   %g", graft),
		fmt.Sprintf("  source reads:  %g   (Read / Grep / Glob)", source),
		"  mix:           " + mix,
		"  tokens saved:  ~" + groupedNumber(saved),
	}
	if cost, billed := readNumber("inputCostMicros"), readNumber("inputTokensBilled"); cost != 0 && billed != 0 && saved > 0 {
		usd := saved * (cost / billed) / 1e6
		if !math.IsInf(usd, 0) && !math.IsNaN(usd) {
			value := "<$0.01"
			if usd >= 0.01 {
				value = fmt.Sprintf("$%.2f", usd)
			}
			lines = append(lines, "  value saved:   ~"+value)
		}
	}
	var lastQuery string
	if json.Unmarshal(values["lastQuery"], &lastQuery) == nil && lastQuery != "" {
		lines = append(lines, "  last query:    "+lastQuery)
	}
	if _, err := fmt.Fprintln(stdout, strings.Join(lines, "\n")); err != nil {
		return 1
	}
	return 0
}

func groupedNumber(value float64) string {
	plain := strconv.FormatFloat(value, 'f', 0, 64)
	for i := len(plain) - 3; i > 0; i -= 3 {
		plain = plain[:i] + "," + plain[i:]
	}
	return plain
}
