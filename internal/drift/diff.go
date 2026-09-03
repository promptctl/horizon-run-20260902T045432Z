package drift

import (
	"fmt"
	"strings"

	"github.com/promptctl/macklebox/internal/ui"
)

// context is how many unchanged lines a hunk carries on each side of a change
// -- the three that diff(1) and every unified diff a user has read use.
const context = 3

// maxEdits bounds the diff search, and the bound is on the edit DISTANCE
// rather than on the size of the files.
//
// The search below is O(D) in space per step and remembers every step, so two
// files with nothing in common cost O(D^2) memory where D is the number of
// insertions and deletions between them. That is nothing for the ordinary case
// -- a config file with a line changed has D of two, whatever its length -- and
// unbounded for the pathological one, which is a sync run allocating gigabytes
// while the user waits for a prompt.
//
// Past the bound the detail becomes the whole destination removed and the whole
// source added: still a correct unified diff, no longer a minimal one. Nothing
// legible is lost, because a thousand interleaved edits is already more than a
// person reads before answering a yes/no question.
const maxEdits = 1000

// noNewline is what diff(1) prints for a file whose last line has no
// terminator, and it is here for a reason beyond fidelity.
//
// Splitting "a\nb\n" and "a\nb" into lines gives the same two lines, so a pair
// differing ONLY in the final newline would be reported as differing -- they
// are not byte-identical -- and then produce an empty diff. That is the one
// disagreement between "identical" and "the detail is empty" that this package
// promises cannot happen, and appending this line to the side that lacks the
// terminator is what closes it.
//
// Where it lands is NOT what diff(1) does, and the difference is stated rather
// than implied. diff(1) prints the marker unprefixed, directly under the body
// line of the side that lacks the terminator, and shows that line as changed on
// both sides. Here the marker is a line of that side's own, so it appears with
// an added or removed prefix and the real last line stays context. Both say the
// same thing to a reader; this one needs no special case in the renderer, and
// appspec/06 asks for "a unified diff of their lines" without fixing either
// spelling.
const noNewline = `\ No newline at end of file`

// unified renders the difference between two sequences of lines as a unified
// diff, decorated with the levels appspec/07-output-safety-lifecycle.md
// assigns to diff output.
//
// before is the DESTINATION and after is the SOURCE, which is the direction a
// reader of this output needs and not the direction appspec/06 words the
// comparison in. The specification says "compares source vs. destination" and
// fixes no diff direction; what fixes it is what the diff is FOR. Promise 6 of
// appspec/00-overview.md calls it "diff-before-replace": the user is about to
// be asked whether to replace the destination with the source, so the
// destination is the "before" whose lines are removed and the source is the
// "after" whose lines are added. Printed the other way round, every "+" in the
// output would name a line that is about to be deleted.
func unified(before, after []string, beforeLabel, afterLabel string) []Line {
	script := edits(before, after)
	hunks := hunksOf(script)
	if len(hunks) == 0 {
		return nil
	}
	lines := []Line{
		{Level: ui.DiffFileHeader, Text: "--- " + beforeLabel},
		{Level: ui.DiffFileHeader, Text: "+++ " + afterLabel},
	}
	for _, hunk := range hunks {
		lines = append(lines, hunk.render(script)...)
	}
	return lines
}

// splitLines cuts a file's bytes into the lines a diff is taken over,
// reporting whether the last one was terminated.
//
// A trailing newline is a terminator, not an empty final line: without this an
// ordinary text file would show a spurious blank line at the end of every diff.
// The bool is what noNewline exists to spend.
func splitLines(data []byte) ([]string, bool) {
	text := string(data)
	if text == "" {
		return nil, true
	}
	terminated := strings.HasSuffix(text, "\n")
	if terminated {
		text = text[:len(text)-1]
	}
	return strings.Split(text, "\n"), terminated
}

// markIncomplete appends the no-newline marker to whichever side lacks a final
// terminator, when only one of them does.
//
// Only when one of them does: two files that both end mid-line agree about it,
// and marking both would put an identical line on each side that the diff would
// then match up and show as context, for a difference that is not there.
func markIncomplete(before []string, beforeTerminated bool, after []string, afterTerminated bool) ([]string, []string) {
	if beforeTerminated == afterTerminated {
		return before, after
	}
	if !beforeTerminated {
		return append(append([]string(nil), before...), noNewline), after
	}
	return before, append(append([]string(nil), after...), noNewline)
}

// An editKind is what happened to one line.
type editKind int

const (
	// keep is a line both sides have: context in the rendered diff.
	keep editKind = iota
	// remove is a line only the destination has.
	remove
	// insert is a line only the source has.
	insert
)

