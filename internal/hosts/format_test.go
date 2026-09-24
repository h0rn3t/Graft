package hosts

import (
	"slices"
	"strings"
	"testing"
)

func TestFormatInitEpilogueWordmarkLabels(t *testing.T) {
	const (
		version = "  ]             ~ ~             |     |___/             v0.2.0"
		stats   = "   |                           |     3,018 nodes · 7,231 edges"
	)
	tests := []struct {
		name       string
		graphBuilt bool
		want       []string
	}{
		{
			name:       "graph built",
			graphBuilt: true,
			want:       []string{wordmark[7], version, wordmark[9], stats},
		},
		{
			name: "graph missing",
			want: []string{wordmark[7], version, wordmark[9], wordmark[10]},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lines := strings.Split(FormatInitEpilogue(tt.graphBuilt, 3018, 7231, "0.2.0", false), "\n")
			if got := lines[7:11]; !slices.Equal(got, tt.want) {
				t.Errorf("FormatInitEpilogue(%v, 3018, 7231, %q, false) lines 7-10 = %q, want %q", tt.graphBuilt, "0.2.0", got, tt.want)
			}
		})
	}
}
