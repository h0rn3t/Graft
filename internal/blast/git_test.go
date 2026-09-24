package blast

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// newRepo returns an empty git repository with main as its branch, or skips
// the test when git is not installed.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet", "--initial-branch=main")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	runGitAs(t, dir, "Ann Author", "ann@example.com", args...)
}

func runGitAs(t *testing.T, dir, name, email string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=" + name, "-c", "user.email=" + email, "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q in %s error = %v, output = %s", args, dir, err, out)
	}
}

func writeRepoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commitAllAs(t *testing.T, dir, name, email, message string) {
	t.Helper()
	runGitAs(t, dir, name, email, "add", "--all")
	runGitAs(t, dir, name, email, "commit", "--quiet", "--message", message)
}

func numberedLines(from, to int) []string {
	lines := make([]string, 0, to-from+1)
	for n := from; n <= to; n++ {
		lines = append(lines, "line "+strconv.Itoa(n))
	}
	return lines
}

func rangesOf(t *testing.T, diff DiffResult) map[string][]LineRange {
	t.Helper()
	ranges := make(map[string][]LineRange, len(diff.Files))
	for _, file := range diff.Files {
		ranges[file.Path] = file.Ranges
	}
	return ranges
}

func TestDiffAuthorsLeavesOutCommitsMadeOnBaseAfterBranching(t *testing.T) {
	repo := newRepo(t)
	writeRepoFile(t, repo, "a.ts", "one\n")
	commitAllAs(t, repo, "Ann Author", "ann@example.com", "base")
	runGit(t, repo, "branch", "feature")
	writeRepoFile(t, repo, "main-only.ts", "main\n")
	commitAllAs(t, repo, "Mallory Main", "mallory@example.com", "later on main")
	runGit(t, repo, "checkout", "--quiet", "feature")
	writeRepoFile(t, repo, "a.ts", "one\ntwo\n")
	commitAllAs(t, repo, "Pat Branch", "pat@example.com", "the PR")

	got := DiffAuthors(t.Context(), repo, "main")
	if want := []string{"Pat Branch", "pat@example.com"}; !slices.Equal(got, want) {
		t.Errorf("DiffAuthors(main) = %q, want %q", got, want)
	}
}

func TestChangedFilesKeepsHunksAfterAnAddedLineLikeAFileHeader(t *testing.T) {
	repo := newRepo(t)
	original := numberedLines(1, 20)
	original[9] = "-- k;"
	writeRepoFile(t, repo, "c.c", strings.Join(original, "\n")+"\n")
	writeRepoFile(t, repo, "d.c", "first\n")
	commitAllAs(t, repo, "Ann Author", "ann@example.com", "base")

	changed := slices.Concat(original[:2], []string{"++ i;"}, original[2:9], original[10:17], []string{"changed 18"}, original[18:])
	writeRepoFile(t, repo, "c.c", strings.Join(changed, "\n")+"\n")
	writeRepoFile(t, repo, "d.c", "first, edited\n")

	diff, err := ChangedFiles(t.Context(), repo, nil)
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	want := map[string][]LineRange{
		"c.c": {{Start: 3, End: 3}, {Start: 10, End: 10}, {Start: 18, End: 18}},
		"d.c": {{Start: 1, End: 1}},
	}
	if got := rangesOf(t, diff); !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedFiles() ranges = %v, want %v", got, want)
	}
	first := diff.Files[0].Hunks[0].Lines
	if len(first) != 1 || first[0].Text != "++ i;" || first[0].N == nil || *first[0].N != 3 {
		t.Errorf("ChangedFiles() first c.c hunk lines = %+v, want the added line \"++ i;\" at 3", first)
	}
}

func TestChangedFilesReadsMnemonicPrefixesAndQuotedPaths(t *testing.T) {
	repo := newRepo(t)
	names := []string{`q"x.ts`, `back\slash.ts`, "sp ace.ts", "é.ts"}
	for _, name := range names {
		writeRepoFile(t, repo, name, "a\n")
	}
	commitAllAs(t, repo, "Ann Author", "ann@example.com", "base")
	for _, setting := range [][]string{{"diff.mnemonicPrefix", "true"}, {"core.quotePath", "true"}, {"color.ui", "always"}} {
		runGit(t, repo, "config", setting[0], setting[1])
	}
	for _, name := range names {
		writeRepoFile(t, repo, name, "a\nb\n")
	}

	diff, err := ChangedFiles(t.Context(), repo, nil)
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	want := make(map[string][]LineRange, len(names))
	for _, name := range names {
		want[name] = []LineRange{{Start: 2, End: 2}}
	}
	if got := rangesOf(t, diff); !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedFiles() ranges = %v, want %v", got, want)
	}
}

