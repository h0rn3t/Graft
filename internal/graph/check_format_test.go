package graph

import "testing"

func TestFormatGraphCheckReport(t *testing.T) {
	tests := []struct {
		name   string
		result GraphCheckResult
		want   string
	}{
		{
			name:   "missing graph",
			result: GraphCheckResult{Missing: true},
			want:   "graph check: NO GRAPH\n\nNo graft/.graph/wiring.json found. Run `graft build` first.",
		},
		{
			name:   "clean structure",
			result: GraphCheckResult{OK: true},
			want:   "graph check: OK — the wiring graph is in sync with the code.",
		},
		{
			name:   "structural drift",
			result: GraphCheckResult{Changed: []string{"main.go#run"}},
			want:   "graph check: STALE\n\nchanged (1):\n  ~ main.go#run\n\nRun `graft build` to rebuild the structure, then commit graft/.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatGraphCheckReport(tt.result); got != tt.want {
				t.Errorf("FormatGraphCheckReport(%+v) = %q, want %q", tt.result, got, tt.want)
			}
		})
	}
}
