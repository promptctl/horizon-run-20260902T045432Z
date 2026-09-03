//go:build conformance

// The comparison half of the copy suite: the SHAPE of what appspec/06 "Drift
// detection" prints, observed at the program's boundary.
//
// sync_test.go already asks whether a comparison happened and which way round
// it reads -- that a diff appears under the header, that the two sides are the
// destination and the source, that a type mismatch says which way round, that
// three groups are named for a directory. What it does not ask is what the
// diff LOOKS like, and that is this file: the context width, the hunk merge,
// the two range spellings diff(1) uses, the appspec/07 level on each kind of
// line, the no-newline marker, and the parts of the directory summary that a
// shallow or unsorted implementation still passes today.
//
// It is separate from sync_test.go because the two are about different things
// and one file was already eleven hundred lines. Every case here syncs the
// same probe application and answers "no" at the prompt: the assertion is what
// was PRINTED before the question, so a case that let the copy happen would be
// asserting the output of one run and the fixture of the next.
//
// Why any of this belongs in a black-box suite at all, when internal/drift's
// own cases cover the same ground more cheaply: because internal/drift is
// reached through a single call in internal/app/sync.go, and every claim below
// is one a program could satisfy in the package and lose on the way out --
// a level dropped by the caller, a detail printed on the wrong stream, a
// comparison the executor never makes. The mutation battery reports exactly
// that as RIG-BLIND, and the cases here are what stops it.

package conformance

import (
	"fmt"
	"strings"
	"testing"
)

// numberedLines builds a file of count lines named l01, l02 and so on, with the
// line at oneBasedChange replaced by replacement when it is not empty.
//
// One helper rather than a literal per case, because the diff-shape cases are
// about WHICH lines appear around a change and a hand-written fixture makes
// that a counting exercise for the reader. The names carry their own line
// number so an assertion reads as the line it is about.
func numberedLines(count, oneBasedChange int, replacement string) string {
	var out strings.Builder
	for i := 1; i <= count; i++ {
		if i == oneBasedChange && replacement != "" {
			out.WriteString(replacement + "\n")
			continue
		}
		fmt.Fprintf(&out, "l%02d\n", i)
	}
	return out.String()
}

// hunkCount is how many hunk headers a diff printed.
func hunkCount(stdout string) int { return strings.Count(stdout, "@@ -") }

// -- The unified diff's shape ------------------------------------------------

func TestTheDiffCarriesThreeLinesOfContextOnEachSide(t *testing.T) {
	// appspec/06 asks for "a unified diff", and three lines of context is what
	// that phrase means to every tool and every reader of one -- it is diff
	// -u's default and git's. A narrower diff is still a unified diff by
	// shape, so nothing about the format itself pins the number; only a case
	// that counts the lines does.
	//
	// Twelve lines with one changed in the middle, so there are more than
	// three unchanged lines available on each side. A fixture with three or
	// fewer cannot tell a context of three from a context of ten.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", numberedLines(12, 7, "changed"), 0o600)
	world.WriteMackup(".probrc", numberedLines(12, 0, ""), 0o600)

	stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

	// The header numbers the hunk from the first line of context: seven lines
	// starting at line 4 on each side.
	if want := "@@ -4,7 +4,7 @@"; !strings.Contains(stdout, want) {
		t.Errorf("backup stdout = %q, want the hunk header %q", stdout, want)
	}
	for _, want := range []string{" l04", " l05", " l06", "-l07", "+changed", " l08", " l09", " l10"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("backup stdout = %q, want the diff line %q", stdout, want)
		}
	}
	// The fourth line out on each side is beyond the context and must not be
	// printed. This is the half a diff with too MUCH context fails, and the
	// half that a case asserting only the presence of l04 would miss.
	for _, unwanted := range []string{"l03", "l11"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("backup stdout = %q, want %q outside the three lines of context", stdout, unwanted)
		}
	}
}

func TestChangesCloseTogetherShareAHunkAndFarApartDoNot(t *testing.T) {
	// The other half of what the context width decides: two changes whose
	// context regions touch are one hunk, because printing them apart would
	// repeat the lines between them, and two changes far apart are two.
	//
	// Both directions in one case. A program that never merged would pass a
	// case asserting only that distant changes are separate, and one that
	// merged everything into a single hunk would pass the opposite case --
	// the claim is the boundary between them, so both fixtures are needed.
	for _, test := range []struct {
		name        string
		first, next int
		wantHunks   int
	}{
		{"four lines apart", 10, 14, 1},
		{"twenty lines apart", 5, 25, 2},
	} {
		world := newSyncWorld(t, ".probrc")
		home := numberedLines(30, test.first, "first change")
		home = strings.Replace(home, fmt.Sprintf("l%02d\n", test.next), "second change\n", 1)
		world.WriteFile(".probrc", home, 0o600)
		world.WriteMackup(".probrc", numberedLines(30, 0, ""), 0o600)

		stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

		if got := hunkCount(stdout); got != test.wantHunks {
			t.Errorf("%s: backup printed %d hunks, want %d; stdout = %q",
				test.name, got, test.wantHunks, stdout)
		}
	}
}

