// The graph-quality command reports structural metrics for a graft wiring graph.
package main

import (
	"fmt"
	"os"

	"github.com/NanoNets/context-graph-engine/internal/graphquality"
)

func main() {
	arg, jsonOutput, strict, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	path, ok := graphquality.ResolvePath(arg)
	if !ok {
		fmt.Fprintf(os.Stderr, "no graph found at %s (run `graft build` first)\n", arg)
		os.Exit(2)
	}
	graph, err := graphquality.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read graph at %s: %v\n", path, err)
		os.Exit(2)
	}
	report := graphquality.Analyze(graph, path)
	if jsonOutput {
		data, err := report.JSON()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to encode graph report: %v\n", err)
			os.Exit(2)
		}
		fmt.Println(string(data))
	} else {
		fmt.Print(report.Human())
	}
	if strict && !report.Invariants.OK {
		os.Exit(1)
	}
}

func parseArgs(args []string) (string, bool, bool, error) {
	arg := "."
	argSet := false
	jsonOutput := false
	strict := false
	for _, value := range args {
		switch value {
		case "--json":
			jsonOutput = true
		case "--strict":
			strict = true
		default:
			if argSet {
				return "", false, false, fmt.Errorf("unexpected argument %q", value)
			}
			arg = value
			argSet = true
		}
	}
	return arg, jsonOutput, strict, nil
}
