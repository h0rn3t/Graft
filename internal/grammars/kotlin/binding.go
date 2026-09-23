// Package kotlin is the tree-sitter-kotlin 0.3.8 grammar, vendored from the npm package
// the TypeScript extractor loads so both builds parse identical trees.
package kotlin

// #cgo CFLAGS: -std=c11 -fPIC
// #include "tree_sitter/parser.h"
// const TSLanguage *tree_sitter_kotlin(void);
import "C"

import "unsafe"

// Language returns the tree-sitter language for this grammar.
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_kotlin())
}
