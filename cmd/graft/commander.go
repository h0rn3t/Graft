package main

import (
	"embed"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// The CLI grammar follows commander v15, which the TypeScript CLI is built on,
// so the Go binary accepts the same argument vectors and rejects the rest with
// the same messages: option groups like -ix, --name=value, variadic values up
// to the next option, --no-x negation, program options anywhere, suggestions
// for misspelt options and commands, and -h/--help and help <command>.

// optionSpec is one option, parsed from its commander flags string.
type optionSpec struct {
	flags    string
	short    string
	long     string
	required bool
	optional bool
	variadic bool
	negate   bool
}

func parseOptionSpec(flags string) optionSpec {
	spec := optionSpec{flags: flags}
	parts := strings.FieldsSeq(strings.ReplaceAll(flags, ",", " "))
	for part := range parts {
		switch {
		case strings.HasPrefix(part, "--"):
			spec.long = part
		case strings.HasPrefix(part, "-"):
			spec.short = part
		case strings.HasPrefix(part, "<"):
			spec.required = true
			spec.variadic = strings.HasSuffix(part, "...>")
		case strings.HasPrefix(part, "["):
			spec.optional = true
			spec.variadic = strings.HasSuffix(part, "...]")
		}
	}
	spec.negate = strings.HasPrefix(spec.long, "--no-")
	return spec
}

// key is where the option's value is stored: the long flag, with a negated
// option sharing its positive form's key.
func (spec optionSpec) key() string {
	if spec.negate {
		return "--" + strings.TrimPrefix(spec.long, "--no-")
	}
	if spec.long != "" {
		return spec.long
	}
	return spec.short
}

func (spec optionSpec) is(arg string) bool {
	return arg == spec.short || arg == spec.long
}

// argSpec is one positional argument from a usage string.
type argSpec struct {
	name     string
	required bool
	variadic bool
}

// commandSpec is one command of the tree.
type commandSpec struct {
	name     string
	args     []argSpec
	options  []optionSpec
	commands []*commandSpec
	parent   *commandSpec
	hidden   bool
	// group commands have subcommands and no action of their own.
	group bool
}

func newCommand(usage string, options []string) *commandSpec {
	words := strings.Fields(usage)
	command := &commandSpec{}
	for _, word := range words[1:] {
		switch {
		case word == "[options]" || word == "[command]":
		case strings.HasPrefix(word, "<"):
			name := strings.Trim(word, "<>")
			command.args = append(command.args, argSpec{name: strings.TrimSuffix(name, "..."), required: true, variadic: strings.HasSuffix(name, "...")})
		case strings.HasPrefix(word, "["):
			name := strings.Trim(word, "[]")
			command.args = append(command.args, argSpec{name: strings.TrimSuffix(name, "..."), variadic: strings.HasSuffix(name, "...")})
		default:
			if command.name != "" {
				command.name += " "
			}
			command.name += word
		}
	}
	for _, flags := range options {
		command.options = append(command.options, parseOptionSpec(flags))
	}
	return command
}

// programSpec is the whole command tree, mirroring docs/cli-contract.json.
func programSpec() *commandSpec {
	program := &commandSpec{name: "graft", group: true}
	for _, flags := range []string{"-v, --version", "--dir <path>"} {
		program.options = append(program.options, parseOptionSpec(flags))
	}
	add := func(parent *commandSpec, usage string, options ...string) *commandSpec {
		command := newCommand(usage, options)
		command.name = command.name[strings.LastIndex(command.name, " ")+1:]
		command.parent = parent
		parent.commands = append(parent.commands, command)
		return command
	}
	add(program, "graft _hook [options] <sub>").hidden = true
	add(program, "graft _statusline [options]").hidden = true
	add(program, "graft _sync-run [options] <dir>").hidden = true
	add(program, "graft version [options]")
	add(program, "graft build [options] [dir]", "-e, --extensions <exts...>", "--no-reuse", "--lsp", "--no-lsp",
		"--follow-submodules", "--no-follow-submodules", "--follow-nested-repos", "--no-follow-nested-repos",
		"--include-dir <name>", "--only-dir <path>", "--no-gitignore", "--no-ignore")
	add(program, "graft ask [options] <query> [dir]", "-n, --limit <n>", "--source", "--full", "--budget <tokens>", "--intent <lookup|edit>", "--in <path>", "--json", "--no-graph-rank", "--no-refresh")
	add(program, "graft skeleton [options] <file> [dir]", "--json", "--no-refresh")
	add(program, "graft read [options] <symbol> [dir]", "--also <symbol>", "--budget <tokens>", "--json", "--no-refresh")
	add(program, "graft check [options] [dir]", "-e, --extensions <exts...>", "--json")
	add(program, "graft stats [options] [dir]", "--json")
	add(program, "graft mcp [options] [dir]")
	add(program, "graft callers [options] <symbol> [dir]", "--direction <in|out>", "-d, --depth <n>", "--in <path>", "--json", "--no-refresh")
	add(program, "graft blast [options] [dir]", "--base <ref>", "-d, --depth <n>", "--format <fmt>",
		"--no-owners", "--pr-author <who...>", "--no-refresh")
	add(program, "graft grep [options] <pattern> [dir]", "-i, --ignore-case", "--fixed", "--in <path>", "--json", "--no-refresh")
	add(program, "graft map [options] [dir]", "--max-dirs <n>", "--json", "--no-refresh")
	add(program, "graft init [options] [dir]", "--no-build", "--agents <ids...>", "--all-agents", "--no-agents", "--list-agents", "--no-mcp",
		"--no-hooks", "--no-statusline", "--dry-run", "-y, --yes", "--no-global")
	add(program, "graft uninstall [options] [dir]", "-y, --yes", "--keep-cache", "--no-global")
	return program
}

// path is the command's name from the program down.
func (command *commandSpec) path() string {
	if command.parent == nil || command.parent.parent == nil {
		return command.name
	}
	return command.parent.path() + " " + command.name
}

func (command *commandSpec) findCommand(name string) *commandSpec {
	for _, sub := range command.commands {
		if sub.name == name {
			return sub
		}
	}
	return nil
}

func (command *commandSpec) findOption(arg string) (optionSpec, bool) {
	for _, option := range command.options {
		if option.is(arg) {
			return option, true
		}
	}
	return optionSpec{}, false
}

// cliError is a parse failure, printed to stderr with exit status 1.
type cliError struct {
	message string
}

func (err *cliError) Error() string {
	return err.message
}

// helpRequest asks for command's help on stdout (exit 0) or stderr (exit 1).
type helpRequest struct {
	command *commandSpec
	toErr   bool
}

func (request *helpRequest) Error() string {
	return "help requested"
}

// versionRequest asks for the version to be printed.
type versionRequest struct{}

func (versionRequest) Error() string {
	return "version requested"
}

// parsedFlags holds the options and positionals of one command line.
type parsedFlags struct {
	bools       map[string]bool
	values      map[string][]string
	positionals []string
}

// value is the last value given for an option.
func (parsed parsedFlags) value(name string) (string, bool) {
	values, ok := parsed.values[name]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[len(values)-1], true
}

// dir is the optional [dir] argument, defaulting to ".".
func (parsed parsedFlags) dir() string {
	if len(parsed.positionals) == 0 {
		return "."
	}
	return parsed.positionals[len(parsed.positionals)-1]
}

// invocation is a parsed command line.
type invocation struct {
	command *commandSpec
	args    []string
	flags   parsedFlags
}

var negativeNumber = regexp.MustCompile(`^-(\d+|\d*\.\d+)(e[+-]?\d+)?$`)

func maybeOption(arg string) bool {
	return len(arg) > 1 && arg[0] == '-'
}

// parseOptions is commander's Command.parseOptions: it consumes the options
// command knows, wherever they appear, and splits the rest into operands and
// the first unknown option with everything after it.
func (command *commandSpec) parseOptions(args []string, flags parsedFlags) ([]string, []string, error) {
	operands, unknown := []string{}, []string{}
	toUnknown := false
	push := func(values ...string) {
		if toUnknown {
			unknown = append(unknown, values...)
		} else {
			operands = append(operands, values...)
		}
	}
	var variadic *optionSpec
	group := ""
	for i := 0; i < len(args) || group != ""; {
		arg := group
		if arg == "" {
			arg = args[i]
			i++
		}
		group = ""
		if arg == "--" {
			if toUnknown {
				unknown = append(unknown, arg)
			}
			push(args[i:]...)
			break
		}
		if variadic != nil && (!maybeOption(arg) || negativeNumber.MatchString(arg)) {
			flags.set(*variadic, arg)
			continue
		}
		variadic = nil
		if maybeOption(arg) {
			if option, ok := command.findOption(arg); ok {
				switch {
				case option.required:
					if i >= len(args) {
						return nil, nil, &cliError{fmt.Sprintf("error: option '%s' argument missing", option.flags)}
					}
					flags.set(option, args[i])
					i++
				case option.optional:
					value := ""
					if i < len(args) && (!maybeOption(args[i]) || negativeNumber.MatchString(args[i])) {
						value = args[i]
						i++
					}
					flags.set(option, value)
				default:
					if option.long == "--version" {
						return nil, nil, versionRequest{}
					}
					flags.set(option, "")
				}
				if option.variadic {
					variadic = &option
				}
				continue
			}
		}
		if len(arg) > 2 && arg[0] == '-' && arg[1] != '-' {
			if option, ok := command.findOption(arg[:2]); ok {
				if option.required {
					flags.set(option, arg[2:])
				} else {
					if option.long == "--version" {
						return nil, nil, versionRequest{}
					}
					flags.set(option, "")
					group = "-" + arg[2:]
				}
				continue
			}
		}
		if strings.HasPrefix(arg, "--") && strings.Contains(arg, "=") && strings.Index(arg, "=") > 2 {
			at := strings.Index(arg, "=")
			if option, ok := command.findOption(arg[:at]); ok && (option.required || option.optional) {
				flags.set(option, arg[at+1:])
				continue
			}
		}
		if !toUnknown && maybeOption(arg) && (len(command.commands) != 0 || !negativeNumber.MatchString(arg)) {
			toUnknown = true
		}
		push(arg)
	}
	return operands, unknown, nil
}

// set records one option occurrence: a flag, a value, or one value of a
// variadic option. A negated flag stores false under the positive key.
func (flags parsedFlags) set(option optionSpec, value string) {
	key := option.key()
	switch {
	case option.negate:
		flags.bools[option.long] = true
		delete(flags.bools, key)
		delete(flags.values, key)
	case option.required || option.optional:
		if option.variadic || repeatableOptions[key] {
			flags.values[key] = append(flags.values[key], value)
		} else {
			flags.values[key] = []string{value}
		}
		for _, negated := range []string{"--no-" + strings.TrimPrefix(key, "--")} {
			delete(flags.bools, negated)
		}
	default:
		flags.bools[key] = true
		delete(flags.bools, "--no-"+strings.TrimPrefix(key, "--"))
	}
}

// repeatableOptions collect every occurrence, as the TypeScript CLI's
// accumulating argument parsers do.
var repeatableOptions = map[string]bool{"--include-dir": true, "--only-dir": true, "--also": true}

func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "--help"
}

