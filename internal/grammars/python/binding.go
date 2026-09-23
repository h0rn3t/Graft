// Package python is the tree-sitter-python 0.21.0 grammar, vendored from the npm package
// the TypeScript extractor loads so both builds parse identical trees.
package python

// #cgo CFLAGS: -std=c11 -fPIC
// #include "tree_sitter/parser.h"
// const TSLanguage *tree_sitter_python(void);
import "C"

import "unsafe"

// Language returns the tree-sitter language for this grammar.
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_python())
}