func TestSubdirectoryRootReadsPathsRelativeToIt(t *testing.T) {
	repo := newRepo(t)
	writeRepoFile(t, repo, "app/src/a.ts", "a\n")
	writeRepoFile(t, repo, "other/b.ts", "b\n")
	commitAllAs(t, repo, "Ann Author", "ann@example.com", "base")
	writeRepoFile(t, repo, "app/src/a.ts", "a\nmore\n")
	writeRepoFile(t, repo, "other/b.ts", "b\nmore\n")
	root := filepath.Join(repo, "app")

	diff, err := ChangedFiles(t.Context(), root, nil)
	if err != nil {
		t.Fatalf("ChangedFiles(%s) error = %v", root, err)
	}
	want := map[string][]LineRange{"src/a.ts": {{Start: 2, End: 2}}}
	if got := rangesOf(t, diff); !reflect.DeepEqual(got, want) {
		t.Errorf("ChangedFiles(app) ranges = %v, want %v", got, want)
	}
	owners := OwnersFor(t.Context(), root, []string{"src/a.ts"}, OwnerOptions{})
	if len(owners) != 1 || owners[0].Name != "Ann Author" || owners[0].Commits != 1 {
		t.Errorf("OwnersFor(app, src/a.ts) = %+v, want Ann Author with 1 commit", owners)
	}
}

func TestRefsCannotBeReadAsOptions(t *testing.T) {
	repo := newRepo(t)
	writeRepoFile(t, repo, "a.ts", "one\n")
	commitAllAs(t, repo, "Ann Author", "ann@example.com", "base")
	writeRepoFile(t, repo, "a.ts", "one\ntwo\n")
	commitAllAs(t, repo, "Ann Author", "ann@example.com", "second")
	target := filepath.Join(t.TempDir(), "written")
	base := "--output=" + target

	if RefExists(t.Context(), repo, base) {
		t.Errorf("RefExists(%q) = true, want false", base)
	}
	if _, err := ChangedFiles(t.Context(), repo, &base); !errors.Is(err, errOptionRef) {
		t.Errorf("ChangedFiles(%q) error = %v, want %v", base, err, errOptionRef)
	}
	if got := DiffAuthors(t.Context(), repo, base); got != nil {
		t.Errorf("DiffAuthors(%q) = %q, want nil", base, got)
	}
	// Past the exported checks, --end-of-options still keeps git from reading
	// the revision as an option.
	if _, err := diffFiles(t.Context(), repo, base+"...HEAD"); err == nil {
		t.Errorf("diffFiles(%q) error = nil, want an unknown revision", base)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("os.Stat(%s) error = %v, want the file never written", target, err)
	}
}

func TestChangedFilesOutsideARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if _, err := ChangedFiles(t.Context(), dir, nil); !errors.Is(err, ErrNotRepository) {
		t.Errorf("ChangedFiles(%s) error = %v, want %v", dir, err, ErrNotRepository)
	}
}

func TestAttachOwnersLooksUpOnlyTheRowsTheTableShows(t *testing.T) {
	repo := newRepo(t)
	report := &Report{Areas: []*ChangedArea{{Label: "area", Files: []string{"f0.ts"}}}}
	for i := range 10 {
		name := "f" + strconv.Itoa(i) + ".ts"
		writeRepoFile(t, repo, name, "x\n")
		if i > 0 {
			report.Modules = append(report.Modules, &ImpactedModule{Label: "module" + strconv.Itoa(i), Files: []string{name}})
		}
	}
	commitAllAs(t, repo, "Ann Author", "ann@example.com", "base")

	AttachOwners(t.Context(), repo, report, OwnerOptions{})

	if report.Areas[0].Owners == nil || len(*report.Areas[0].Owners) != 1 {
		t.Errorf("AttachOwners() area owners = %v, want one owner", report.Areas[0].Owners)
	}
	wantLabels := []string{"area"}
	for i, module := range report.Modules {
		shown := i < maxOwnerRows-len(report.Areas)
		if got := module.Owners != nil; got != shown {
			t.Errorf("AttachOwners() module %s has owners = %t, want %t", module.Label, got, shown)
		}
		if shown {
			wantLabels = append(wantLabels, module.Label)
		}
	}
	if report.Reviewers == nil || len(*report.Reviewers) != 1 || !slices.Equal((*report.Reviewers)[0].Areas, wantLabels) {
		t.Errorf("AttachOwners() reviewers = %+v, want Ann Author over %q", report.Reviewers, wantLabels)
	}
}