func TestAOneLineRangeOmitsItsCountAndAnEmptyOneIsNumberedFromZero(t *testing.T) {
	// The two spellings diff(1) uses in a hunk header, which a reimplementation
	// writing "%d,%d" unconditionally gets wrong in both places.
	//
	// A range of exactly one line is written as the line number alone, and an
	// empty range is written as ",0" against the line BEFORE it -- so a file
	// with no lines at all is numbered from zero rather than from one. Neither
	// is decoration: they are what makes this output readable by `patch` and by
	// every tool that has ever read a unified diff.
	for _, test := range []struct {
		name           string
		home, storage  string
		wantHunkHeader string
	}{
		// One line on each side, changed. Both ranges are exactly one.
		{"one line each side", "home line\n", "storage line\n", "@@ -1 +1 @@"},
		// The destination is an EMPTY file, so its range holds nothing. There
		// is no first line to name, which is why the zero is not incremented.
		{"an empty destination", "the only line\n", "", "@@ -0,0 +1 @@"},
	} {
		world := newSyncWorld(t, ".probrc")
		world.WriteFile(".probrc", test.home, 0o600)
		world.WriteMackup(".probrc", test.storage, 0o600)

		stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

		if !strings.Contains(stdout, test.wantHunkHeader) {
			t.Errorf("%s: backup stdout = %q, want the hunk header %q",
				test.name, stdout, test.wantHunkHeader)
		}
	}
}

func TestEachKindOfDiffLineCarriesTheLevelAppspec07GivesIt(t *testing.T) {
	// appspec/07's diff decorations: file headers bold, hunk headers cyan (36),
	// added lines green (32), removed lines red (31) -- and a context line is
	// not a decoration at all, so it takes the normal-progress level (33).
	//
	// Asserted against the raw bytes, because the claim IS the escape
	// sequences: stripped of colour these six lines are the same six lines
	// whatever level each was printed at. It is the same reason
	// TestTheVerbosePerAppHeaderIsABoldNameBetweenBlueRules reads the raw
	// stream.
	//
	// The context line is the one worth a negative assertion. It is the line a
	// reimplementation is most likely to fold in with the additions -- it sits
	// in the same loop and it is the only one of the four with no marker
	// character -- and printing every unchanged line of every diff in green
	// says a file changed in ways it did not.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "keep\nhome\n", 0o600)
	world.WriteMackup(".probrc", "keep\nstorage\n", 0o600)

	result := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0)

	for _, want := range []struct {
		what string
		text string
	}{
		{"the destination file header, bold", "\x1b[1m--- "},
		{"the source file header, bold", "\x1b[1m+++ "},
		{"the hunk header, cyan", "\x1b[36m@@ -1,2 +1,2 @@\x1b[0m"},
		{"the removed line, red", "\x1b[31m-storage\x1b[0m"},
		{"the added line, green", "\x1b[32m+home\x1b[0m"},
		{"the context line, normal progress", "\x1b[33m keep\x1b[0m"},
	} {
		if !strings.Contains(result.Stdout, want.text) {
			t.Errorf("backup stdout = %q, want %s: %q", result.Stdout, want.what, want.text)
		}
	}
	if unwanted := "\x1b[32m keep"; strings.Contains(result.Stdout, unwanted) {
		t.Errorf("backup stdout = %q, want the context line NOT printed as an addition (%q)",
			result.Stdout, unwanted)
	}
}

