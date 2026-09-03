package ui

import (
	"fmt"
	"io"
	"regexp"
)

// A Level is the class of a message: what it tells the user, not how it looks.
//
// appspec/07-output-safety-lifecycle.md makes the level the thing color
// conveys -- "color alone conveys the level; there are no textual level
// labels" -- and, separately, makes the stream each message class lands on
// contract rather than cosmetics. Those two facts are about the same classes,
// so they are one type here: naming a level picks the colour AND the stream
// together.
//
// That is the whole point of the type. The stream is contract, the wording is
// not, and a call site that says Errf is one refactor away from putting the
// drift header or the link-uninstall warning on stderr -- the two messages
// appspec/07 calls out by name under "Do not generalize warnings -> stderr",
// which are the ones a reader is most likely to get wrong. A call site that
// says ui.Anomaly cannot make that mistake, because it never names a stream.
type Level int

// appspec/07 "Colored output" lists these, in this order, followed by its
// diff-decoration bullet.
const (
	// Progress is normal progress and informational output: the per-file
	// "Backing up ..." lines, and the --version banner.
	Progress Level = iota
	// Anomaly is a non-fatal anomaly the run continues past: the drift
	// "differs between ..." header, and the link uninstall "does not point to
	// Mackup ... skipping" warning. Both are on STDOUT; see the type's doc.
	Anomaly
	// Success reports something completed.
	Success
	// Fatal is an error that ends the run: the "Error: ..." diagnostics that
	// precede a clean non-zero exit.
	Fatal
	// CopyFailure is a per-file copy failure that does NOT end the run, and
	// the end-of-run "incomplete" summary that follows one. Distinct from
	// Fatal in both colour and meaning: appspec/07 gives fatal errors bright
	// red and these red, because the first means the program stopped and the
	// second means it carried on.
	CopyFailure
	// Verbose is a trace shown only under --verbose.
	Verbose
	// DiffFileHeader decorates the file header of a diff or drift detail.
	DiffFileHeader
	// DiffAdded decorates an added line.
	DiffAdded
	// DiffRemoved decorates a removed line.
	DiffRemoved
	// DiffHunk decorates a hunk header.
	DiffHunk
	// AppRule decorates the rules around the per-application verbose header.
	AppRule
	// AppName decorates the application name inside that header.
	AppName

	// levelCount is one past the last level. It exists so a test can walk
	// every level and refuse one that was declared without being given a
	// colour and a stream -- see TestEveryLevelIsFullySpecified. Keep it last.
	levelCount
)

// stream names one of the program's two output streams. An enum rather than
// the io.Writer itself, because a Level is a package-level constant and the
// writers belong to an IO value.
type stream int

const (
	toStdout stream = iota
	toStderr
)

// A levelSpec is everything appspec/07 fixes about a message class.
type levelSpec struct {
	// sgr is the SGR parameter string, without the ESC [ and the m.
	sgr string
	// on is the stream appspec/07 "Output streams" assigns the class.
	on stream
	// why records where in appspec/07 the pair comes from, so a later edit
	// that "corrects" a surprising row has to argue with the spec rather than
	// with a bare number. Read by no code; that is the point of writing it
	// next to the value instead of in a comment further away.
	why string
}

// levels is the single definition of the scheme. Nothing else in the program
// names an SGR code or picks a stream for a message.
//
// The surprising rows are the ones to leave alone: Anomaly is a warning ON
// STDOUT, and CopyFailure is red where Fatal is bright red. Both are stated
// outright in appspec/07 and both look like mistakes to a reader who has not
// read it, which is why each carries its reason here.
var levels = map[Level]levelSpec{
	Progress:    {sgr: "33", on: toStdout, why: "appspec/07: normal progress / info -> yellow; progress lines are stdout"},
	Anomaly:     {sgr: "1;33", on: toStdout, why: `appspec/07: non-fatal anomaly -> bold yellow, and "Do not generalize warnings -> stderr" puts the drift header and the link-uninstall warning on STDOUT`},
	Success:     {sgr: "32", on: toStdout, why: "appspec/07: success -> green; it reports completed work, which is stdout"},
	Fatal:       {sgr: "91", on: toStderr, why: "appspec/07: fatal errors that exit -> bright red; fatal diagnostics are stderr"},
	CopyFailure: {sgr: "31", on: toStderr, why: "appspec/07: non-fatal copy-failure lines -> red; the copy-failure channel is stderr"},
	Verbose:     {sgr: "35", on: toStdout, why: "appspec/07: verbose-only traces -> magenta; verbose traces are stdout"},

	DiffFileHeader: {sgr: "1", on: toStdout, why: "appspec/07 diff decoration: file headers bold; diff detail is stdout"},
	DiffAdded:      {sgr: "32", on: toStdout, why: "appspec/07 diff decoration: added lines green"},
	DiffRemoved:    {sgr: "31", on: toStdout, why: "appspec/07 diff decoration: removed lines red"},
	DiffHunk:       {sgr: "36", on: toStdout, why: "appspec/07 diff decoration: hunk headers cyan"},
	AppRule:        {sgr: "34", on: toStdout, why: "appspec/07: per-app verbose header uses blue rules"},
	AppName:        {sgr: "1", on: toStdout, why: "appspec/07: per-app verbose header wraps a bold app name"},
}

