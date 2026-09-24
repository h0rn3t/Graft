package graph

import "strings"

// genericLanguages indexes languages through their tags queries rather than a
// hand-written extraction walk.
var genericLanguages = []struct {
	name       string
	extensions []string
}{
	{"rust", []string{".rs"}},
	{"c", []string{".c", ".h"}},
	{"cpp", []string{".cpp", ".cc", ".cxx", ".hpp", ".hh"}},
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
