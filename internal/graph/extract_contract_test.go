package graph

import (
	"reflect"
	"testing"
)

func TestExtractFileContract(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		source    string
		language  string
		wantNodes []NodeV1
		wantEdges []rawEdge
		wantErr   bool
	}{
		{
			name:     "TypeScript functions and calls",
			path:     "src/app.TS",
			source:   "export function greet(name: string): string {\n  return helper(name);\n}\n\nfunction helper(value: string): string {\n  return value;\n}",
			language: "typescript",
			wantNodes: []NodeV1{
				{ID: "src/app.TS", Name: "app.TS", Kind: "file", Path: "src/app.TS", Span: "L1-L7", Exported: true, Origin: "ast", BodyHash: "dfc9845b9dc41b3bb09029bcc35e1910df71e7318ae8e74ddcc69493af308b06", Chars: new(130), BodyText: new(""), SummaryState: "pending"},
				{ID: "src/app.TS#greet", Name: "greet", Kind: "function", Path: "src/app.TS", Span: "L1-L3", Signature: new("function greet(name: string): string"), Exported: true, Origin: "ast", BodyHash: "0b53a7ae1870891045c2d2839f0ee80e977b36ed82f9c43cf1c67af9911cef69", BodyText: new("function greet(name: string): string { return helper(name); }"), SummaryState: "pending"},
				{ID: "src/app.TS#helper", Name: "helper", Kind: "function", Path: "src/app.TS", Span: "L5-L7", Signature: new("function helper(value: string): string"), Origin: "ast", BodyHash: "b9d33cb96c6dc1d29080021709000748806c79efc19b370f2a4769bee1eb430d", BodyText: new("function helper(value: string): string { return value; }"), SummaryState: "pending"},
			},
			wantEdges: []rawEdge{
				{source: "src/app.TS", relation: "contains", targetID: "src/app.TS#greet", file: "src/app.TS"},
				{source: "src/app.TS#greet", relation: "calls", name: "helper", file: "src/app.TS"},
				{source: "src/app.TS", relation: "contains", targetID: "src/app.TS#helper", file: "src/app.TS"},
			},
		},
		{
			name:     "JavaScript class methods and calls",
			path:     "src/worker.js",
			source:   "export class Worker { run(value) { return make(value); } }\nfunction make(value) { return value; }",
			language: "javascript",
			wantNodes: []NodeV1{
				{ID: "src/worker.js", Name: "worker.js", Kind: "file", Path: "src/worker.js", Span: "L1-L2", Exported: true, Origin: "ast", BodyHash: "5b7a3dd7f6edb752a3b9a0f0ad2336a21507dabd1f6013950da2d3b939fa4eca", Chars: new(97), BodyText: new(""), SummaryState: "pending"},
				{ID: "src/worker.js#Worker", Name: "Worker", Kind: "class", Path: "src/worker.js", Span: "L1-L1", Signature: new("class Worker"), Exported: true, Origin: "ast", BodyHash: "0cb799849d1367997c64edcf0741e3b05d714de2342e073d8e8655613cd01da7", BodyText: new("class Worker { run(value) { return make(value); } }"), SummaryState: "pending"},
				{ID: "src/worker.js#Worker.run", Name: "run", Kind: "method", Path: "src/worker.js", Span: "L1-L1", Signature: new("run(value)"), Exported: true, Origin: "ast", BodyHash: "bac35bfcea417e21d006c8a1b2df4124473e7bbe9a7f6225ccd2fe95ac8441eb", BodyText: new("run(value) { return make(value); }"), Owner: new("Worker"), SummaryState: "pending"},
				{ID: "src/worker.js#make", Name: "make", Kind: "function", Path: "src/worker.js", Span: "L2-L2", Signature: new("function make(value)"), Origin: "ast", BodyHash: "0005a05f47a8816ef4a26c00d1225d67f2112ea899396863ccf30deb9b3245cf", BodyText: new("function make(value) { return value; }"), SummaryState: "pending"},
			},
			wantEdges: []rawEdge{
				{source: "src/worker.js", relation: "contains", targetID: "src/worker.js#Worker", file: "src/worker.js"},
				{source: "src/worker.js#Worker", relation: "contains", targetID: "src/worker.js#Worker.run", file: "src/worker.js"},
				{source: "src/worker.js#Worker.run", relation: "calls", name: "make", file: "src/worker.js"},
				{source: "src/worker.js", relation: "contains", targetID: "src/worker.js#make", file: "src/worker.js"},
			},
		},
		{
			name:     "JavaScript module import",
			path:     "src/app.js",
			source:   "import \"./dep.js\";\nfunction run() {}",
			language: "javascript",
			wantNodes: []NodeV1{
				{ID: "src/app.js", Name: "app.js", Kind: "file", Path: "src/app.js", Span: "L1-L2", Exported: true, Origin: "ast", BodyHash: "e895f158ab4e7ce35e76e021642b3c46da956ba6e185f11184d32016407331ed", Chars: new(36), BodyText: new("import \"./dep.js\";"), SummaryState: "pending"},
				{ID: "src/app.js#run", Name: "run", Kind: "function", Path: "src/app.js", Span: "L2-L2", Signature: new("function run()"), Origin: "ast", BodyHash: "772ac08d618be0a9d483d59ab7573fb70e730fc76853177c37b38c7a70607fc1", BodyText: new("function run() {}"), SummaryState: "pending"},
			},
			wantEdges: []rawEdge{
				{source: "src/app.js", relation: "imports", specifier: "./dep.js", file: "src/app.js"},
				{source: "src/app.js", relation: "contains", targetID: "src/app.js#run", file: "src/app.js"},
			},
		},
		{
			name:     "local parameter shadows imported symbol",
			path:     "src/app.js",
			source:   "import { helper } from \"./dep.js\";\nfunction run(helper) { return helper; }",
			language: "javascript",
			wantNodes: []NodeV1{
				{ID: "src/app.js", Name: "app.js", Kind: "file", Path: "src/app.js", Span: "L1-L2", Exported: true, Origin: "ast", BodyHash: "21437d2e746dd513827bc27118c345306eda57939191f62ad18fb95e8ec3ae2f", Chars: new(74), BodyText: new("import { helper } from \"./dep.js\";"), SummaryState: "pending"},
				{ID: "src/app.js#run", Name: "run", Kind: "function", Path: "src/app.js", Span: "L2-L2", Signature: new("function run(helper)"), Origin: "ast", BodyHash: "7ba8173d1eb759d46e0a0669ba989b4fff3a76ff4cac9cf886b5e7163cc30aed", BodyText: new("function run(helper) { return helper; }"), SummaryState: "pending"},
			},
			wantEdges: []rawEdge{
				{source: "src/app.js", relation: "imports", specifier: "./dep.js", file: "src/app.js"},
				{source: "src/app.js", relation: "contains", targetID: "src/app.js#run", file: "src/app.js"},
			},
		},
		{
			name:    "unsupported extension",
			path:    "src/app.rs",
			source:  "fn run() {}",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractFile(tt.path, tt.source)
			if (err != nil) != tt.wantErr {
				t.Fatalf("extractFile(%q, source) error = %v, want error presence %t", tt.path, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.language != tt.language || len(got.nodes) != len(tt.wantNodes) || len(got.rawEdges) != len(tt.wantEdges) {
				t.Fatalf("extractFile(%q, source) = (language %q, %d nodes, %d edges), want (%q, %d nodes, %d edges)", tt.path, got.language, len(got.nodes), len(got.rawEdges), tt.language, len(tt.wantNodes), len(tt.wantEdges))
			}
			for index, want := range tt.wantNodes {
				node := got.nodes[index]
				if node.ID != want.ID || node.Name != want.Name || node.Kind != want.Kind || node.Path != want.Path || node.Span != want.Span || !reflect.DeepEqual(node.Signature, want.Signature) || node.Exported != want.Exported || node.Origin != want.Origin || node.BodyHash != want.BodyHash || !reflect.DeepEqual(node.Owner, want.Owner) || !reflect.DeepEqual(node.Chars, want.Chars) || !reflect.DeepEqual(node.BodyText, want.BodyText) || node.SummaryState != want.SummaryState {
					t.Errorf("extractFile(%q, source).nodes[%d] = %#v, want %#v", tt.path, index, node, want)
				}
			}
			for index, want := range tt.wantEdges {
				edge := got.rawEdges[index]
				if !reflect.DeepEqual(edge, want) {
					t.Errorf("extractFile(%q, source).rawEdges[%d] = %#v, want %#v", tt.path, index, edge, want)
				}
			}
		})
	}
}

func TestExtractArrowSignatureContract(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		source string
		want   string
	}{
		{
			name:   "TypeScript block-bodied arrow",
			path:   "src/app.ts",
			source: "const greet = (name: string) => { return name; }",
			want:   "greet = (name: string)",
		},
		{
			name:   "JavaScript expression-bodied arrow",
			path:   "src/app.js",
			source: "const greet = (name) => name;",
			want:   "greet = (name)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractFile(tt.path, tt.source)
			if err != nil {
				t.Fatalf("extractFile(%q, %q) error = %v, want nil", tt.path, tt.source, err)
			}
			if len(got.nodes) != 2 || got.nodes[1].Signature == nil {
				t.Fatalf("extractFile(%q, %q) nodes = %#v, want one function signature", tt.path, tt.source, got.nodes)
			}
			if signature := *got.nodes[1].Signature; signature != tt.want {
				t.Errorf("extractFile(%q, %q) signature = %q, want %q", tt.path, tt.source, signature, tt.want)
			}
		})
	}
}
