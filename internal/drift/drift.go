// Package drift answers the one question appspec/06-sync-operations.md "Drift
// detection" asks: are the source and the destination of a copy the same, and
// if not, what should the user be shown before being asked whether to replace
// one with the other.
//
// It is promise 6 of appspec/00-overview.md -- "diff-before-replace" -- and it
// is also the idempotency fixed point of appspec/06's per-file procedure: when
// the two paths are identical the file is skipped with no prompt, which is what
// makes "a second run with no underlying change does nothing and prompts for
// nothing" true.
//
// # Pure comparison
//
// Compare reads and returns. It creates nothing, changes nothing, prompts for
// nothing and writes to no stream: appspec/06 defines its result as the pair
// (identical, detail) and leaves what to do about it to the per-file procedure
// that called it. Print exists so that the caller does not have to know which
// of appspec/07's colour levels each kind of detail line takes, and it is a
// separate call for the same reason -- a dry run and a declined prompt both
// want the comparison without the output.
//
// The header that goes ABOVE the detail -- "<f> differs between <home and
// Mackup>:" -- is not written here. Its wording comes from the direction
// record of appspec/06's backup/restore table, which is the caller's, and it is
// a ui.Anomaly line rather than diff decoration. Note that ui.Anomaly is on
// STDOUT: appspec/07 puts the drift header and its diff body there under a
// paragraph headed "Do not generalize warnings -> stderr", which names this
// message specifically.
//
// # Symlinks: this package looks AT the path, the copy primitive looks THROUGH
//
// The pair being compared is classified with os.Lstat, so a symlink is a
// symlink. That is the opposite of internal/syncfs.Copy, which classifies with
// a following os.Stat so that copying a symlink writes the real file it points
// at, and the difference is deliberate on both sides: a copy is about content,
// and drift is about what is at the path. appspec/06 words the rule as "if
// either path is a symlink: treated as differing", which is a question about
// the path.
//
// A drift check written with the following stat that syncfs uses would compare
// the two links' TARGETS, find them identical, and report "already in sync" for
// a pair appspec/06 requires be reported as differing -- silently skipping a
// file the user was supposed to be prompted about. It is the easy mistake to
// make here precisely because the primitive one package over does the opposite.
//
// Inside a directory comparison the rule is the other way round, and
// compareTrees says why.
package drift

import (
	"bytes"
	"io"
	"os"
	"unicode/utf8"

	"github.com/promptctl/macklebox/internal/plist"
	"github.com/promptctl/macklebox/internal/ui"
)

// A Line is one line of drift detail with the appspec/07 level it is printed
// at.
//
// appspec/06 calls the detail a single value that "is empty when the paths are
// not content-comparable". It is carried as lines with levels because the
// detail is not one message: a unified diff's file headers, hunk headers,
// added and removed lines each take a different colour from appspec/07's
// scheme, and a caller handed one string would have to re-derive which line was
// which to print it. An empty Detail is appspec/06's empty detail exactly.
type Line struct {
	Level ui.Level
	Text  string
}

// A Result is appspec/06's (identical, detail) pair.
//
// The two fields cannot disagree: a Result is Identical only when there is no
// detail, and a differing Result has detail whenever the comparison could
// produce any. The half of that which takes work is the second -- a comparison
// reporting "differs" and then printing nothing under the header leaves the
// user at a prompt with no reason for it -- and it is why the text arm carries
// a marker for a missing final newline and why the plist arm decides identity
// from the same rendering it diffs.
type Result struct {
	Identical bool
	Detail    []Line
}

// Print writes the detail to the stream appspec/07 assigns each line's level,
// which for every level a drift line can carry is stdout.
//
// Nothing is printed for an identical result, and nothing for a differing one
// whose detail is empty: appspec/06 says that case gets "the plain prompt, no
// diff printed", so a placeholder line here would be the program inventing a
// message the specification does not have.
func (r Result) Print(streams *ui.IO) {
	for _, line := range r.Detail {
		streams.Say(line.Level, line.Text)
	}
}

// identical is the skip-with-no-prompt answer -- appspec/06's idempotency
// fixed point.
func identical() Result { return Result{Identical: true} }

// differs is the answer for a pair that is not content-comparable: differing,
// with no detail, which appspec/06 pairs with "the plain prompt, no diff
// printed".
func differs() Result { return Result{} }

