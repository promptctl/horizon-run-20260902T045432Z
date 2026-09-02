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
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Outln writes s and a newline to stdout.
func (s *IO) Outln(text string) { fmt.Fprintln(s.Out, text) }

// Outf writes a formatted message to stdout. No newline is added.
func (s *IO) Outf(format string, args ...any) { fmt.Fprintf(s.Out, format, args...) }

// Errln writes s and a newline to stderr.
func (s *IO) Errln(text string) { fmt.Fprintln(s.Err, text) }

// Errf writes a formatted message to stderr. No newline is added.
func (s *IO) Errf(format string, args ...any) { fmt.Fprintf(s.Err, format, args...) }
