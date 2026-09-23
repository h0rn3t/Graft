package hosts

import (
	"os"
	"path/filepath"
	"slices"
)

// Kind says how graft writes a host's instruction file.
type Kind string

const (
	// KindSection upserts a fenced block into a file the user owns.
	KindSection Kind = "section"
	// KindOwned overwrites a file graft owns.
	KindOwned Kind = "owned"
)

// Host is one AI coding host graft writes instructions for.
type Host struct {
	ID      string
	Name    string
	Kind    Kind
	RelPath string
	Content func() string
	detect  func(home, repo string) bool
}

// Detect reports whether the host looks installed for home or used in repo.
func (host Host) Detect(home, repo string) bool {
	return host.detect(home, repo)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func anyDir(paths ...string) bool {
	return slices.ContainsFunc(paths, dirExists)
}

// Hosts is the registry, in the order the picker and plans list it.
func Hosts() []Host {
	return []Host{
		{
			ID: "agents", Name: "AGENTS.md hosts (Codex-style CLIs, editors that read AGENTS.md)", Kind: KindSection, RelPath: "AGENTS.md", Content: InstructionBody,
			detect: func(home, _ string) bool {
				return anyDir(filepath.Join(home, ".codex"), filepath.Join(home, ".config", "opencode"), filepath.Join(home, ".config", "agents"))
			},
		},
		{
			ID: "adal", Name: "AdaL", Kind: KindOwned, RelPath: filepath.Join(".adal", "skills", "graft", "SKILL.md"), Content: SkillTemplate,
			detect: func(home, repo string) bool {
				return anyDir(filepath.Join(home, ".adal"), filepath.Join(repo, ".adal"))
			},
		},
		{
			ID: "cursor", Name: "Cursor", Kind: KindOwned, RelPath: filepath.Join(".cursor", "rules", "graft.mdc"), Content: CursorRule,
			detect: func(home, repo string) bool {
				return anyDir(filepath.Join(home, ".cursor"), filepath.Join(repo, ".cursor"))
			},
		},
		{
			ID: "gemini", Name: "Gemini CLI", Kind: KindSection, RelPath: "GEMINI.md", Content: InstructionBody,
			detect: func(home, _ string) bool { return dirExists(filepath.Join(home, ".gemini")) },
		},
		{
			ID: "grok", Name: "Grok (xAI)", Kind: KindOwned, RelPath: filepath.Join(".grok", "skills", "graft", "SKILL.md"), Content: SkillTemplate,
			detect: func(home, repo string) bool {
				return anyDir(filepath.Join(home, ".grok"), filepath.Join(repo, ".grok"))
			},
		},
		{
			ID: "hermes", Name: "Hermes Agent (Nous Research)", Kind: KindSection, RelPath: "AGENTS.md", Content: InstructionBody,
			detect: func(home, repo string) bool {
				return anyDir(filepath.Join(home, ".hermes"), filepath.Join(home, "AppData", "Local", "hermes"), filepath.Join(repo, ".hermes"))
			},
		},
		{
			ID: "antigravity", Name: "Google Antigravity", Kind: KindSection, RelPath: "AGENTS.md", Content: InstructionBody,
			detect: func(home, repo string) bool {
				return anyDir(filepath.Join(home, ".gemini", "config"), filepath.Join(home, ".gemini", "antigravity-cli"), filepath.Join(repo, ".agents"))
			},
		},
		{
			ID: "copilot", Name: "GitHub Copilot", Kind: KindSection, RelPath: filepath.Join(".github", "copilot-instructions.md"), Content: InstructionBody,
			detect: func(_, repo string) bool { return dirExists(filepath.Join(repo, ".github")) },
		},
		{
			ID: "kiro", Name: "Kiro", Kind: KindOwned, RelPath: filepath.Join(".kiro", "steering", "graft.md"), Content: KiroSteering,
			detect: func(home, repo string) bool {
				return anyDir(filepath.Join(home, ".kiro"), filepath.Join(repo, ".kiro"))
			},
		},
		{
			ID: "windsurf", Name: "Windsurf", Kind: KindOwned, RelPath: filepath.Join(".windsurf", "rules", "graft.md"), Content: WindsurfRule,
			detect: func(home, repo string) bool {
				return anyDir(filepath.Join(home, ".codeium", "windsurf"), filepath.Join(repo, ".windsurf"))
			},
		},
	}
}

// HostIDs lists the registry ids in order.
func HostIDs() []string {
	hosts := Hosts()
	ids := make([]string, len(hosts))
	for i, host := range hosts {
		ids[i] = host.ID
	}
	return ids
}

// DetectHosts returns the hosts that look installed for home or used in repo.
func DetectHosts(home, repo string) []Host {
	detected := make([]Host, 0)
	for _, host := range Hosts() {
		if host.Detect(home, repo) {
			detected = append(detected, host)
		}
	}
	return detected
}

// HostByID finds a registry entry.
func HostByID(id string) (Host, bool) {
	for _, host := range Hosts() {
		if host.ID == id {
			return host, true
		}
	}
	return Host{}, false
}
