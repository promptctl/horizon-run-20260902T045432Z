package drift

import (
	"math/rand"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/promptctl/macklebox/internal/ui"
)

// text renders a diff the way it would be printed, so a case can be written
// against what the user sees rather than against a slice of structs.
func text(lines []Line) string {
	rendered := make([]string, len(lines))
	for i, line := range lines {
		rendered[i] = line.Text
	}
	return strings.Join(rendered, "\n")
}

func linesOf(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// The output is pinned against diff(1)'s, which was run on these exact two
// files to produce the expectation below -- `diff -u before.txt after.txt` on
// macOS, its header lines dropped because they carry timestamps this program
// has no reason to print. Two changes seven lines apart, which is far enough to
// be two hunks and is what makes the second hunk's line numbers worth pinning:
// they are the numbers a reader checks a diff against, and a renderer that
// counted only one side's lines would still get the first hunk right.
func TestTheDiffIsTheOneDiffWouldHavePrinted(t *testing.T) {
	before := linesOf("alpha\nbravo\ncharlie\ndelta\necho\nfoxtrot\ngolf\nhotel\nindia\njuliet\nkilo\nlima")
	after := linesOf("alpha\nbravo\ncharlie\nDELTA\necho\nfoxtrot\ngolf\nhotel\nindia\njuliet\nkilo\nLIMA\nmike")

	want := strings.Join([]string{
		"--- target", "+++ source",
		"@@ -1,7 +1,7 @@",
		" alpha", " bravo", " charlie", "-delta", "+DELTA", " echo", " foxtrot", " golf",
		"@@ -9,4 +9,5 @@",
		" india", " juliet", " kilo", "-lima", "+LIMA", "+mike",
	}, "\n")
	if got := text(unified(before, after, "target", "source")); got != want {
		t.Errorf("unified diff:\n%s\n\nwant:\n%s", got, want)
	}
}

// The empty range of a whole-file deletion, which diff(1) writes "-1,2 +0,0":
// zero-length ranges are numbered by the line before them, so the start is not
// incremented. Verified against `diff -u` on the same two files.
func TestAnEmptyRangeIsNumberedTheWayDiffNumbersIt(t *testing.T) {
	want := "--- target\n+++ source\n@@ -1,2 +0,0 @@\n-a\n-b"
	if got := text(unified(linesOf("a\nb"), nil, "target", "source")); got != want {
		t.Errorf("unified diff:\n%s\n\nwant:\n%s", got, want)
	}
	want = "--- target\n+++ source\n@@ -0,0 +1,2 @@\n+a\n+b"
	if got := text(unified(nil, linesOf("a\nb"), "target", "source")); got != want {
		t.Errorf("unified diff:\n%s\n\nwant:\n%s", got, want)
	}
}

// A count of exactly one is left off the header, as diff(1) leaves it off.
func TestAOneLineRangeOmitsItsCount(t *testing.T) {
	got := text(unified(linesOf("only"), linesOf("only line"), "target", "source"))
	if want := "--- target\n+++ source\n@@ -1 +1 @@\n-only\n+only line"; got != want {
		t.Errorf("unified diff:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestOneChangeInALongFileProducesOneShortHunk(t *testing.T) {
	// The reason the diff is a diff and not the two files: a config file the
	// user has edited one line of must not print two hundred lines before the
	// prompt that asks whether to replace it.
	before := make([]string, 200)
	for i := range before {
		before[i] = "line " + strconv.Itoa(i)
	}
	after := append([]string(nil), before...)
	after[100] = "changed"

	lines := unified(before, after, "target", "source")
	// Two file headers, one hunk header, three lines of context on each side,
	// and the removed and added line.
	if len(lines) != 2+1+context*2+2 {
		t.Errorf("a one-line change printed %d lines:\n%s", len(lines), text(lines))
	}
	if got := text(lines); !strings.Contains(got, "@@ -98,7 +98,7 @@") {
		t.Errorf("hunk header missing or misnumbered:\n%s", got)
	}
}

func TestChangesCloseTogetherShareAHunkAndFarApartDoNot(t *testing.T) {
	// The merge rule: two changes separated by at most twice the context are
	// one hunk, because printing them apart would repeat the lines between
	// them. One line further and they are two.
	// Both boundaries were checked against `diff -u` on the same pair of
	// files before being written down: six unchanged lines between the two
	// changes is one hunk there, seven is two.
	build := func(gap int) []Line {
		before := make([]string, gap+2)
		for i := range before {
			before[i] = "line " + strconv.Itoa(i)
		}
		after := append([]string(nil), before...)
		after[0], after[gap+1] = "first", "last"
		return unified(before, after, "target", "source")
	}
	// Counted by level and not by looking for "@@" in the text: a hunk header
	// contains that token twice, which is how the first spelling of this case
	// reported two hunks for one and passed the boundary it was checking.
	headers := func(lines []Line) int {
		count := 0
		for _, line := range lines {
			if line.Level == ui.DiffHunk {
				count++
			}
		}
		return count
	}
	if got := build(2 * context); headers(got) != 1 {
		t.Errorf("changes %d lines apart should share one hunk:\n%s", 2*context, text(got))
	}
	if got := build(2*context + 1); headers(got) != 2 {
		t.Errorf("changes %d lines apart should be two hunks:\n%s", 2*context+1, text(got))
	}
}

// The property that makes the whole thing trustworthy: whatever path the search
// takes, the script it produces turns the destination into the source. Random
// inputs rather than chosen ones, because the cases a person picks are the ones
// they already thought about, and this algorithm's mistakes live in the paths
// they did not.
func TestTheEditScriptRebuildsTheFileItDescribes(t *testing.T) {
	random := rand.New(rand.NewSource(1))
	alphabet := []string{"a", "b", "c", "d"}
	sequence := func(length int) []string {
		out := make([]string, length)
		for i := range out {
			out[i] = alphabet[random.Intn(len(alphabet))]
		}
		return out
	}
	for trial := 0; trial < 2000; trial++ {
		before, after := sequence(random.Intn(12)), sequence(random.Intn(12))
		original, rebuilt := applied(edits(before, after))
		if !reflect.DeepEqual(nonEmpty(original), nonEmpty(before)) {
			t.Fatalf("the script's kept and removed lines are %v, want the destination %v", original, before)
		}
		if !reflect.DeepEqual(nonEmpty(rebuilt), nonEmpty(after)) {
			t.Fatalf("applying the script to %v gave %v, want the source %v", before, rebuilt, after)
		}
	}
}

// applied replays a script, returning the lines it says the destination held
// and the lines it produces for the source. Both halves matter: a script that
// rebuilt the source out of the wrong original describes an edit between two
// files that are not the ones compared.
func applied(script []edit) (original, rebuilt []string) {
	for _, step := range script {
		switch step.kind {
		case keep:
			rebuilt = append(rebuilt, step.text)
			original = append(original, step.text)
		case insert:
			rebuilt = append(rebuilt, step.text)
		case remove:
			original = append(original, step.text)
		}
	}
	return original, rebuilt
}

// nonEmpty maps a nil slice and an empty one to the same value, so that
// DeepEqual is comparing contents rather than how a slice came to be empty.
func nonEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

// The script is not merely correct, it is minimal: Myers finds the shortest
// edit script, and a bug that produced a valid but longer one would pass the
// case above unnoticed. Checked against a straightforward
// longest-common-subsequence table, which is slow and obviously right and is
// the reason the algorithm in the package is neither.
func TestTheEditScriptIsAsShortAsOneCanBe(t *testing.T) {
	random := rand.New(rand.NewSource(2))
	alphabet := []string{"a", "b", "c"}
	sequence := func(length int) []string {
		out := make([]string, length)
		for i := range out {
			out[i] = alphabet[random.Intn(len(alphabet))]
		}
		return out
	}
	for trial := 0; trial < 500; trial++ {
		before, after := sequence(random.Intn(10)), sequence(random.Intn(10))
		changes := 0
		for _, step := range edits(before, after) {
			if step.kind != keep {
				changes++
			}
		}
		if want := len(before) + len(after) - 2*longestCommon(before, after); changes != want {
			t.Fatalf("%v -> %v took %d edits, want %d", before, after, changes, want)
		}
	}
}

// longestCommon is the textbook table, here only to have a second opinion.
func longestCommon(a, b []string) int {
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			switch {
			case a[i-1] == b[j-1]:
				table[i][j] = table[i-1][j-1] + 1
			case table[i-1][j] >= table[i][j-1]:
				table[i][j] = table[i-1][j]
			default:
				table[i][j] = table[i][j-1]
			}
		}
	}
	return table[len(a)][len(b)]
}

func TestTwoFilesWithNothingInCommonFallBackToAWholeFileReplacement(t *testing.T) {
	// Past maxEdits the search stops and the detail becomes everything removed
	// and everything added: still a correct unified diff, no longer a minimal
	// one. The bound is on memory, and what this pins is that reaching it
	// produces output rather than a wrong answer or an allocation storm.
	// Half the lines are left alone, which is what makes the bound
	// observable: a search that ran to the end would keep them as context,
	// and the fallback cannot. A pair with nothing in common would produce
	// the same output either way and would pin nothing.
	const length = maxEdits + 2
	before := make([]string, length)
	after := make([]string, length)
	for i := range before {
		before[i] = "line " + strconv.Itoa(i)
		after[i] = before[i]
		if i%2 == 0 {
			after[i] = "changed " + strconv.Itoa(i)
		}
	}
	lines := unified(before, after, "target", "source")
	if got := text(lines); !strings.Contains(got, "@@ -1,1002 +1,1002 @@") {
		t.Fatalf("want one hunk covering both whole files, got:\n%s", strings.Join(strings.Split(got, "\n")[:4], "\n"))
	}

	counts := map[ui.Level]int{}
	for _, line := range lines {
		counts[line.Level]++
	}
	// Every line removed and every line added, except the last -- which is
	// unchanged and is matched off as a common suffix before the search runs
	// at all, so it survives as the one context line. A search that had run to
	// the end would have kept five hundred more.
	if counts[ui.DiffRemoved] != length-1 || counts[ui.DiffAdded] != length-1 {
		t.Fatalf("the fallback removed %d lines and added %d, want %d of each",
			counts[ui.DiffRemoved], counts[ui.DiffAdded], length-1)
	}
	if counts[ui.Progress] != 1 {
		t.Fatalf("the fallback kept %d context lines, want only the trimmed common suffix", counts[ui.Progress])
	}
}

func TestALongFileWithATinyChangeAtEachEndStillDiffsMinimally(t *testing.T) {
	// The one shape where the search runs on more lines than maxEdits and
	// still finishes inside the bound: trimming buys nothing because the two
	// files differ at the first line and at the last, but what is between them
	// is identical, so the edit distance is four. n+m is 2004 and d is 4.
	//
	// It is here because backtrack derives its own origin and must reach the
	// same value the search used. Every other case has n+m below maxEdits,
	// where the clamp is a no-op and an origin that ignored it would look
	// correct; this one reads every recorded step at the wrong offset without
	// it, and answers with a script that is not the shortest and need not even
	// rebuild the file.
	const common = 1000
	before := make([]string, 0, common+2)
	after := make([]string, 0, common+2)
	before = append(before, "head of the destination")
	after = append(after, "head of the source")
	for i := 0; i < common; i++ {
		before = append(before, "line "+strconv.Itoa(i))
		after = append(after, "line "+strconv.Itoa(i))
	}
	before = append(before, "tail of the destination")
	after = append(after, "tail of the source")

	lines := unified(before, after, "target", "source")
	counts := map[ui.Level]int{}
	for _, line := range lines {
		counts[line.Level]++
	}
	// One line at each end, both ways.
	if counts[ui.DiffRemoved] != 2 || counts[ui.DiffAdded] != 2 {
		t.Errorf("the diff removed %d lines and added %d, want two of each:\n%s",
			counts[ui.DiffRemoved], counts[ui.DiffAdded], text(lines))
	}
	// Two hunks: the ends are a thousand lines apart, far past the context
	// either side would need to merge them.
	if counts[ui.DiffHunk] != 2 {
		t.Errorf("the diff has %d hunks, want one at each end:\n%s", counts[ui.DiffHunk], text(lines))
	}
	original, rebuilt := applied(edits(before, after))
	if !reflect.DeepEqual(original, before) || !reflect.DeepEqual(rebuilt, after) {
		t.Error("the script does not describe an edit between the two files it was given")
	}
}

func TestASearchThatFinishesAtExactlyTheBoundIsStillReadCorrectly(t *testing.T) {
	// The boundary of maxEdits from the inside. The case below it pins what
	// happens PAST the bound, where the answer is the whole-file fallback, and
	// a bound checked from one side only is a bound that is off by one and
	// says nothing -- so this pins that a search finishing on its last
	// permitted step still produces the minimal script rather than falling
	// back. It is also the only step whose snapshot is clipped at the start of
	// the furthest-reaching array, which is the step a backward pass that
	// re-derived its offset would read one diagonal out.
	//
	// So: four hundred and ninety-nine lines changed at the head, one at the
	// tail, two thousand identical between them. Trimming buys nothing because
	// both ends differ, leaving n+m at five thousand, well past maxEdits; the
	// edit distance is exactly a thousand, so the search finishes on its last
	// permitted step rather than falling back.
	const head = 499
	const common = 2000
	before := make([]string, 0, head+common+1)
	after := make([]string, 0, head+common+1)
	for i := 0; i < head; i++ {
		before = append(before, "destination "+strconv.Itoa(i))
		after = append(after, "source "+strconv.Itoa(i))
	}
	for i := 0; i < common; i++ {
		before = append(before, "line "+strconv.Itoa(i))
		after = append(after, "line "+strconv.Itoa(i))
	}
	before = append(before, "tail of the destination")
	after = append(after, "tail of the source")

	script := edits(before, after)
	changes := 0
	for _, step := range script {
		if step.kind != keep {
			changes++
		}
	}
	// Exactly the bound: one more and this would be the fallback, whose script
	// is every line of both files and which would pass a weaker assertion.
	if changes != maxEdits {
		t.Errorf("the script took %d edits, want the %d that reach the bound exactly", changes, maxEdits)
	}
	original, rebuilt := applied(script)
	if !reflect.DeepEqual(original, before) || !reflect.DeepEqual(rebuilt, after) {
		t.Error("the script does not describe an edit between the two files it was given")
	}
}

func TestTheSearchsMemoryIsAFunctionOfTheBoundAndNotOfTheFiles(t *testing.T) {
	// The claim maxEdits makes, and the one three comments in diff.go make
	// about it: past the bound the search's cost is the bound's, whatever the
	// files are. It was false while the furthest-reaching array was sized
	// 2*(len(before)+len(after))+1 -- in exactly the case the bound exists
	// for, two files with nothing in common, that array is the largest
	// allocation the search makes and grows with the input while the bound
	// stays a constant.
	//
	// Stated as growth rather than as a ceiling on purpose. The search
	// legitimately allocates a few megabytes of per-step snapshots at
	// maxEdits, which is bound-shaped and correct; a case written against an
	// absolute figure would be pinning that constant instead of this property.
	distinct := func(count int, side string) []string {
		lines := make([]string, count)
		for i := range lines {
			lines[i] = side + strconv.Itoa(i)
		}
		return lines
	}
	cost := func(count int) uint64 {
		before, after := distinct(count, "before "), distinct(count, "after ")
		var start, end runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&start)
		if _, complete := search(before, after); complete {
			t.Fatalf("%d lines with nothing in common completed within the bound", count)
		}
		runtime.ReadMemStats(&end)
		return end.TotalAlloc - start.TotalAlloc
	}

	small := cost(2 * maxEdits)
	large := cost(200 * maxEdits)
	// A megabyte of slack for the allocator, against the eight-plus megabytes
	// the unbounded array cost at this size.
	if large > small+1<<20 {
		t.Errorf("the search allocated %d bytes on %d lines and %d on %d: it is sized by the files, not by the bound",
			large, 200*maxEdits, small, 2*maxEdits)
	}
}

// The levels appspec/07 assigns to diff output, on the lines that carry them.
// Nothing else in the program picks a colour for a diff, so if this mapping is
// wrong every diff the program ever prints is wrong.
func TestEachKindOfDiffLineCarriesTheLevelAppspec07GivesIt(t *testing.T) {
	lines := unified(linesOf("kept\nremoved"), linesOf("kept\nadded"), "target", "source")
	want := []ui.Level{
		ui.DiffFileHeader, // --- target
		ui.DiffFileHeader, // +++ source
		ui.DiffHunk,       // @@
		ui.Progress,       // context
		ui.DiffRemoved,
		ui.DiffAdded,
	}
	if len(lines) != len(want) {
		t.Fatalf("diff has %d lines, want %d:\n%s", len(lines), len(want), text(lines))
	}
	for i, level := range want {
		if lines[i].Level != level {
			t.Errorf("line %d (%q) is level %d, want %d", i, lines[i].Text, lines[i].Level, level)
		}
	}
}
