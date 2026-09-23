package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestGrammarMatchesTheCLIContract checks the Go command tree against
// docs/cli-contract.json. The only differences are the invocations the
// contract routes to TypeScript: build --deep, viz, and blast --export-viz.
func TestGrammarMatchesTheCLIContract(t *testing.T) {
	data, err := os.ReadFile("../../docs/cli-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		GlobalOptions []string `json:"globalOptions"`
		Commands      []struct {
			Name    string   `json:"name"`
			Usage   string   `json:"usage"`
			Options []string `json:"options"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	program := programSpec()
	globals := make([]string, 0)
	for _, option := range program.options[1:] {
		globals = append(globals, option.flags)
	}
	if !slices.Equal(globals, contract.GlobalOptions) {
		t.Errorf("global options = %q, contract = %q", globals, contract.GlobalOptions)
	}
	typescriptOnly := map[string][]string{"build": {"--deep"}, "blast": {"--export-viz <dir>"}}
	seen := make([]string, 0)
	for _, entry := range contract.Commands {
		if entry.Name == "viz" {
			continue
		}
		command := program
		for word := range strings.FieldsSeq(entry.Name) {
			if command = command.findCommand(word); command == nil {
				t.Fatalf("contract command %q is missing from the Go grammar", entry.Name)
			}
		}
		seen = append(seen, entry.Name)
		want := slices.DeleteFunc(slices.Clone(entry.Options), func(flags string) bool { return slices.Contains(typescriptOnly[entry.Name], flags) })
		got := make([]string, 0, len(command.options))
		for _, option := range command.options {
			got = append(got, option.flags)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s options = %q, contract = %q", entry.Name, got, want)
		}
		usage := newCommand(entry.Usage, nil)
		if len(usage.args) != len(command.args) {
			t.Errorf("%s arguments = %+v, contract usage %q", entry.Name, command.args, entry.Usage)
			continue
		}
		for i, arg := range usage.args {
			if arg != command.args[i] {
				t.Errorf("%s argument %d = %+v, contract %+v", entry.Name, i, command.args[i], arg)
			}
		}
	}
	for _, command := range program.commands {
		if !slices.ContainsFunc(seen, func(name string) bool { return strings.HasPrefix(name, command.name) }) {
			t.Errorf("Go command %q is not in the contract", command.name)
		}
	}
}
