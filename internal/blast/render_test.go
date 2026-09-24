package blast

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestMarkdownEscaping(t *testing.T) {
	tests := []struct {
		name, in, text, code, codeCell string
	}{
		{"ordinary", "src/core/store.ts:L1-L8", "src/core/store.ts:L1-L8", "`src/core/store.ts:L1-L8`", "`src/core/store.ts:L1-L8`"},
		{"underscores stay", "my_func", "my_func", "`my_func`", "`my_func`"},
		{"pipe", "a|b", `a\|b`, "`a|b`", "`a\\|b`"},
		{"html", "<img src=x>&", "&lt;img src=x&gt;&amp;", "`<img src=x>&`", "`<img src=x>&`"},
		{"backticks", "x`y``z", "x\\`y\\`\\`z", "```x`y``z```", "```x`y``z```"},
		{"edge backtick", "`x", "\\`x", "`` `x ``", "`` `x ``"},
		{"backslash", `a\`, `a\\`, "`a\\`", "`a\\`"},
		{"newline", "a\nb", "a b", "`a b`", "`a b`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mdText(tt.in); got != tt.text {
				t.Errorf("mdText(%q) = %q, want %q", tt.in, got, tt.text)
			}
			if got := mdCode(tt.in, 1); got != tt.code {
				t.Errorf("mdCode(%q, 1) = %q, want %q", tt.in, got, tt.code)
			}
			if got := mdCodeCell(tt.in); got != tt.codeCell {
				t.Errorf("mdCodeCell(%q) = %q, want %q", tt.in, got, tt.codeCell)
			}
		})
	}
	if got, want := mdCode("7: const s = ```", 3), "```` 7: const s = ``` ````"; got != want {
		t.Errorf("mdCode(evidence with a fence, 3) = %q, want %q", got, want)
	}
	if got, want := mdCode("7: plain", 3), "```7: plain```"; got != want {
		t.Errorf("mdCode(plain evidence, 3) = %q, want %q", got, want)
	}
}

func hostileReport() *Report {
	symbol := Impacted{Name: "<script>x</script>", Path: "src/a|b`.ts", Span: "L1-L2", Relation: "calls", Depth: 1}
	owners := []Owner{{Name: "Eve | <b>bold</b>", Commits: 1}}
	return &Report{
		Basis: "<main>...HEAD",
		Modules: []*ImpactedModule{{
			Label: "evil|label", Files: []string{"src/a|b`.ts"}, Symbols: []Impacted{symbol}, From: []string{"src/x|y.ts"}, Owners: &owners,
		}},
		Areas:     []*ChangedArea{{Label: "area<i>", Files: []string{"src/x|y.ts"}, Tests: TestsNone, Owners: &owners}},
		Unindexed: []string{"docs/<b>.md"},
		Reviewers: &[]Reviewer{{Owner: owners[0], Areas: []string{"area<i>"}}},
	}
}

func TestMarkdownReportEscapesRepositoryText(t *testing.T) {
	out := MarkdownReport(hostileReport(), "")
	for _, raw := range []string{"<script>", "<b>", "<i>", "<main>", "Eve |"} {
		if strings.Contains(out, raw) {
			t.Errorf("MarkdownReport(hostile names) contains raw %q:\n%s", raw, out)
		}
	}
	// Every table row keeps exactly its own column separators.
	rows := 0
	for line := range strings.SplitSeq(out, "\n") {
		want := 0
		switch {
		case strings.HasPrefix(line, "| evil"):
			want = 5
		case strings.HasPrefix(line, "| **"):
			want = 3
		default:
			continue
		}
		rows++
		if got := strings.Count(line, "|") - strings.Count(line, `\|`); got != want {
			t.Errorf("MarkdownReport row %q has %d unescaped pipes, want %d", line, got, want)
		}
	}
	if rows != 3 {
		t.Errorf("MarkdownReport(hostile names) has %d escaped table rows, want 3:\n%s", rows, out)
	}
}

func TestSymbolListQuotesEvidenceWithALongerFence(t *testing.T) {
	report := &Report{Modules: []*ImpactedModule{{Label: "m", Files: []string{"a.ts"}, Symbols: []Impacted{{Name: "f", Path: "a.ts", Span: "L3-L3", Relation: "calls", Depth: 1}}}}}
	got := symbolList(report, 1, func(Impacted) (evidenceLine, bool) {
		return evidenceLine{N: 3, Text: "  const doc = ```text```;"}, true
	})
	if want := "  ````3: const doc = ```text```;````"; !slices.Contains(got, want) {
		t.Errorf("symbolList(evidence with ```) = %q, want the line %q", got, want)
	}
}

func TestSymbolListCountsTheSymbolsLeftOut(t *testing.T) {
	tests := []struct {
		symbols int
		want    string
	}{
		{80, "…20 further symbols not listed."},
		{61, "…1 further symbol not listed."},
		{60, ""},
		{3, ""},
	}
	for _, tt := range tests {
		symbols := make([]Impacted, tt.symbols)
		for i := range symbols {
			symbols[i] = Impacted{Name: "s" + strconv.Itoa(i), Path: "a.ts", Span: "L1-L1", Relation: "calls", Depth: 1}
		}
		report := &Report{Modules: []*ImpactedModule{{Label: "m", Files: []string{"a.ts"}, Symbols: symbols}}}
		out := symbolList(report, tt.symbols, func(Impacted) (evidenceLine, bool) { return evidenceLine{}, false })
		got := ""
		for _, line := range out {
			if strings.HasSuffix(line, "not listed.") {
				got = line
			}
		}
		if got != tt.want {
			t.Errorf("symbolList(%d symbols) note = %q, want %q", tt.symbols, got, tt.want)
		}
	}
}

func TestFileReaderStaysInsideRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "in.ts"), []byte("inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret.txt"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(root, "link.ts")); err != nil {
		t.Fatal(err)
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	read := fileReader(opened)

	if got := read("in.ts"); !slices.Equal(got, []string{"inside", ""}) {
		t.Errorf("fileReader(root)(in.ts) = %q, want the file's lines", got)
	}
	for _, path := range []string{"../secret.txt", "link.ts"} {
		if got := read(path); got != nil {
			t.Errorf("fileReader(root)(%q) = %q, want nil for a file outside root", path, got)
		}
	}
	if got := fileReader(nil)("in.ts"); got != nil {
		t.Errorf("fileReader(nil)(in.ts) = %q, want nil", got)
	}
}
