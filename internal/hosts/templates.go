// Package hosts wires graft into AI coding hosts: instruction files, MCP
// registrations, hook configs, and the Claude Code layer, planned once so a dry
// run and a real run touch the same files, and retracted by the same lists.
package hosts

import (
	"embed"
	"strings"

	"github.com/h0rn3t/Graft/internal/jsonjs"
)

// The templates are generated from the TypeScript sources and checked against
// them by test/host-templates-go.test.ts.
//
//go:embed templates/instructions.md templates/skill.md templates/hooks-shim.cjs templates/statusline-shim.cjs
var templates embed.FS

const bakedPlaceholder = `"@@BAKED@@"`

func template(name string) string {
	data, err := templates.ReadFile("templates/" + name)
	if err != nil {
		panic("hosts: missing embedded template " + name)
	}
	return string(data)
}

// InstructionBody is the canonical graft instruction block.
func InstructionBody() string {
	return strings.TrimSuffix(template("instructions.md"), "\n")
}

// CursorRule wraps the instruction block as an always-applied Cursor rule.
func CursorRule() string {
	return "---\ndescription: Use the Graft context graph in graft/ before exploring source\nalwaysApply: true\n---\n" + InstructionBody() + "\n"
}

// KiroSteering wraps the instruction block as an always-included Kiro steering file.
func KiroSteering() string {
	return "---\ninclusion: always\n---\n" + InstructionBody() + "\n"
}

// WindsurfRule is the instruction block as a Windsurf rule.
func WindsurfRule() string {
	return InstructionBody() + "\n"
}

// SkillTemplate is the graft skill card shared by Claude Code, AdaL, Grok, and Antigravity.
func SkillTemplate() string {
	return template("skill.md")
}

// HooksShim is the hook shim that starts the installed native package,
// with bakedDir (the package root) as its first candidate.
func HooksShim(bakedDir string) string {
	return strings.Replace(template("hooks-shim.cjs"), bakedPlaceholder, jsonjs.Quote(bakedDir), 1)
}

// StatuslineShim is the statusline shim, resolved the same way as HooksShim.
func StatuslineShim(bakedDir string) string {
	return strings.Replace(template("statusline-shim.cjs"), bakedPlaceholder, jsonjs.Quote(bakedDir), 1)
}