// parseCommandLine is commander's _parseCommand, from the program down.
func parseCommandLine(program *commandSpec, argv []string) (invocation, error) {
	flags := parsedFlags{bools: make(map[string]bool), values: make(map[string][]string)}
	command := program
	operands, unknown := []string{}, argv
	for {
		parsedOperands, parsedUnknown, err := command.parseOptions(unknown, flags)
		if err != nil {
			return invocation{}, err
		}
		operands = append(operands, parsedOperands...)
		unknown = parsedUnknown
		if len(operands) > 0 {
			if sub := command.findCommand(operands[0]); sub != nil {
				command, operands = sub, operands[1:]
				continue
			}
		}
		if len(command.commands) > 0 && len(operands) > 0 && operands[0] == "help" {
			if len(operands) < 2 {
				return invocation{}, &helpRequest{command: command}
			}
			if sub := command.findCommand(operands[1]); sub != nil {
				return invocation{}, &helpRequest{command: sub}
			}
			return invocation{}, &helpRequest{command: command, toErr: true}
		}
		all := append(slices.Clone(operands), unknown...)
		if len(command.commands) > 0 && len(all) == 0 && command.group {
			return invocation{}, &helpRequest{command: command, toErr: true}
		}
		if slices.ContainsFunc(unknown, isHelpFlag) {
			return invocation{}, &helpRequest{command: command}
		}
		if command.group {
			if len(operands) > 0 {
				return invocation{}, command.unknownCommand(operands[0])
			}
			if len(unknown) > 0 {
				return invocation{}, command.unknownOption(unknown[0])
			}
			return invocation{}, &helpRequest{command: command, toErr: true}
		}
		if len(unknown) > 0 {
			return invocation{}, command.unknownOption(unknown[0])
		}
		if err := command.checkArguments(all); err != nil {
			return invocation{}, err
		}
		flags.positionals = all
		return invocation{command: command, args: all, flags: flags}, nil
	}
}

