package main

import "github.com/h0rn3t/Graft/internal/repoconfig"

// patchBuildConfig records the build flags that persist per repository in
// .graft/config.json, keeping the existing keys in their order, and makes
// sure .gitignore covers the directory.
func patchBuildConfig(root string, opts callersOptions) error {
	fields := make([]repoconfig.Field, 0, 3)
	if len(opts.includeDirs) > 0 {
		fields = append(fields, repoconfig.Field{Key: "includeDirs", Value: opts.includeDirs})
	}
	if opts.followSubmodules != nil {
		fields = append(fields, repoconfig.Field{Key: "followSubmodules", Value: *opts.followSubmodules})
	}
	if opts.followNestedRepos != nil {
		fields = append(fields, repoconfig.Field{Key: "followNestedRepos", Value: *opts.followNestedRepos})
	}
	if len(fields) == 0 {
		return nil
	}
	// The caller reports a failure as "build configuration write failed".
	return repoconfig.Patch(root, fields)
}
