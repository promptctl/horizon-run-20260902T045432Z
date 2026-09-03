package drift

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/promptctl/macklebox/internal/ui"
)

// Helpers shared by every case here. Fixtures are built under t.TempDir, and
// the two paths a case compares are always named source and target, which are
// appspec/06's own words for them -- backup passes the home path as the source
// and the mackup path as the target, restore the other way round, and this
// package cannot tell which it is being used for.

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("preparing the parent of %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func writeBytes(t *testing.T, path string, content []byte) string {
	t.Helper()
	return writeFile(t, path, string(content))
}

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	return path
}

func symlink(t *testing.T, target, linkPath string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
		t.Fatalf("preparing the parent of %s: %v", linkPath, err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatalf("linking %s to %s: %v", linkPath, target, err)
	}
	return linkPath
}

// details renders a result's detail as the lines a user would see.
func details(r Result) []string {
	rendered := make([]string, len(r.Detail))
	for i, line := range r.Detail {
		rendered[i] = line.Text
	}
	return rendered
}

// expectDiffers checks the half of appspec/06's pair that says the caller must
// prompt, and reports the detail when it is wrong so a failure is readable.
func expectDiffers(t *testing.T, got Result, why string) {
	t.Helper()
	if got.Identical {
		t.Fatalf("%s was reported identical, so the file would be skipped with no prompt", why)
	}
}

func expectIdentical(t *testing.T, got Result, why string) {
	t.Helper()
	if !got.Identical {
		t.Fatalf("%s was reported as differing, with detail:\n%s", why, strings.Join(details(got), "\n"))
	}
}

func TestTwoIdenticalFilesAreTheIdempotencyFixedPoint(t *testing.T) {
	// appspec/06: "When two paths are identical the file is skipped with no
	// prompt -- this is the backup/restore idempotency fixed point." A
	// detail here would be printed under a header for a file nothing is
	// happening to.
	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "source"), "alpha\nbravo\n")
	target := writeFile(t, filepath.Join(dir, "target"), "alpha\nbravo\n")

	got := Compare(source, target)
	expectIdentical(t, got, "two files with the same bytes")
	if len(got.Detail) != 0 {
		t.Errorf("an identical result carries detail: %v", details(got))
	}
}

// appspec/06: "If either path is a symlink: treated as differing, with no diff
// detail (plain prompt, no diff printed)."
//
// Every arrangement below has the SAME CONTENT on both sides, which is what
// makes the case worth having: a drift check written with the following stat
// that internal/syncfs.Copy uses -- the primitive one package over -- would
// compare the link's target and report every one of them "already in sync",
// silently skipping a file the user was supposed to be prompted about.
func TestEitherPathBeingASymlinkIsDifferingWithNoDetail(t *testing.T) {
	dir := t.TempDir()
	real := writeFile(t, filepath.Join(dir, "real"), "same\n")
	plain := writeFile(t, filepath.Join(dir, "plain"), "same\n")
	toReal := symlink(t, real, filepath.Join(dir, "link"))
	toOther := symlink(t, plain, filepath.Join(dir, "other-link"))
	directory := mustMkdir(t, filepath.Join(dir, "tree"))
	toDirectory := symlink(t, directory, filepath.Join(dir, "tree-link"))

	for _, pair := range []struct {
		why            string
		source, target string
	}{
		{"a symlinked source over a real file of the same content", toReal, plain},
		{"a real file over a symlinked target of the same content", plain, toReal},
		{"two symlinks to files of the same content", toReal, toOther},
		{"a symlinked directory over the directory itself", toDirectory, directory},
	} {
		got := Compare(pair.source, pair.target)
		expectDiffers(t, got, pair.why)
		if len(got.Detail) != 0 {
			t.Errorf("%s printed detail, and appspec/06 gives this case the plain prompt: %v", pair.why, details(got))
		}
	}
}

