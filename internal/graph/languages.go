package graph

import "strings"

// genericLanguages mirrors src/graph/generic.ts GENERIC_LANGS: the breadth tier
// that indexes a language through its tags query rather than a hand-written walk.
var genericLanguages = []struct {
	name       string
	extensions []string
}{
	{"rust", []string{".rs"}},
	{"java", []string{".java"}},
	{"c", []string{".c", ".h"}},
	{"cpp", []string{".cpp", ".cc", ".cxx", ".hpp", ".hh"}},
	{"ruby", []string{".rb"}},
	{"c_sharp", []string{".cs"}},
	{"scala", []string{".scala", ".sc"}},
	{"elixir", []string{".ex", ".exs"}},
	{"solidity", []string{".sol"}},
	{"ocaml", []string{".ml", ".mli"}},
	{"zig", []string{".zig"}},
	{"dart", []string{".dart"}},
	{"clojure", []string{".clj", ".cljs", ".cljc", ".bb"}},
	{"nix", []string{".nix"}},
	{"lua", []string{".lua"}},
}

// containerLanguages mirrors src/graph/container.ts CONTAINER_LANGS: files whose
// wrapper grammar locates an embedded block for the depth tier.
var containerLanguages = []struct {
	name       string
	extensions []string
}{
	{"vue", []string{".vue"}},
}

func genericLanguageOf(file string) (string, bool) {
	lower := strings.ToLower(file)
	for _, lang := range genericLanguages {
		for _, extension := range lang.extensions {
			if strings.HasSuffix(lower, extension) {
				return lang.name, true
			}
		}
	}
	return "", false
}
