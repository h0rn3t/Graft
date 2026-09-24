package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type cliContractCommand struct {
	Name    string   `json:"name"`
	Usage   string   `json:"usage"`
	Backend string   `json:"backend"`
	Hidden  bool     `json:"hidden"`
	Options []string `json:"options"`
}

type cliContract struct {
	Source        json.RawMessage           `json:"source"`
	Exceptions    json.RawMessage           `json:"exceptions"`
	GlobalOptions []string                  `json:"globalOptions"`
	Commands      []cliContractCommand      `json:"commands"`
	Environment   map[string]map[string]any `json:"environment"`
}

func readCLIContract(t *testing.T) cliContract {
	t.Helper()
	data, err := os.ReadFile("../../docs/cli-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract cliContract
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	return contract
}

func TestGrammarMatchesTheCLIContract(t *testing.T) {
	contract := readCLIContract(t)
	if len(contract.Source) != 0 || len(contract.Exceptions) != 0 {
		t.Errorf("legacy contract fields source = %s, exceptions = %s, want absent", contract.Source, contract.Exceptions)
	}

	program := programSpec()
	globals := make([]string, 0, len(program.options)-1)
	for _, option := range program.options[1:] {
		globals = append(globals, option.flags)
	}
	if !slices.Equal(globals, contract.GlobalOptions) {
		t.Errorf("global options = %q, contract = %q", globals, contract.GlobalOptions)
	}

	commands := make(map[string]*commandSpec)
	var collect func(*commandSpec)
	collect = func(parent *commandSpec) {
		for _, command := range parent.commands {
			if !command.group {
				commands[command.path()] = command
			}
			collect(command)
		}
	}
	collect(program)
	if len(contract.Commands) != len(commands) {
		t.Errorf("contract commands = %d, Go commands = %d (%q)", len(contract.Commands), len(commands), slices.Sorted(maps.Keys(commands)))
	}

	seen := make(map[string]struct{}, len(contract.Commands))
	for _, entry := range contract.Commands {
		if _, duplicate := seen[entry.Name]; duplicate {
			t.Errorf("contract command %q is duplicated", entry.Name)
		}
		seen[entry.Name] = struct{}{}
		command, ok := commands[entry.Name]
		if !ok {
			t.Errorf("contract command %q is missing from the Go grammar", entry.Name)
			continue
		}
		if entry.Backend != "go" {
			t.Errorf("%s backend = %q, want go", entry.Name, entry.Backend)
		}
		if command.hidden != entry.Hidden {
			t.Errorf("%s hidden = %t, contract = %t", entry.Name, command.hidden, entry.Hidden)
		}
		gotOptions := make([]string, 0, len(command.options))
		for _, option := range command.options {
			gotOptions = append(gotOptions, option.flags)
		}
		if !slices.Equal(gotOptions, entry.Options) {
			t.Errorf("%s options = %q, contract = %q", entry.Name, gotOptions, entry.Options)
		}
		usage := newCommand(entry.Usage, nil)
		if !slices.Equal(usage.args, command.args) {
			t.Errorf("%s arguments = %+v, contract usage %q = %+v", entry.Name, command.args, entry.Usage, usage.args)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		if _, ok := seen[name]; !ok {
			t.Errorf("Go command %q is not in the contract", name)
		}
	}
}

func TestEnvironmentMatchesGoReads(t *testing.T) {
	contract := readCLIContract(t)
	got := goEnvironmentReads(t)
	want := slices.Sorted(maps.Keys(contract.Environment))
	if !slices.Equal(got, want) {
		t.Errorf("Go environment reads = %q, contract = %q", got, want)
	}
}

func goEnvironmentReads(t *testing.T) []string {
	t.Helper()
	read := make(map[string]struct{})
	addString := func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		name, err := strconv.Unquote(literal.Value)
		if err == nil {
			read[name] = struct{}{}
		}
		return true
	}
	for _, root := range []string{"../../cmd/graft", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.CallExpr:
					if selector, ok := node.Fun.(*ast.SelectorExpr); ok {
						pkg, pkgOK := selector.X.(*ast.Ident)
						if pkgOK && pkg.Name == "os" && (selector.Sel.Name == "Getenv" || selector.Sel.Name == "LookupEnv") && len(node.Args) == 1 {
							if literal, ok := node.Args[0].(*ast.BasicLit); ok && literal.Kind == token.STRING {
								addString(literal)
							}
						}
					}
					if helper, ok := node.Fun.(*ast.Ident); ok && helper.Name == "envTruthy" {
						for _, arg := range node.Args {
							ast.Inspect(arg, addString)
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("scan Go environment reads under %q error = %v", root, err)
		}
	}
	return slices.Sorted(maps.Keys(read))
}
