// The graph-quality command reports structural metrics for a graft wiring graph.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/h0rn3t/Graft/internal/graphquality"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			if _, err := fmt.Fprintln(stdout, "Usage: graph-quality [graph-path] [--json] [--strict]\n       graph-quality <fixture-root> --oracle <manifest.json> [--json] [--strict]"); err != nil {
				return 2
			}
			return 0
		}
	}
	arg, jsonOutput, strict, oraclePath, err := parseArgs(args)
	if err != nil {
		diagnostic(stderr, "%v\n", err)
		return 2
	}
	if oraclePath != "" {
		return runOracle(arg, oraclePath, jsonOutput, strict, stdout, stderr)
	}
	path, ok := graphquality.ResolvePath(arg)
	if !ok {
		diagnostic(stderr, "no graph found at %s (run `graft build` first)\n", arg)
		return 2
	}
	graph, err := graphquality.Load(path)
	if err != nil {
		diagnostic(stderr, "failed to read graph at %s: %v\n", path, err)
		return 2
	}
	report := graphquality.Analyze(graph, path)
	if jsonOutput {
		data, err := report.JSON()
		if err != nil {
			diagnostic(stderr, "failed to encode graph report: %v\n", err)
			return 2
		}
		if _, err := fmt.Fprintln(stdout, string(data)); err != nil {
			return 2
		}
	} else {
		if _, err := fmt.Fprint(stdout, report.Human()); err != nil {
			return 2
		}
	}
	if strict && !report.Invariants.OK {
		return 1
	}
	return 0
}

func runOracle(root, path string, jsonOutput, strict bool, stdout, stderr io.Writer) int {
	file, err := os.Open(path)
	if err != nil {
		diagnostic(stderr, "open oracle: %v\n", err)
		return 2
	}
	manifest, decodeErr := graphquality.DecodeManifest(file)
	closeErr := file.Close()
	if decodeErr != nil || closeErr != nil {
		diagnostic(stderr, "read oracle: %v\n", firstError(decodeErr, closeErr))
		return 2
	}
	graph, err := graphquality.BuildOracleFixture(root, manifest)
	if err != nil {
		diagnostic(stderr, "evaluate oracle: %v\n", err)
		return 2
	}
	structural := graphquality.Analyze(graph, root)
	oracle, err := graphquality.EvaluateOracle(graph, manifest)
	if err != nil {
		diagnostic(stderr, "evaluate oracle: %v\n", err)
		return 2
	}
	if jsonOutput {
		structuralJSON, err := structural.JSON()
		if err != nil {
			diagnostic(stderr, "encode structural report: %v\n", err)
			return 2
		}
		data, err := json.MarshalIndent(struct {
			Structural json.RawMessage           `json:"structural"`
			Oracle     graphquality.OracleReport `json:"oracle"`
		}{Structural: structuralJSON, Oracle: oracle}, "", "  ")
		if err != nil {
			diagnostic(stderr, "encode oracle report: %v\n", err)
			return 2
		}
		if _, err := fmt.Fprintln(stdout, string(data)); err != nil {
			return 2
		}
	} else {
		var outputErr error
		write := func(format string, args ...any) {
			if outputErr == nil {
				_, outputErr = fmt.Fprintf(stdout, format, args...)
			}
		}
		write("%s", structural.Human())
		write("oracle:\n")
		for _, score := range oracle.Partitions {
			write("  %s (%s %s, %s): TP=%d FP=%d FN=%d precision=%s recall=%s\n",
				score.Name, score.Language, score.Relation, strings.Join(score.Files, ","),
				score.TP, score.FP, score.FN, formatMetric(score.Precision), formatMetric(score.Recall))
			for _, fact := range score.FalsePositives {
				write("    FP %s %s %s\n", fact.Source, fact.Relation, fact.Target)
			}
			for _, fact := range score.FalseNegatives {
				write("    FN %s %s %s\n", fact.Source, fact.Relation, fact.Target)
			}
			for _, fact := range score.DuplicateActual {
				write("    duplicate %s %s %s\n", fact.Source, fact.Relation, fact.Target)
			}
		}
		write("  unassessed relations: %s\n", strings.Join(oracle.UnassessedRelations, ", "))
		for _, limitation := range oracle.Limitations {
			write("  limitation: %s\n", limitation)
		}
		if outputErr != nil {
			return 2
		}
	}
	if strict && (!structural.Invariants.OK || !oracle.OK()) {
		return 1
	}
	return 0
}

func diagnostic(w io.Writer, format string, args ...any) {
	// A failed stderr writer cannot carry a further diagnostic.
	_, _ = fmt.Fprintf(w, format, args...)
}

func formatMetric(value *float64) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprintf("%.4f", *value)
}

func firstError(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

func parseArgs(args []string) (string, bool, bool, string, error) {
	arg := "."
	argSet := false
	jsonOutput := false
	strict := false
	oraclePath := ""
	for i := 0; i < len(args); i++ {
		value := args[i]
		switch value {
		case "--json":
			jsonOutput = true
		case "--strict":
			strict = true
		case "--oracle":
			if oraclePath != "" || i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", false, false, "", fmt.Errorf("--oracle requires one manifest path")
			}
			i++
			oraclePath = args[i]
		default:
			if argSet {
				return "", false, false, "", fmt.Errorf("unexpected argument %q", value)
			}
			arg = value
			argSet = true
		}
	}
	return arg, jsonOutput, strict, oraclePath, nil
}