func (command *commandSpec) checkArguments(args []string) error {
	for index, arg := range command.args {
		if arg.required && index >= len(args) {
			return &cliError{fmt.Sprintf("error: missing required argument '%s'", arg.name)}
		}
	}
	if len(command.args) > 0 && command.args[len(command.args)-1].variadic {
		return nil
	}
	if len(args) > len(command.args) {
		s := "s"
		if len(command.args) == 1 {
			s = ""
		}
		return &cliError{fmt.Sprintf("error: too many arguments for '%s'. Expected %d argument%s but got %d: %s.",
			command.name, len(command.args), s, len(args), strings.Join(args, ", "))}
	}
	return nil
}

func (command *commandSpec) unknownOption(flag string) error {
	suggestion := ""
	if strings.HasPrefix(flag, "--") {
		candidates := make([]string, 0)
		for current := command; current != nil; current = current.parent {
			for _, option := range current.options {
				if option.long != "" {
					candidates = append(candidates, option.long)
				}
			}
			candidates = append(candidates, "--help")
		}
		suggestion = suggestSimilar(flag, candidates)
	}
	return &cliError{fmt.Sprintf("error: unknown option '%s'%s", flag, suggestion)}
}

func (command *commandSpec) unknownCommand(name string) error {
	candidates := make([]string, 0, len(command.commands)+1)
	for _, sub := range command.commands {
		if !sub.hidden {
			candidates = append(candidates, sub.name)
		}
	}
	candidates = append(candidates, "help")
	return &cliError{fmt.Sprintf("error: unknown command '%s'%s", name, suggestSimilar(name, candidates))}
}

