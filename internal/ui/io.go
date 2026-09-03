// Package ui owns the program's two output streams.
//
// appspec/07-output-safety-lifecycle.md makes the stream a message lands on
// contract rather than cosmetics, so every message in the program is written
// through an IO rather than to os.Stdout/os.Stderr directly. That also lets the
// conformance tests capture output without a subprocess.
package ui

import (
	"bufio"
	"fmt"
	"io"
)

// IO is the process's standard streams.
//
// It records the first failed write instead of returning an error from every
// print call. Callers cannot act on a per-message write failure -- the only
// place to report one is the other stream -- but a run whose output never
// reached the user must not exit 0, so the failure is carried to the one place
// that owns the exit code. See WriteError.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer

	writeErr error

	// in buffers In across prompts. Created on the first ReadLine and kept
	// for the rest of the run; see ReadLine for why a per-call reader loses
	// answers.
	in *bufio.Reader
}

// Outln and Errln write text and a newline, uncoloured, to the named stream.
//
// They are the ONLY writers here that name a stream instead of a level, and
// they exist for one message: the argument parser's usage block, which
// appspec/07's colour scheme assigns no level and which appspec/02 declares
// human-facing wording rather than contract. It goes to stdout for --help and
// a bare invocation, and to stderr after a usage-error diagnostic -- the same
// text on either stream, which is why it cannot be a level.
//
// Everything else uses Say/Sayf and names its level, which picks the stream.
// The formatted spellings of these two were deleted along the way: an
// uncoloured writer that both names a stream and formats is the shape every
// misrouted message in appspec/07's "Do not generalize warnings -> stderr"
// paragraph would take, and nothing needs one.
func (s *IO) Outln(text string) { s.write(s.Out, "%s\n", text) }

// Errln writes text and a newline to stderr. See Outln.
func (s *IO) Errln(text string) { s.write(s.Err, "%s\n", text) }

// WriteError reports the first write failure on either stream, or nil if every
// message was delivered.
func (s *IO) WriteError() error { return s.writeErr }

func (s *IO) write(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil && s.writeErr == nil {
		s.writeErr = err
	}
}
