package upkeep

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

//go:embed templates/gemini-instructions.md
var wiringTemplates embed.FS

type wiringHostTarget struct {
	id      string
	path    string
	section bool
}

// WiredHostIDs lists the registered hosts whose target files are present.
func WiredHostIDs(repo string) []string {
	var hosts []string
	if _, err := os.Stat(filepath.Join(repo, ".claude", "helpers", "graft-hooks.cjs")); err == nil {
		hosts = append(hosts, "claude")
	}
	for _, host := range wiringHostTargets() {
		path := filepath.Join(repo, host.path)
		if host.section {
			data, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(data), "<!-- graft:start -->") {
				continue
			}
		} else if _, err := os.Stat(path); err != nil {
			continue
		}
		hosts = append(hosts, host.id)
	}
	return hosts
}

// RewriteWiring updates the Gemini host targets. It rejects other hosts before
// writing, so ReconcileWiring leaves their stamp stale for a later native slice.
func RewriteWiring(ctx context.Context, repo string, hosts []string, options WiringOptions) error {
	if ctx == nil {
		return errors.New("rewrite wiring requires a context")
	}
	for _, host := range hosts {
		if host != "gemini" {
			return fmt.Errorf("unsupported wiring host %q", host)
		}
	}
	if !slices.Contains(hosts, "gemini") {
		return nil
	}
	body, err := wiringTemplates.ReadFile("templates/gemini-instructions.md")
	if err != nil {
		return fmt.Errorf("read Gemini instructions: %w", err)
	}
	if err := upsertManagedSection(filepath.Join(repo, "GEMINI.md"), strings.TrimSuffix(string(body), "\n"), "<!-- graft:start -->", "<!-- graft:end -->"); err != nil {
		return fmt.Errorf("rewrite Gemini instructions: %w", err)
	}
	if !options.MCP {
		return nil
	}
	command, args := geminiMCPServerEntry(ctx, repo)
	return mergeGeminiMCPConfig(filepath.Join(repo, ".gemini", "settings.json"), command, args)
}

func wiringHostTargets() []wiringHostTarget {
	return []wiringHostTarget{
		{id: "agents", path: "AGENTS.md", section: true},
		{id: "adal", path: filepath.Join(".adal", "skills", "graft", "SKILL.md")},
		{id: "cursor", path: filepath.Join(".cursor", "rules", "graft.mdc")},
		{id: "gemini", path: "GEMINI.md", section: true},
		{id: "grok", path: filepath.Join(".grok", "skills", "graft", "SKILL.md")},
		{id: "hermes", path: "AGENTS.md", section: true},
		{id: "antigravity", path: "AGENTS.md", section: true},
		{id: "copilot", path: filepath.Join(".github", "copilot-instructions.md"), section: true},
		{id: "kiro", path: filepath.Join(".kiro", "steering", "graft.md")},
		{id: "windsurf", path: filepath.Join(".windsurf", "rules", "graft.md")},
	}
}

func upsertManagedSection(path, body, startMarker, endMarker string) error {
	block := startMarker + "\n" + strings.TrimSpace(strings.ReplaceAll(body, "\r", "")) + "\n" + endMarker
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(block+"\n"), 0o644)
	}
	if err != nil {
		return err
	}
	text := string(data)
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	startIndex, endIndex := -1, -1
	for index, line := range lines {
		if strings.TrimSpace(line) == startMarker {
			startIndex = index
			for markerIndex := index + 1; markerIndex < len(lines); markerIndex++ {
				if strings.TrimSpace(lines[markerIndex]) == endMarker {
					endIndex = markerIndex
					break
				}
			}
			break
		}
	}
	if startIndex >= 0 && endIndex >= 0 {
		if strings.Join(lines[startIndex:endIndex+1], "\n") == block {
			return nil
		}
		updated := slices.Concat(
			lines[:startIndex],
			strings.Split(strings.ReplaceAll(block, "\n", eol), eol),
			lines[endIndex+1:],
		)
		return os.WriteFile(path, []byte(strings.Join(updated, eol)), 0o644)
	}
	separator := eol + eol
	if strings.HasSuffix(text, separator) {
		separator = ""
	} else if strings.HasSuffix(text, eol) {
		separator = eol
	}
	return os.WriteFile(path, []byte(text+separator+strings.ReplaceAll(block, "\n", eol)+eol), 0o644)
}

func geminiMCPServerEntry(ctx context.Context, repo string) (string, []string) {
	forced := os.Getenv("GRAFT_MCP_NPX")
	if forced != "" && forced != "0" && forced != "false" {
		return "npx", []string{"-y", "@nanonets/graft", "mcp"}
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(check, "graft", "--version")
	command.Dir = repo
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if command.Run() == nil {
		return "graft", []string{"mcp"}
	}
	return "npx", []string{"-y", "@nanonets/graft", "mcp"}
}

func mergeGeminiMCPConfig(path, command string, args []string) error {
	data, err := os.ReadFile(path)
	root := make(map[string]any)
	if err == nil {
		if json.Unmarshal(data, &root) != nil || root == nil {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if root["mcpServers"] == nil {
		servers = make(map[string]any)
		root["mcpServers"] = servers
	} else if !ok {
		return nil
	}
	wantArgs := make([]any, len(args))
	for index, arg := range args {
		wantArgs[index] = arg
	}
	if existing, ok := servers["graft"].(map[string]any); ok && len(existing) == 2 && existing["command"] == command {
		if oldArgs, ok := existing["args"].([]any); ok && slices.Equal(oldArgs, wantArgs) {
			return nil
		}
	}
	servers["graft"] = map[string]any{"command": command, "args": args}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err = json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
