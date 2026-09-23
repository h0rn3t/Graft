package main

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func patchBuildConfig(root string, opts callersOptions) error {
	if len(opts.includeDirs) == 0 && opts.followSubmodules == nil && opts.followNestedRepos == nil {
		return nil
	}
	path := filepath.Join(root, ".graft", "config.json")
	config := make(map[string]any)
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &config) != nil || config == nil {
			config = make(map[string]any)
		}
	}
	if len(opts.includeDirs) > 0 {
		config["includeDirs"] = opts.includeDirs
	}
	if opts.followSubmodules != nil {
		config["followSubmodules"] = *opts.followSubmodules
	}
	if opts.followNestedRepos != nil {
		config["followNestedRepos"] = *opts.followNestedRepos
	}
	data, err := jsonv2.Marshal(config, jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("encode build configuration: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create build configuration directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "config.json.*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary build configuration: %w", err)
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary build configuration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary build configuration: %w", err)
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("replace build configuration: %w", err)
	}
	ignorePath := filepath.Join(root, ".gitignore")
	ignored, _ := os.ReadFile(ignorePath) // Missing .gitignore starts empty.
	current := string(ignored)
	for line := range strings.SplitSeq(current, "\n") {
		entry := strings.TrimSpace(line)
		if entry == ".graft" || entry == ".graft/" || entry == "/.graft/" {
			return nil
		}
	}
	gap := ""
	if current != "" {
		gap = "\n"
		if !strings.HasSuffix(current, "\n") {
			gap = "\n\n"
		}
	}
	_ = os.WriteFile(ignorePath, []byte(current+gap+"# graft's local repository settings — not committed.\n/.graft/\n"), 0o644) // Best-effort convenience file.
	return nil
}