// editDistance is commander's optimal-string-alignment distance.
func editDistance(a, b []rune) int {
	const maxDistance = 3
	if int(math.Abs(float64(len(a)-len(b)))) > maxDistance {
		return max(len(a), len(b))
	}
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for j := 1; j <= len(b); j++ {
		for i := 1; i <= len(a); i++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

// suggestSimilar is commander's suggestSimilar.
func suggestSimilar(word string, candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	unique := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if !slices.Contains(unique, candidate) {
			unique = append(unique, candidate)
		}
	}
	options := strings.HasPrefix(word, "--")
	if options {
		word = word[2:]
		for i := range unique {
			unique[i] = unique[i][2:]
		}
	}
	similar := make([]string, 0)
	best := 3
	for _, candidate := range unique {
		if len([]rune(candidate)) <= 1 {
			continue
		}
		distance := editDistance([]rune(word), []rune(candidate))
		length := max(len([]rune(word)), len([]rune(candidate)))
		if float64(length-distance)/float64(length) <= 0.4 {
			continue
		}
		switch {
		case distance < best:
			best, similar = distance, []string{candidate}
		case distance == best:
			similar = append(similar, candidate)
		}
	}
	compare := localeCompareStrings()
	slices.SortFunc(similar, compare)
	if options {
		for i := range similar {
			similar[i] = "--" + similar[i]
		}
	}
	switch len(similar) {
	case 0:
		return ""
	case 1:
		return "\n(Did you mean " + similar[0] + "?)"
	default:
		return "\n(Did you mean one of " + strings.Join(similar, ", ") + "?)"
	}
}

//go:embed help
var helpTexts embed.FS

// helpText is commander's help for command, generated from the TypeScript CLI.
func helpText(command *commandSpec) string {
	name := "graft"
	if command.parent != nil {
		name = strings.ReplaceAll(command.path(), " ", "-")
	}
	data, err := helpTexts.ReadFile("help/" + name + ".txt")
	if err != nil {
		return ""
	}
	return string(data)
}

// localeCompareStrings matches JavaScript's default String.prototype.localeCompare.
func localeCompareStrings() func(a, b string) int {
	return collate.New(language.English).CompareString
}