// An edit is one step of the script that turns before into after.
type edit struct {
	kind editKind
	// text is the line itself, taken from whichever side has it.
	text string
}

// edits computes a script turning before into after.
//
// Myers's algorithm: walk the edit graph by increasing edit distance, keeping
// for each diagonal the furthest point reached, and remember every step so the
// path can be recovered afterwards. It is chosen over a longest-common-
// subsequence table because its cost is driven by how DIFFERENT the files are
// rather than by how big they are, and the case this runs in on every file of
// every run is two files that are nearly the same.
//
// Common leading and trailing lines are matched off before the search starts.
// That is not only an optimisation: it is what keeps the search's memory
// proportional to the size of the change rather than to the size of the file,
// so a one-line edit in a ten-thousand-line file costs one step.
func edits(before, after []string) []edit {
	prefix := 0
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix &&
		before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}

	script := make([]edit, 0, len(before)+len(after))
	for _, line := range before[:prefix] {
		script = append(script, edit{keep, line})
	}
	script = append(script, middle(before[prefix:len(before)-suffix], after[prefix:len(after)-suffix])...)
	for _, line := range before[len(before)-suffix:] {
		script = append(script, edit{keep, line})
	}
	return script
}

// middle scripts the part of the two files that is left once the shared
// beginning and end are matched off.
func middle(before, after []string) []edit {
	if len(before) == 0 && len(after) == 0 {
		return nil
	}
	trace, complete := search(before, after)
	if !complete {
		// The bound was reached: everything removed, everything added. See
		// maxEdits.
		script := make([]edit, 0, len(before)+len(after))
		for _, line := range before {
			script = append(script, edit{remove, line})
		}
		for _, line := range after {
			script = append(script, edit{insert, line})
		}
		return script
	}
	return backtrack(before, after, trace)
}

// search runs the forward pass, returning the furthest-reaching points of each
// step and whether the end was reached within the bound.
//
// Each step's snapshot covers only the diagonals that step can have touched,
// plus the one on either side that the backward pass reads. Snapshotting the
// whole array instead would cost the length of the FILES on every step, which
// is the one thing the trimming above is there to avoid paying.
func search(before, after []string) ([]step, bool) {
	n, m := len(before), len(after)
	bound := n + m
	if bound > maxEdits {
		bound = maxEdits
	}
	// furthest[origin+k] is the furthest x reached on diagonal k = x - y, and
	// it is sized by the BOUND rather than by the files -- which is the whole
	// claim maxEdits makes, and was false here while the array was 2*(n+m)+1:
	// two half-million-line files with nothing in common allocated 16 MiB
	// before the search took a step, and the bound then stopped it at a
	// thousand. Only diagonals -bound..bound are ever reachable. The highest
	// index touched is origin+d, because the read of origin+k+1 happens only
	// on the k == -d arm and on k != d and so never reaches origin+d+1; the
	// lowest is origin-d. Both are inside 2*bound+1 entries.
	origin := bound
	furthest := make([]int, 2*bound+1)

	var trace []step
	for d := 0; d <= bound; d++ {
		trace = append(trace, window(furthest, origin, d))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && furthest[origin+k-1] < furthest[origin+k+1]) {
				x = furthest[origin+k+1]
			} else {
				x = furthest[origin+k-1] + 1
			}
			y := x - k
			for x < n && y < m && before[x] == after[y] {
				x, y = x+1, y+1
			}
			furthest[origin+k] = x
			if x >= n && y >= m {
				return trace, true
			}
		}
	}
	return nil, false
}

// A step is one snapshot of the forward pass: the furthest-reaching x on each
// diagonal it could have touched, and which diagonal the first of them is.
//
// first is recorded rather than re-derived. The backward pass needs the offset
// the snapshot was cut at, and computing it there from the origin and d is the
// same arithmetic written twice -- which is fine until a window is clipped at
// the start of the array, as the last one is when the search finishes at
// exactly the bound, and the two spellings disagree by one about every
// diagonal in it.
type step struct {
	first    int
	furthest []int
}

// window copies the diagonals from -(d+1) to d+1 out of the furthest-reaching
// array, which is every diagonal the backward pass can ask about at step d.
func window(furthest []int, origin, d int) step {
	low, high := origin-d-1, origin+d+2
	if low < 0 {
		low = 0
	}
	if high > len(furthest) {
		high = len(furthest)
	}
	// Sized to the window, not to the array it is cut from. Allocating the
	// full array and returning a slice of it would keep every step's snapshot
	// as large as the two FILES, which is the cost the trimming in edits and
	// the bound in maxEdits both exist to avoid -- and neither would have
	// helped, since the allocation is per step either way.
	snapshot := make([]int, high-low)
	copy(snapshot, furthest[low:high])
	return step{first: low - origin, furthest: snapshot}
}