// note is a differing answer whose detail is one line: appspec/06's "type
// mismatch: folder vs file" and "binary contents differ".
//
// Both take the file-header level. They are not added, removed or hunk lines,
// and of appspec/07's four diff decorations the file header is the one that
// names what is being reported rather than marking a change within it.
func note(text string) Result {
	return Result{Detail: []Line{{Level: ui.DiffFileHeader, Text: text}}}
}

// Compare reports whether the two paths hold the same thing and, when they do
// not, what to show the user before asking to replace the target.
//
// source and target are appspec/06's source and destination -- backup compares
// the home path against the mackup path and restore the other way round -- and
// the caller has already established that both exist. The arms below are that
// section's own list, in its order.
//
// A path that cannot be stat-ed at all takes the same arm as a symlink:
// differing, no detail. That is not one of appspec/06's cases because its
// caller has just found both paths present, so reaching it means the tree
// changed under the run; the conservative half of "differing" is the one that
// asks the user rather than the one that silently skips their file.
func Compare(source, target string) Result {
	sourceInfo, sourceErr := os.Lstat(source)
	targetInfo, targetErr := os.Lstat(target)
	if sourceErr != nil || targetErr != nil {
		return differs()
	}
	// "If either path is a symlink: treated as differing, with no diff
	// detail." Asked of the Lstat results, which is what makes it a question
	// about the paths; see the package doc.
	if isSymlink(sourceInfo) || isSymlink(targetInfo) {
		return differs()
	}

	switch {
	case sourceInfo.IsDir() && targetInfo.IsDir():
		return compareTrees(source, target)
	case sourceInfo.IsDir() && targetInfo.Mode().IsRegular():
		return note("type mismatch: folder vs file")
	case sourceInfo.Mode().IsRegular() && targetInfo.IsDir():
		return note("type mismatch: file vs folder")
	case sourceInfo.Mode().IsRegular() && targetInfo.Mode().IsRegular():
		return compareFiles(source, target)
	}
	// A socket, device or FIFO on either side. appspec/06 gives no comparison
	// for one -- its three arms are two files, two directories, and one of
	// each -- and there is no content to compare, so this is the same
	// differing-with-no-detail answer a symlink gets. It is reachable: the
	// per-file procedure only checks that the SOURCE is a regular file or
	// directory, so the destination is whatever is at that path.
	return differs()
}

// isSymlink reports whether an Lstat result describes a symbolic link.
func isSymlink(info os.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }

// compareFiles is appspec/06's "two regular files": the plist arm, then the
// UTF-8 text arm, then byte-for-byte, in that order.
//
// The order is the specification's and it is not interchangeable. A plist is
// valid UTF-8 in its XML spelling, so a text-first reader would diff a
// preference file's markup instead of its settings and would report drift for
// two files whose settings agree -- which is the whole reason appspec/06 puts
// the plist arm first.
//
// Both files are read whole. A comparison that streamed would answer the
// identical/differs question with less memory, but every arm that produces
// detail needs the contents anyway, and the sizes involved are config files.
func compareFiles(source, target string) Result {
	sourceBytes, err := os.ReadFile(source)
	if err != nil {
		// "If either file is unreadable, treated as differing with no detail
		// (plain prompt)."
		return differs()
	}
	targetBytes, err := os.ReadFile(target)
	if err != nil {
		return differs()
	}

	// appspec/06 gives each of the three arms below its own "identical if
	// equal", and for two of them -- the UTF-8 text arm and the byte-for-byte
	// arm -- this is that test. It is hoisted above them rather than written
	// twice, and it is asked of the BYTES so that two files differing only in
	// a final newline are not called identical by a comparison of their split
	// lines.
	//
	// This is load-bearing and not an optimisation, which is worth saying
	// because it was written as one: the comment here used to claim that
	// deleting it changed no answer, and injection disagreed. Without it two
	// identical text files reach compareText, which produces an empty diff and
	// no identical answer, and every unchanged file in every run is reported
	// as differing.
	//
	// It answers the plist arm's identical case early as well, and THAT part
	// is a saving rather than a decision -- two plists with the same bytes
	// render the same, so that arm would agree. It is also the only path
	// through this function that does not parse, which matters on the run
	// appspec/06's idempotency fixed point makes the common one: every file,
	// nothing changed.
	if bytes.Equal(sourceBytes, targetBytes) {
		return identical()
	}

	if detail, isPlistPair := comparePlists(sourceBytes, targetBytes, source, target); isPlistPair {
		return detail
	}
	if utf8.Valid(sourceBytes) && utf8.Valid(targetBytes) {
		return compareText(sourceBytes, targetBytes, source, target)
	}
	// "Else compared byte-for-byte; identical if equal, else the detail is
	// 'binary contents differ'." The equal case was answered above.
	return note("binary contents differ")
}