func TestATypeMismatchIsOneLineSayingWhichWayRound(t *testing.T) {
	// appspec/06: differing, "with a one-line 'type mismatch: folder vs file'
	// (or 'file vs folder') detail". Which way round is the content of the
	// message, so both directions are pinned: a single-arm implementation
	// would pass a case that only checked one.
	dir := t.TempDir()
	file := writeFile(t, filepath.Join(dir, "file"), "x\n")
	directory := mustMkdir(t, filepath.Join(dir, "dir"))

	for _, arrangement := range []struct {
		source, target, want string
	}{
		{directory, file, "type mismatch: folder vs file"},
		{file, directory, "type mismatch: file vs folder"},
	} {
		got := Compare(arrangement.source, arrangement.target)
		expectDiffers(t, got, "a "+arrangement.want)
		if want := []string{arrangement.want}; !equalLines(details(got), want) {
			t.Errorf("detail = %v, want %v", details(got), want)
		}
	}
}

// The fixtures are byte-identical copies of internal/plist's, which were
// produced by macOS -- written as XML and converted with `plutil -convert
// binary1` -- and are described where they came from. Copied rather than
// reached for across the package boundary so that this case reads on its own,
// and because what they pin here is a different claim: internal/plist's case
// says the two spellings parse to the same thing, and this one says drift
// detection PUTS THEM DOWN THE PLIST ARM. A binary plist is not valid UTF-8, so
// without that arm this pair is two files that "differ" with a
// "binary contents differ" note.
const (
	xmlPlist    = "testdata/settings.plist"
	binaryPlist = "testdata/settings.binary.plist"
)

func TestTwoSpellingsOfOnePropertyListAreComparedByContentNotBytes(t *testing.T) {
	dir := t.TempDir()
	xml, err := os.ReadFile(xmlPlist)
	if err != nil {
		t.Fatalf("reading %s: %v", xmlPlist, err)
	}
	binary, err := os.ReadFile(binaryPlist)
	if err != nil {
		t.Fatalf("reading %s: %v", binaryPlist, err)
	}
	if bytes.Equal(xml, binary) {
		t.Fatal("the two fixtures have the same bytes, so this case would pass without a plist reader")
	}
	source := writeBytes(t, filepath.Join(dir, "source.plist"), xml)
	target := writeBytes(t, filepath.Join(dir, "target.plist"), binary)

	expectIdentical(t, Compare(source, target), "one property list in its two on-disk spellings")
}

func TestAPropertyListIsComparedAsAStructureAndNotAsMarkup(t *testing.T) {
	// Same settings, different bytes: the keys are in another order and the
	// indentation is different. Compared as text these two files differ on
	// nearly every line, which is what appspec/06 puts the plist arm BEFORE
	// the UTF-8 text arm to prevent.
	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "source.plist"),
		"<plist version=\"1.0\"><dict>\n  <key>a</key><integer>1</integer>\n  <key>b</key><string>two</string>\n</dict></plist>\n")
	target := writeFile(t, filepath.Join(dir, "target.plist"),
		"<plist version=\"1.0\"><dict><key>b</key><string>two</string><key>a</key><integer>1</integer></dict></plist>")

	expectIdentical(t, Compare(source, target), "two property lists holding the same settings")
}

func TestTwoPropertyListsThatDifferProduceADiffOfTheirStructures(t *testing.T) {
	// appspec/06: "else a unified diff of their pretty-printed structures".
	// The diff is of the STRUCTURE, so the changed line names the key and its
	// value rather than quoting the markup around it -- and it runs from the
	// destination to the source, because the user is about to be asked whether
	// to replace the destination with the source.
	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "source.plist"),
		"<plist version=\"1.0\"><dict><key>count</key><integer>8</integer></dict></plist>")
	target := writeFile(t, filepath.Join(dir, "target.plist"),
		"<plist version=\"1.0\"><dict><key>count</key><integer>7</integer></dict></plist>")

	got := Compare(source, target)
	expectDiffers(t, got, "two property lists holding different settings")
	want := []string{
		"--- " + target,
		"+++ " + source,
		"@@ -1,3 +1,3 @@",
		" {",
		`-  "count": 7`,
		`+  "count": 8`,
		" }",
	}
	if !equalLines(details(got), want) {
		t.Errorf("detail =\n%s\n\nwant:\n%s", strings.Join(details(got), "\n"), strings.Join(want, "\n"))
	}
}