const (
	escape = "\x1b["
	// reset is what every colored string ends with, per appspec/07.
	reset = escape + "0m"
)

// embeddedReset matches the SGR reset in text being colored. All three
// spellings, because a reset arriving from somewhere else is exactly the case
// this exists for and there is no reason to assume it is the canonical one:
// ESC[0m, the abbreviated ESC[m, and a zero-padded ESC[00m are the same
// instruction to a terminal.
var embeddedReset = regexp.MustCompile(`\x1b\[0*m`)

// Colorize wraps text in the level's colour, reset-safely.
//
// appspec/07: "a color is re-applied after any embedded reset code in the
// message so a nested reset does not strip color from the rest of a line.
// Every colored string is terminated with a reset."
//
// Re-applying after an embedded reset is what makes Colorize compose: a span
// coloured by an inner call ends in a reset, and an outer call re-applies its
// own colour after it, so the remainder of the outer message keeps its level
// instead of falling back to the terminal default. The per-application verbose
// header of appspec/07 -- blue rules around a bold name -- is exactly that
// shape, and without this it would print the trailing rule uncoloured.
//
// Exported because a message built from differently-coloured spans has to
// compose them before it is written. A whole-line message should use Say
// instead, which colours and routes in one step.
func Colorize(level Level, text string) string {
	spec, ok := levels[level]
	if !ok {
		// An undeclared level is a programming error, caught by
		// TestEveryLevelIsFullySpecified before it can ship. Returning the
		// text unchanged is the one response that cannot make output worse
		// than no colour at all.
		return text
	}
	open := escape + spec.sgr + "m"
	return open + embeddedReset.ReplaceAllString(text, reset+open) + reset
}

// Say writes one complete message at its level, coloured, newline-terminated,
// on the stream appspec/07 assigns that level.
//
// One call is one line. The colour opens and closes inside the call, so no
// message can be left with an unterminated colour by a caller that writes it
// in two pieces -- which is also why there is no unterminated ("Sayf without a
// newline") spelling of this. Use Colorize to build a span and pass the result
// here when a line mixes levels.
//
// The single exception is Prompt, and it is an exception to the NEWLINE and
// not to the colour: appspec/07 has the user type the answer on the question's
// own line, so that one message ends in a reset without ending in a newline.
//
// The newline is written OUTSIDE the reset, so a line ends in the default
// colour rather than carrying it into whatever the terminal prints next.
func (s *IO) Say(level Level, text string) {
	s.write(s.streamFor(level), "%s\n", Colorize(level, text))
}

// Sayf is Say with a format string. The message is formatted first and
// coloured as a whole, so a %s carrying its own escape sequences is made
// reset-safe along with everything else.
func (s *IO) Sayf(level Level, format string, args ...any) {
	s.Say(level, fmt.Sprintf(format, args...))
}

// streamFor resolves the writer a level's messages go to.
//
// An undeclared level lands on stderr, uncoloured. That is deliberate and it
// is the safer of the two wrong answers: a message the program cannot classify
// must not be injected into the stdout stream that appspec/07 promises a
// script can parse, and stderr is where a diagnostic belongs. It is unreachable
// while TestEveryLevelIsFullySpecified passes.
func (s *IO) streamFor(level Level) io.Writer {
	if spec, ok := levels[level]; ok && spec.on == toStdout {
		return s.Out
	}
	return s.Err
}

// StripColor removes every SGR sequence from text.
//
// It is here rather than in a test helper because the property it defines is
// the program's, not a test's: colour wraps whole messages and never splits
// one, so removing the sequences must give back the exact message. The
// conformance suite reads it through the same definition the program writes
// with, so the two cannot drift apart -- and it is what lets a case assert a
// literal contract token like "Options --force and --force-no are mutually
// exclusive." on output that is, by appspec/02, a colored diagnostic line.
func StripColor(text string) string { return sgrSequence.ReplaceAllString(text, "") }

// sgrSequence matches a CSI sequence terminated by m -- the SGR subset, which
// is all this program emits. Not a general ANSI matcher: a wider pattern would
// silently eat output that is not colour, and StripColor is used to check that
// what remains is exactly the message.
var sgrSequence = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// HasColor reports whether text carries an SGR sequence. Used by the
// conformance suite to assert that colour is emitted even when the streams are
// pipes, which appspec/07 requires: the program does not condition colour on
// whether stdout is a TTY.
//
// It asks the same pattern StripColor removes, rather than looking for a bare
// ESC. A looser test would answer yes for any escape sequence at all, so a
// program that had stopped colouring entirely but still emitted, say, a cursor
// move would satisfy the case that exists to catch exactly that.
func HasColor(text string) bool { return sgrSequence.MatchString(text) }
