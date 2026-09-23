// Package swift is the tree-sitter-swift 0.7.1 grammar, vendored from the npm package
// the TypeScript extractor loads so both builds parse identical trees.
package swift

// #cgo CFLAGS: -std=c11 -fPIC
// #include "tree_sitter/parser.h"
// const TSLanguage *tree_sitter_swift(void);
import "C"

import "unsafe"

// Language returns the tree-sitter language for this grammar.
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_swift())
}