func TestTwoTextFilesProduceAUnifiedDiffOfTheirLines(t *testing.T) {
	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "source"), "alpha\nBRAVO\ncharlie\n")
	target := writeFile(t, filepath.Join(dir, "target"), "alpha\nbravo\ncharlie\n")

	got := Compare(source, target)
	expectDiffers(t, got, "two text files with a changed line")
	want := []string{
		"--- " + target,
		"+++ " + source,
		"@@ -1,3 +1,3 @@",
		" alpha",
		"-bravo",
		"+BRAVO",
		" charlie",
	}
	if !equalLines(details(got), want) {
		t.Errorf("detail =\n%s\n\nwant:\n%s", strings.Join(details(got), "\n"), strings.Join(want, "\n"))
	}
}

func TestTextThatIsNotASCIIIsStillText(t *testing.T) {
	// appspec/06's second arm is "readable as UTF-8 text", not "ASCII". A
	// reader that tested for ASCII would send every config file holding an
	// accent or a box-drawing character down the byte-for-byte arm and print
	// "binary contents differ" for a one-word change.
	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "source"), "café — 日本語\n")
	target := writeFile(t, filepath.Join(dir, "target"), "café — 中文\n")

	got := Compare(source, target)
	expectDiffers(t, got, "two UTF-8 files")
	if joined := strings.Join(details(got), "\n"); !strings.Contains(joined, "+café — 日本語") {
		t.Errorf("detail is not a text diff:\n%s", joined)
	}
}

func TestTwoFilesDifferingOnlyInAFinalNewlineDifferAndSayHow(t *testing.T) {
	// The pair that catches a comparison done on split lines instead of on
	// bytes: these two files have the same lines and different contents. The
	// second half of the case is the one that matters -- a result that
	// differs must not print an empty detail, or the user meets a prompt with
	// nothing under the header explaining it.
	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "source"), "alpha\nbravo")
	target := writeFile(t, filepath.Join(dir, "target"), "alpha\nbravo\n")

	got := Compare(source, target)
	expectDiffers(t, got, "a file with no final newline against one with it")
	if joined := strings.Join(details(got), "\n"); !strings.Contains(joined, noNewline) {
		t.Errorf("detail does not say what differs:\n%s", joined)
	}
}

func TestFilesThatAreNotTextGetTheOneLineNoteAppspec06Names(t *testing.T) {
	// appspec/06: "Else compared byte-for-byte; identical if equal, else the
	// detail is 'binary contents differ'." Invalid UTF-8 on both sides, so
	// neither of the arms above can apply.
	dir := t.TempDir()
	source := writeBytes(t, filepath.Join(dir, "source"), []byte{0x00, 0xFF, 0xFE, 0x01})
	target := writeBytes(t, filepath.Join(dir, "target"), []byte{0x00, 0xFF, 0xFE, 0x02})

	got := Compare(source, target)
	expectDiffers(t, got, "two files that are not text")
	if want := []string{"binary contents differ"}; !equalLines(details(got), want) {
		t.Errorf("detail = %v, want %v", details(got), want)
	}
}

func TestTwoIdenticalFilesThatAreNotTextAreIdentical(t *testing.T) {
	dir := t.TempDir()
	content := []byte{0x00, 0xFF, 0xFE, 0x01}
	source := writeBytes(t, filepath.Join(dir, "source"), content)
	target := writeBytes(t, filepath.Join(dir, "target"), content)

	expectIdentical(t, Compare(source, target), "two files with the same non-text bytes")
}