func TestTwoFilesDifferingOnlyInAFinalNewlineSayHow(t *testing.T) {
	// Two files whose bytes differ can have the same LINES, in exactly one
	// way: one of them ends mid-line. Without the marker the diff of that pair
	// is EMPTY, so the user is told the file differs, shown nothing, and asked
	// whether to replace it -- which is the one outcome appspec/06's
	// diff-before-replace promise cannot survive.
	//
	// The marker is diff(1)'s own wording, so the output reads as a diff
	// rather than as this program's opinion of one.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "the line\n", 0o600)
	world.WriteMackup(".probrc", "the line", 0o600)

	stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

	if want := `\ No newline at end of file`; !strings.Contains(stdout, want) {
		t.Errorf("backup stdout = %q, want the marker %q under the header", stdout, want)
	}
	// The header is only printed when there IS detail, so its presence is the
	// evidence that the diff was not empty.
	if want := ".probrc differs between home and Mackup:"; !strings.Contains(stdout, want) {
		t.Errorf("backup stdout = %q, want the drift header %q", stdout, want)
	}
}

// -- The recursive directory comparison --------------------------------------

func TestADirectoryComparisonReadsContentsAndNotJustSizes(t *testing.T) {
	// appspec/06 says the directory arm is "compared recursively by content
	// (not shallow stat)", and the parenthesis is the whole of this case.
	//
	// The two files are the SAME SIZE and hold different bytes, which is the
	// one arrangement a size comparison cannot tell apart. Every directory
	// fixture elsewhere in the suite differs in length as well as in content,
	// so a program that compared sizes and stopped passes all of them and
	// silently skips this pair on every run.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile(".probdir/same-size", "aaaa\n", 0o600)
	world.WriteMackup(".probdir/same-size", "bbbb\n", 0o600)

	stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

	if want := "changed: same-size"; !strings.Contains(stdout, want) {
		t.Errorf("backup stdout = %q, want %q: two files of equal size and different content differ",
			stdout, want)
	}
	if want := "Are you sure that you want to replace it?"; !strings.Contains(stdout, want) {
		t.Errorf("backup stdout = %q, want the replace prompt: the directories are not identical", stdout)
	}
}

func TestTheDirectoryGroupsAreSortedWithinEachGroup(t *testing.T) {
	// appspec/06 asks for the three lists "sorted", and the walk's own order is
	// not sorted order: it visits each directory's entries in order and
	// descends immediately, so a name inside a subdirectory is emitted before
	// a later sibling of that subdirectory.
	//
	// The fixture is built to make the difference visible, because on almost
	// any other one the two orders agree. A common subdirectory "a" holds a
	// source-only entry, and there is a source-only "a.txt" beside it: the
	// walk emits "a/b" first because it descends into "a" before it reaches
	// "a.txt", while sorted order puts "a.txt" first -- "." sorts before "/".
	// Remove the sort and this is the case that changes.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile(".probdir/a/keep", "keep\n", 0o600)
	world.WriteFile(".probdir/a/b", "source only, inside\n", 0o600)
	world.WriteFile(".probdir/a.txt", "source only, beside\n", 0o600)
	world.WriteMackup(".probdir/a/keep", "keep\n", 0o600)

	stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

	beside := strings.Index(stdout, "only in source: a.txt")
	inside := strings.Index(stdout, "only in source: a/b")
	if beside < 0 || inside < 0 {
		t.Fatalf("backup stdout = %q, want both one-sided entries listed", stdout)
	}
	if beside > inside {
		t.Errorf("backup stdout = %q, want \"a.txt\" listed before \"a/b\": the list is sorted, not in walk order",
			stdout)
	}
}

func TestASymlinkedDirectoryInsideATreeMakesTheSecondBackupSilent(t *testing.T) {
	// The comparison has to be the fixed point of the copy that follows it, and
	// inside a tree that means classifying each entry with a FOLLOWING stat --
	// the opposite of the rule for the pair at the top, which appspec/06 makes
	// a question about the path.
	//
	// internal/syncfs's copy follows a symlinked directory and writes real
	// content, so after one backup the home side has a link where storage has
	// a real directory. A comparison that did not follow would call that pair
	// changed on every run: the user is prompted, agrees, the copy reproduces
	// exactly the state just called different, and the next run asks again.
	// This case is the second run, and its whole assertion is silence.
	//
	// Run with stdin at end-of-input, so "identical" is checkable rather than
	// asserted: a prompt reached with no answer available exits non-zero.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile("outside/leaf", "leaf\n", 0o600)
	world.WriteFile(".probdir/plain", "plain\n", 0o600)
	symlink(t, world.Path("outside"), world.Path(".probdir", "linked"))

	world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()
	expectContent(t, world.Mackup(".probdir", "linked", "leaf"), "leaf\n")

	before := world.Snapshot()
	second := world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()
	if second.StdoutText() != "" {
		t.Errorf("a second backup printed %q, want nothing: the tree it copied is what it compares against",
			second.Stdout)
	}
	world.ExpectUnchanged(before)
}