// comparePlists is the first arm: "if both parse as property-list (plist)
// files: compared by parsed content; identical if equal, else a unified diff of
// their pretty-printed structures". The second return reports whether the pair
// took this arm at all.
//
// Identity is decided from the same rendering the diff is taken over, rather
// than by walking the parsed values. internal/plist guarantees that equal
// renderings mean equal content, so this is not a weaker test -- and it is the
// one that cannot report "differs" and then print an empty diff.
func comparePlists(sourceBytes, targetBytes []byte, source, target string) (Result, bool) {
	sourceValue, err := plist.Parse(sourceBytes)
	if err != nil {
		return Result{}, false
	}
	targetValue, err := plist.Parse(targetBytes)
	if err != nil {
		return Result{}, false
	}
	sourceLines, targetLines := plist.Format(sourceValue), plist.Format(targetValue)
	if equalLines(sourceLines, targetLines) {
		return identical(), true
	}
	return Result{Detail: unified(targetLines, sourceLines, target, source)}, true
}

// compareText is the second arm: "else if both are readable as UTF-8 text:
// compared as text; identical if equal, else a unified diff of their lines".
//
// It answers only the second half. Its caller has already established that the
// bytes differ, so there is no identical case to reach here -- which is a
// statement about the caller and not a shortcut, and is why the byte
// comparison up there is described as this arm's own "identical if equal"
// rather than as a saving.
//
// What that leaves this function owing is a diff that is never empty. Two
// files with different bytes can have the same LINES, in exactly one way: one
// of them ends mid-line. markIncomplete is what makes that difference visible
// instead of producing a differing result with nothing under it.
func compareText(sourceBytes, targetBytes []byte, source, target string) Result {
	sourceLines, sourceTerminated := splitLines(sourceBytes)
	targetLines, targetTerminated := splitLines(targetBytes)
	targetLines, sourceLines = markIncomplete(targetLines, targetTerminated, sourceLines, sourceTerminated)
	return Result{Detail: unified(targetLines, sourceLines, target, source)}
}

// equalLines reports whether two renderings are the same.
func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sameContents reports whether two regular files hold the same bytes.
//
// Used by the directory arm, where appspec/06 asks only whether "every file
// matches byte-for-byte" and no diff of an individual file is produced. So it
// compares in chunks and stops at the first difference, rather than reading two
// whole trees into memory to answer a question about names.
//
// A file that cannot be read counts as different. The alternative -- reporting
// two files the program cannot read as matching -- would let an unreadable tree
// pass as "already in sync" and be skipped silently.
func sameContents(a, b string) bool {
	first, err := os.Open(a)
	if err != nil {
		return false
	}
	defer first.Close()
	second, err := os.Open(b)
	if err != nil {
		return false
	}
	defer second.Close()

	firstBuffer := make([]byte, 32*1024)
	secondBuffer := make([]byte, 32*1024)
	for {
		firstRead, firstErr := io.ReadFull(first, firstBuffer)
		secondRead, secondErr := io.ReadFull(second, secondBuffer)
		if !bytes.Equal(firstBuffer[:firstRead], secondBuffer[:secondRead]) {
			return false
		}
		// io.ReadFull reports a short final chunk as ErrUnexpectedEOF and an
		// empty one as EOF, and both mean the same thing here: that side is
		// finished. The two sides having finished together is what the byte
		// comparison above has already established.
		firstDone := firstErr == io.EOF || firstErr == io.ErrUnexpectedEOF
		secondDone := secondErr == io.EOF || secondErr == io.ErrUnexpectedEOF
		if firstDone || secondDone {
			// The conjunction is already implied: a chunk short on one side
			// only is a chunk of a different length, which the comparison
			// above has just rejected. Injection confirms it -- replacing this
			// with a bare true changes no answer any case here can see. It is
			// written out because this line is where "the same contents" is
			// defined, and "both files ended" is what that means.
			return firstDone && secondDone
		}
		if firstErr != nil || secondErr != nil {
			return false
		}
	}
}