func TestAPathThatIsNeitherFileNorDirectoryIsDifferingWithNoDetail(t *testing.T) {
	// appspec/06's per-file step 1 checks only that the SOURCE is a regular
	// file or directory, so whatever is at the destination arrives here. There
	// is no content to compare and no arm in that section for it, so it takes
	// the same answer a symlink takes.
	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "source"), "x\n")

	got := Compare(source, filepath.Join(dir, "missing"))
	expectDiffers(t, got, "a destination that is not there")
	if len(got.Detail) != 0 {
		t.Errorf("detail printed for a path that cannot be compared: %v", details(got))
	}
}

func TestTwoIdenticalTreesAreIdentical(t *testing.T) {
	dir := t.TempDir()
	for _, side := range []string{"source", "target"} {
		writeFile(t, filepath.Join(dir, side, "one"), "1\n")
		writeFile(t, filepath.Join(dir, side, "nested", "deep", "two"), "2\n")
		mustMkdir(t, filepath.Join(dir, side, "empty"))
	}
	expectIdentical(t, Compare(filepath.Join(dir, "source"), filepath.Join(dir, "target")), "two identical trees")
}

func TestADirectoryComparisonIsRecursiveAndNotAShallowStat(t *testing.T) {
	// appspec/06: "compared recursively by content (not shallow stat)". Both
	// files are the same length and were written in the same second, so
	// anything short of reading them reports agreement.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "source", "a", "b", "c"), "one\n")
	writeFile(t, filepath.Join(dir, "target", "a", "b", "c"), "two\n")

	got := Compare(filepath.Join(dir, "source"), filepath.Join(dir, "target"))
	expectDiffers(t, got, "trees whose only difference is a nested file's contents")
	if want := []string{"changed: a/b/c"}; !equalLines(details(got), want) {
		t.Errorf("detail = %v, want %v", details(got), want)
	}
}

func TestADirectoryComparisonListsTheThreeGroupsAppspec06AsksFor(t *testing.T) {
	// "the detail lists, sorted: changed files ('changed: <name>'), files
	// present only in source ('only in source: <name>'), and files present
	// only in destination ('only in target: <name>')" -- sorted within each
	// group, and in that order between the groups.
	//
	// The names are chosen so that the walk's own order is NOT sorted order:
	// the walk visits "a" and descends immediately, emitting "a/x" before it
	// reaches "a-b", while sorting the paths puts "a-b" first.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "source", "a", "x"), "source\n")
	writeFile(t, filepath.Join(dir, "target", "a", "x"), "target\n")
	writeFile(t, filepath.Join(dir, "source", "a-b"), "source\n")
	writeFile(t, filepath.Join(dir, "target", "a-b"), "target\n")
	writeFile(t, filepath.Join(dir, "source", "zebra"), "only here\n")
	writeFile(t, filepath.Join(dir, "source", "alpha"), "only here\n")
	writeFile(t, filepath.Join(dir, "target", "yak"), "only there\n")
	writeFile(t, filepath.Join(dir, "target", "beta"), "only there\n")

	got := Compare(filepath.Join(dir, "source"), filepath.Join(dir, "target"))
	expectDiffers(t, got, "two trees with changes on both sides")
	want := []string{
		"changed: a-b",
		"changed: a/x",
		"only in source: alpha",
		"only in source: zebra",
		"only in target: beta",
		"only in target: yak",
	}
	if !equalLines(details(got), want) {
		t.Errorf("detail =\n%s\n\nwant:\n%s", strings.Join(details(got), "\n"), strings.Join(want, "\n"))
	}

	// The levels, which are the closest of appspec/07's four diff
	// decorations: what the copy would add is the added level, what only the
	// destination has is the removed level, and a changed name is neither.
	wantLevels := []ui.Level{
		ui.DiffFileHeader, ui.DiffFileHeader,
		ui.DiffAdded, ui.DiffAdded,
		ui.DiffRemoved, ui.DiffRemoved,
	}
	for i, level := range wantLevels {
		if got.Detail[i].Level != level {
			t.Errorf("%q is level %d, want %d", got.Detail[i].Text, got.Detail[i].Level, level)
		}
	}
}

