package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/NanoNets/context-graph-engine/internal/jsonjs"
	"github.com/NanoNets/context-graph-engine/internal/savings"
)

func runStats(opts callersOptions, stdout, stderr io.Writer) int {
	root := opts.root
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			writeDiagnostic(stderr, "✗ failed to resolve working directory: %v\n", err)
			return 1
		}
		root = nearestGraftRoot(cwd, opts.contextDir)
		if root != cwd {
			writeDiagnostic(stderr, "[graft] no graft/ here — answering from %s/graft\n", root)
		}
	}
	id, data, found := savings.LatestSession(root)
	if !found {
		var err error
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
	raw := data
	if !json.Valid(data) || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		data = []byte(emptySessionJSON)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return 1
	}
	if opts.jsonOutput {
		// { id, ...session }: a parse failure or a literal null reads as the
		// empty session, as the TypeScript readSession does.
		session, err := jsonjs.Parse(raw)
		if err != nil || session == nil {
			session, _ = jsonjs.Parse([]byte(emptySessionJSON))
		}
		merged := jsonjs.NewObject()
		merged.Set("id", id)
		spread := jsonjs.Spread(session, true)
		for _, key := range spread.Keys() {
			value, _ := spread.Get(key)
			merged.Set(key, value)
		}
		if _, err := fmt.Fprintln(stdout, jsonjs.Stringify(merged, 2)); err != nil {
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
	if usd, ok := savings.DollarsSaved(saved, readNumber("inputCostMicros"), readNumber("inputTokensBilled")); ok {
		lines = append(lines, "  value saved:   ~"+savings.FormatDollars(usd))
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

// emptySessionJSON is the state of a session nothing has been recorded for.
const emptySessionJSON = `{"lastQuery":null,"perAgentQuery":{},"graftReads":0,"sourceReads":0,"savedTokens":0,"injectedPointers":[],"nudges":0}`

func groupedNumber(value float64) string {
	plain := strconv.FormatFloat(value, 'f', 0, 64)
	for i := len(plain) - 3; i > 0; i -= 3 {
		plain = plain[:i] + "," + plain[i:]
	}
	return plain
}
