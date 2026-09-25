package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/h0rn3t/Graft/internal/graph"
	"github.com/h0rn3t/Graft/internal/savings"
)

type readBatchItem struct {
	Selector  string      `json:"selector"`
	Status    string      `json:"status"`
	Result    *readResult `json:"result,omitempty"`
	Error     string      `json:"error,omitempty"`
	CoveredBy string      `json:"coveredBy,omitempty"`
}

func validateReadSymbols(selectors []string) error {
	if len(selectors) == 0 || len(selectors) > 8 {
		return fmt.Errorf("symbols must contain between 1 and 8 selectors")
	}
	if slices.ContainsFunc(selectors, func(s string) bool { return strings.TrimSpace(s) == "" }) {
		return fmt.Errorf("symbols must contain non-empty strings")
	}
	return nil
}

func writeReadBatch(root string, workspace graph.WorkspaceGraphs, opts callersOptions, budget int, sources map[string]string, diagnostics string, stdout, stderr io.Writer) int {
	items := make([]readBatchItem, 0, len(opts.symbols))
	for _, selector := range opts.symbols {
		result, err := readExactSymbol(root, workspace, selector, budget, opts.mcp, sources)
		item := readBatchItem{Selector: selector, Status: "omitted"}
		if err != nil {
			item.Status, item.Error = "error", err.Error()
		} else {
			item.Result = &result
		}
		items = append(items, item)
	}
	// Parents precede children so source is emitted only once for contained spans.
	slices.SortStableFunc(items, func(a, b readBatchItem) int {
		if a.Result == nil || b.Result == nil {
			if a.Result != nil {
				return -1
			}
			if b.Result != nil {
				return 1
			}
			return strings.Compare(a.Selector, b.Selector)
		}
		ap, af, at, _ := parseAskPointer(a.Result.Pointer)
		bp, bf, bt, _ := parseAskPointer(b.Result.Pointer)
		return cmp.Or(strings.Compare(ap, bp), cmp.Compare(af, bf), cmp.Compare(bt, at))
	})
	codes := make([]string, len(items))
	for i := range items {
		if items[i].Result != nil {
			codes[i], items[i].Result.Code = items[i].Result.Code, ""
		}
	}
	if savings.Tokens(savings.Length(diagnostics+renderReadBatch(items, opts.jsonOutput))) > budget {
		writeDiagnostic(stderr, "batch metadata exceeds budget; increase budget or request fewer symbols\n")
		return 1
	}
	for i := range items {
		item := &items[i]
		if item.Status != "omitted" {
			continue
		}
		item.Status, item.Result.Code = "ok", codes[i]
		path, from, to, _ := parseAskPointer(item.Result.Pointer)
		var covered []int
		for j := i + 1; j < len(items); j++ {
			child := &items[j]
			if child.Status != "omitted" {
				continue
			}
			other, start, end, _ := parseAskPointer(child.Result.Pointer)
			if path == other && from <= start && to >= end {
				child.Status, child.CoveredBy = "covered", item.Result.ID
				covered = append(covered, j)
			}
		}
		// Source and every reference to it must fit together.
		if savings.Tokens(savings.Length(diagnostics+renderReadBatch(items, opts.jsonOutput))) > budget {
			item.Status, item.Result.Code = "omitted", ""
			for _, j := range covered {
				items[j].Status, items[j].CoveredBy = "omitted", ""
			}
		}
	}
	if _, err := io.WriteString(stderr, diagnostics); err != nil {
		return 1
	}
	if _, err := io.WriteString(stdout, renderReadBatch(items, opts.jsonOutput)); err != nil {
		return 1
	}
	return 0
}

func renderReadBatch(items []readBatchItem, asJSON bool) string {
	if asJSON {
		data, _ := json.Marshal(struct {
			Results []readBatchItem `json:"results"`
		}{items}) // Items contain only JSON-safe values.
		return string(data) + "\n"
	}
	var out strings.Builder
	for _, item := range items {
		switch item.Status {
		case "ok":
			out.WriteString(renderReadResult(*item.Result, false))
		case "covered":
			fmt.Fprintf(&out, "%s · %s — covered by %s\n", item.Selector, item.Result.Pointer, item.CoveredBy)
		case "omitted":
			fmt.Fprintf(&out, "%s · %s — omitted: increase budget or request separately\n", item.Selector, item.Result.Pointer)
		case "error":
			fmt.Fprintf(&out, "%s — error: %s\n", item.Selector, item.Error)
		}
	}
	return out.String()
}