func TestADirectoryOnOneSideIsNamedOnceAndNotDescendedInto(t *testing.T) {
	// diff(1)'s "Only in a: foo", for its reason: listing every file under a
	// directory the other side does not have buries the fact that the
	// directory itself is missing.
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "target"))
	writeFile(t, filepath.Join(dir, "source", "only", "one"), "1\n")
	writeFile(t, filepath.Join(dir, "source", "only", "two"), "2\n")

	got := Compare(filepath.Join(dir, "source"), filepath.Join(dir, "target"))
	expectDiffers(t, got, "a tree with a directory the other does not have")
	if want := []string{"only in source: only"}; !equalLines(details(got), want) {
		t.Errorf("detail = %v, want %v", details(got), want)
	}
}

func TestAnEmptyDirectoryOnOneSideIsAnExtraEntry(t *testing.T) {
	// appspec/06: identical only if "neither side has extra entries". An
	// empty directory holds no files, so a comparison that collected only
	// leaves would report these two trees identical and skip the copy that
	// would create it.
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "source", "empty"))
	mustMkdir(t, filepath.Join(dir, "target"))

	got := Compare(filepath.Join(dir, "source"), filepath.Join(dir, "target"))
	expectDiffers(t, got, "a tree with an empty directory the other does not have")
	if want := []string{"only in source: empty"}; !equalLines(details(got), want) {
		t.Errorf("detail = %v, want %v", details(got), want)
	}
}

func TestASymlinkInsideATreeIsFollowedSoTheComparisonIsTheCopysFixedPoint(t *testing.T) {
	// The deliberate reversal of the top-level rule, and the reason for it.
	// internal/syncfs.Copy follows symlinks when it copies a tree, so the copy
	// it writes holds a real file wherever the source held a link. If this
	// walk did not follow, that pair would be "changed" on every run: the user
	// would be prompted, would agree, the copy would reproduce exactly the
	// state just called different, and the next run would prompt again --
	// which is the idempotency fixed point appspec/06 states outright.
	dir := t.TempDir()
	elsewhere := writeFile(t, filepath.Join(dir, "elsewhere"), "shared\n")
	mustMkdir(t, filepath.Join(dir, "source"))
	symlink(t, elsewhere, filepath.Join(dir, "source", "f"))
	writeFile(t, filepath.Join(dir, "target", "f"), "shared\n")

	expectIdentical(t, Compare(filepath.Join(dir, "source"), filepath.Join(dir, "target")),
		"a tree whose entry is a symlink to the same content the other side holds directly")
}

func TestADanglingSymlinkInsideATreeIsAChangedEntry(t *testing.T) {
	// Following is not the same as ignoring. An entry that cannot be reached
	// is not established to match, and appspec/06 makes "identical" the claim
	// that needs evidence.
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "source"))
	symlink(t, filepath.Join(dir, "nothing-here"), filepath.Join(dir, "source", "f"))
	writeFile(t, filepath.Join(dir, "target", "f"), "content\n")

	got := Compare(filepath.Join(dir, "source"), filepath.Join(dir, "target"))
	expectDiffers(t, got, "a tree holding a dangling symlink")
	if want := []string{"changed: f"}; !equalLines(details(got), want) {
		t.Errorf("detail = %v, want %v", details(got), want)
	}
}

func TestAnEntryThatIsADirectoryOnOneSideAndAFileOnTheOtherIsChanged(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "source", "f", "inside"), "1\n")
	writeFile(t, filepath.Join(dir, "target", "f"), "1\n")

	got := Compare(filepath.Join(dir, "source"), filepath.Join(dir, "target"))
	expectDiffers(t, got, "an entry that is a directory on one side and a file on the other")
	if want := []string{"changed: f"}; !equalLines(details(got), want) {
		t.Errorf("detail = %v, want %v", details(got), want)
	}
}

