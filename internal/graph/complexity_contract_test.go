package graph

import (
	"slices"
	"testing"
)

func TestExtractCyclomaticComplexity(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		source string
		id     string
		want   int // 0: the node carries no complexity
	}{
		{
			name: "go branches, logical operators and non-default cases",
			file: "f.go",
			source: "package p\nfunc f(a, b int) int {\n\tif a > 0 && b > 0 {\n\t\treturn 1\n\t} else if a < 0 {\n\t\treturn 2\n\t}\n" +
				"\tfor i := range a {\n\t\t_ = i\n\t}\n\tswitch a {\n\tcase 1:\n\tcase 2:\n\tdefault:\n\t}\n\treturn 0\n}\n",
			id:   "f.go#f",
			want: 7,
		},
		{
			name:   "go closure counts toward its function",
			file:   "g.go",
			source: "package p\nfunc g() {\n\th := func() {\n\t\tif true {\n\t\t}\n\t}\n\th()\n}\n",
			id:     "g.go#g",
			want:   2,
		},
		{
			name:   "go interface method has no body",
			file:   "i.go",
			source: "package p\ntype I interface{ Run() error }\n",
			id:     "i.go#I.Run",
		},
		{
			name: "python counts comprehension clauses but not with",
			file: "f.py",
			source: "def f(a):\n    if a and b:\n        pass\n    elif a:\n        pass\n    x = [i for i in a if i]\n" +
				"    with a:\n        pass\n    try:\n        pass\n    except E:\n        pass\n    return 1 if a else 2\n",
			id:   "f.py#f",
			want: 8,
		},
		{
			name:   "python nested def is its own node",
			file:   "n.py",
			source: "def outer():\n    if a:\n        pass\n    def inner():\n        if b:\n            pass\n        if c:\n            pass\n",
			id:     "n.py#outer",
			want:   2,
		},
		{
			name:   "typescript",
			file:   "f.ts",
			source: "function f(a) { if ((a && b) || (c ?? d)) {} for (;;) {} switch (a) { case 1: break; default: } try {} catch (e) {} return a ? 1 : 2; }\n",
			id:     "f.ts#f",
			want:   9,
		},
		{
			name:   "java",
			file:   "C.java",
			source: "class C { int f(int a) { if (a > 0 && a < 2) {} for (int x : xs) {} switch (a) { case 1: break; default: } try {} catch (E e) {} return a > 0 ? 1 : 2; } }\n",
			id:     "C.java#C.f",
			want:   7,
		},
		{
			name:   "rust skips the wildcard arm",
			file:   "f.rs",
			source: "fn f(a: i32) { if a > 0 || a < 2 {} while a > 0 {} match a { 1 => {}, _ => {} } }\n",
			id:     "f.rs#f",
			want:   5,
		},
		{
			name:   "c skips the default label",
			file:   "f.c",
			source: "int f(int a) { if (a && b) {} for (;;) {} switch (a) { case 1: break; default: break; } return a ? 1 : 2; }\n",
			id:     "f.c#f",
			want:   6,
		},
		{
			name:   "a class carries no complexity",
			file:   "k.py",
			source: "class K:\n    pass\n",
			id:     "k.py#K",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractFile(tt.file, tt.source)
			if err != nil {
				t.Fatalf("extractFile(%q) error = %v, want nil", tt.file, err)
			}
			index := slices.IndexFunc(got.nodes, func(node NodeV1) bool { return node.ID == tt.id })
			if index < 0 {
				t.Fatalf("extractFile(%q) has no node %q", tt.file, tt.id)
			}
			complexity := 0
			if got.nodes[index].Complexity != nil {
				complexity = *got.nodes[index].Complexity
			}
			if complexity != tt.want {
				t.Errorf("extractFile(%q) %s complexity = %d, want %d (0: none)", tt.file, tt.id, complexity, tt.want)
			}
		})
	}
}
