// Package ui owns the program's two output streams.
//
// appspec/07-output-safety-lifecycle.md makes the stream a message lands on
// contract rather than cosmetics, so every message in the program is written
// through an IO rather than to os.Stdout/os.Stderr directly. That also lets the
// conformance tests capture output without a subprocess.
package ui

import (
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
}

// Outln writes text and a newline to stdout.
func (s *IO) Outln(text string) { s.write(s.Out, "%s\n", text) }

// Outf writes a formatted message to stdout. No newline is added.
func (s *IO) Outf(format string, args ...any) { s.write(s.Out, format, args...) }

// Errln writes text and a newline to stderr.
func (s *IO) Errln(text string) { s.write(s.Err, "%s\n", text) }

// Errf writes a formatted message to stderr. No newline is added.
func (s *IO) Errf(format string, args ...any) { s.write(s.Err, format, args...) }

// WriteError reports the first write failure on either stream, or nil if every
// message was delivered.
func (s *IO) WriteError() error { return s.writeErr }

func (s *IO) write(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil && s.writeErr == nil {
		s.writeErr = err
	}
}