func TestTheDriftDetailGoesToStdout(t *testing.T) {
	// appspec/07 "Output streams": the drift header "and its diff body" are on
	// STDOUT, under the paragraph headed "Do not generalize warnings ->
	// stderr" which names this message. Every level a drift line can carry is
	// exercised, because the routing is per-level and one misrouted level
	// would put part of a diff on the error stream.
	var out, errs bytes.Buffer
	streams := &ui.IO{Out: &out, Err: &errs}

	result := Result{Detail: []Line{
		{Level: ui.DiffFileHeader, Text: "--- target"},
		{Level: ui.DiffHunk, Text: "@@ -1 +1 @@"},
		{Level: ui.DiffRemoved, Text: "-before"},
		{Level: ui.DiffAdded, Text: "+after"},
		{Level: ui.Progress, Text: " context"},
	}}
	result.Print(streams)

	if errs.Len() != 0 {
		t.Errorf("drift detail reached stderr: %q", errs.String())
	}
	if !ui.HasColor(out.String()) {
		t.Error("the detail was printed without colour; appspec/07 does not condition colour on a terminal")
	}
	want := "--- target\n@@ -1 +1 @@\n-before\n+after\n context\n"
	if got := ui.StripColor(out.String()); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestAnIdenticalResultPrintsNothing(t *testing.T) {
	// appspec/06 gives the identical case no output at all: it is "skipped
	// with no prompt", and the only trace of it is the verbose "already in
	// sync, skipping" line, which belongs to the per-file procedure and not
	// here.
	var out, errs bytes.Buffer
	streams := &ui.IO{Out: &out, Err: &errs}
	identical().Print(streams)
	if out.Len() != 0 || errs.Len() != 0 {
		t.Errorf("an identical result printed stdout %q and stderr %q", out.String(), errs.String())
	}
}

func TestADifferingResultWithNoDetailPrintsNothingRatherThanAPlaceholder(t *testing.T) {
	// appspec/06 pairs the empty detail with "the plain prompt, no diff
	// printed". A line here saying so would be a message the specification
	// does not have, printed between the header and the prompt.
	var out, errs bytes.Buffer
	streams := &ui.IO{Out: &out, Err: &errs}
	differs().Print(streams)
	if out.Len() != 0 || errs.Len() != 0 {
		t.Errorf("a detail-less result printed stdout %q and stderr %q", out.String(), errs.String())
	}
}

func TestATreeThatLinksBackIntoItselfIsEndedByTheOperatingSystem(t *testing.T) {
	// The walk has no cycle detector, and compareTrees' doc says why: the copy
	// it guards has none either, so a private one here would report agreement
	// about a tree internal/syncfs cannot write. What ends the recursion is the
	// kernel's limit on how many symbolic links it will resolve in one path --
	// each level adds another, so the read fails within a few dozen and that
	// subtree is recorded as changed.
	//
	// The claim is checked rather than asserted: it is the whole reason there
	// is no guard, and an unbounded walk here is a sync run that never
	// returns.
	dir := t.TempDir()
	for _, side := range []string{"source", "target"} {
		root := mustMkdir(t, filepath.Join(dir, side))
		mustMkdir(t, filepath.Join(root, "a"))
		symlink(t, root, filepath.Join(root, "a", "up"))
	}

	done := make(chan Result, 1)
	go func() { done <- Compare(filepath.Join(dir, "source"), filepath.Join(dir, "target")) }()
	select {
	case got := <-done:
		// Which subtree is named is the operating system's business -- it
		// depends on where in the descent the link budget runs out -- so what
		// is pinned is that the answer arrives and is not "already in sync".
		expectDiffers(t, got, "two trees that link back into themselves")
	case <-time.After(30 * time.Second):
		t.Fatal("Compare did not return on a tree that links back into itself")
	}
}