// backtrack walks the recorded steps from the end of both files back to the
// start, turning the path into a script in reading order.
func backtrack(before, after []string, trace []step) []edit {
	x, y := len(before), len(after)

	var reversed []edit
	for d := len(trace) - 1; d >= 0; d-- {
		snapshot := trace[d]
		at := func(k int) int {
			index := k - snapshot.first
			if index < 0 || index >= len(snapshot.furthest) {
				return 0
			}
			return snapshot.furthest[index]
		}
		k := x - y
		var previousK int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			previousK = k + 1
		} else {
			previousK = k - 1
		}
		previousX := at(previousK)
		previousY := previousX - previousK

		for x > previousX && y > previousY {
			reversed = append(reversed, edit{keep, before[x-1]})
			x, y = x-1, y-1
		}
		if d > 0 {
			if x == previousX {
				reversed = append(reversed, edit{insert, after[previousY]})
			} else {
				reversed = append(reversed, edit{remove, before[previousX]})
			}
		}
		x, y = previousX, previousY
	}

	script := make([]edit, len(reversed))
	for i, step := range reversed {
		script[len(reversed)-1-i] = step
	}
	return script
}

// A hunk is a run of the script that is printed: every change, plus the
// context lines around it.
type hunk struct {
	// from and to are indexes into the script, half-open.
	from, to int
	// beforeStart and afterStart are the zero-based line numbers the hunk
	// begins at on each side.
	beforeStart, afterStart int
}

// hunksOf groups the script's changes into the runs a unified diff prints,
// merging two changes whose context overlaps into one hunk.
//
// Marking each changed step's neighbourhood and then taking the maximal runs
// gives that merge for free, and gives it right: two changes separated by at
// most twice the context are one hunk, because printing them apart would
// repeat the lines between them.
func hunksOf(script []edit) []hunk {
	printed := make([]bool, len(script))
	changes := 0
	for i, step := range script {
		if step.kind == keep {
			continue
		}
		changes++
		for j := i - context; j <= i+context; j++ {
			if j >= 0 && j < len(script) {
				printed[j] = true
			}
		}
	}
	if changes == 0 {
		return nil
	}

	var hunks []hunk
	beforeLine, afterLine := 0, 0
	for i := 0; i < len(script); {
		if !printed[i] {
			beforeLine, afterLine = advance(script[i], beforeLine, afterLine)
			i++
			continue
		}
		current := hunk{from: i, beforeStart: beforeLine, afterStart: afterLine}
		for i < len(script) && printed[i] {
			beforeLine, afterLine = advance(script[i], beforeLine, afterLine)
			i++
		}
		current.to = i
		hunks = append(hunks, current)
	}
	return hunks
}

// advance moves the two line counters past one script step.
func advance(step edit, beforeLine, afterLine int) (int, int) {
	switch step.kind {
	case remove:
		return beforeLine + 1, afterLine
	case insert:
		return beforeLine, afterLine + 1
	default:
		return beforeLine + 1, afterLine + 1
	}
}

// render turns one hunk into its header and body lines.
func (h hunk) render(script []edit) []Line {
	var beforeCount, afterCount int
	for _, step := range script[h.from:h.to] {
		beforeCount, afterCount = advance(step, beforeCount, afterCount)
	}

	lines := []Line{{
		Level: ui.DiffHunk,
		Text: fmt.Sprintf("@@ -%s +%s @@",
			span(h.beforeStart, beforeCount), span(h.afterStart, afterCount)),
	}}
	for _, step := range script[h.from:h.to] {
		switch step.kind {
		case remove:
			lines = append(lines, Line{Level: ui.DiffRemoved, Text: "-" + step.text})
		case insert:
			lines = append(lines, Line{Level: ui.DiffAdded, Text: "+" + step.text})
		default:
			// A context line is not decoration, and appspec/07's colour scheme
			// gives levels to the four decorations only -- file headers, added,
			// removed, hunk headers. So it takes the normal-progress level:
			// the same stream, and the level the rest of the run's narrative
			// is written at. It is deliberately not DiffFileHeader, which
			// would print every unchanged line of every diff in bold.
			lines = append(lines, Line{Level: ui.Progress, Text: " " + step.text})
		}
	}
	return lines
}

// span renders one side of a hunk header.
//
// The two conventions here are diff(1)'s and they are what makes the output
// readable by the tools and the people who already read unified diffs: line
// numbers are one-based, and a count of exactly one is left off. An empty
// range is numbered by the line BEFORE it, which is why start is not
// incremented when the count is zero -- there is no first line to name.
func span(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if count == 1 {
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}
