// Package fault carries a startup failure together with which of the two
// regimes of appspec/01-architecture.md section 6 it belongs to.
//
// appspec/01 splits config-load failures in two. A GUARDED failure is one the
// reference implementation anticipates: it prints a clean diagnostic and exits
// 1. An UNGUARDED failure is one it does not: the error escapes as a traceback
// and the process exits non-zero. appspec/07's error table places every
// condition in one column or the other, and appspec/01 says outright that
// "which cases fall in which regime is itself contract as observed".
//
// Both specifications then permit a reimplementation to "collapse the
// unguarded cases into clean single-line exits", requiring only the shared
// post-condition -- diagnostic on stderr, nothing on stdout, no filesystem
// change, non-zero exit. This program takes that permission for the traceback
// and declines it for the distinction: a Go program has no traceback to print,
// but erasing WHICH regime a failure is in would make the done-claim of
// macklebox-resolvers-5iw.2 -- "the guarded vs unguarded failure split matches
// the table in appspec/07" -- unobservable, and an unobservable claim is one
// nothing can hold the program to.
//
// So the split survives in the one place appspec/02 makes machine-readable for
// the unguarded rows: their shape. A guarded failure prints the sentence
// appspec/07 gives it, under the "Error: " prefix those rows carry. An
// unguarded one prints under "mackup: " and names the offending value, which
// is exactly what appspec/02's exit-code table requires of that regime ("write
// a diagnostic naming the offending value to stderr"). A reader can tell them
// apart; a script matching appspec/07's guarded tokens is unaffected.
//
// The exit code does NOT distinguish them, deliberately. appspec/02 gives
// guarded failures exit 1 and unguarded ones "nonzero", and in the reference
// an uncaught exception exits 1 as well -- so the two are indistinguishable by
// code there, and inventing a second code here would be a contract this
// program made up rather than one it reproduces.
package fault

import (
	"errors"
	"fmt"
)

// A Regime is one of the two config-failure regimes of appspec/01 section 6.
type Regime int

const (
	// Guarded is a failure the program anticipates and reports as the clean
	// diagnostic appspec/07 specifies for it.
	Guarded Regime = iota
	// Unguarded is a failure appspec/07 records as escaping the reference's
	// error handling. Its diagnostic names the offending value.
	Unguarded
)

// String names a regime as appspec/01 section 6 writes it.
func (r Regime) String() string {
	if r == Unguarded {
		return "unguarded"
	}
	return "guarded"
}

// An Error is a startup failure and the diagnostic the program writes for it.
//
// Message is the whole text, already in its final shape and possibly spanning
// several lines -- appspec/07 gives two of the guarded rows as multi-line
// blocks. It is built by the constructors below rather than by the caller, so
// that the prefix a regime carries is decided in one place.
type Error struct {
	Regime  Regime
	Message string
}

// Error renders the failure as its diagnostic.
func (e *Error) Error() string { return e.Message }

// Guardedf reports a guarded failure whose diagnostic appspec/07 gives as a
// single "Error: ..." sentence. The prefix is added here; callers write the
// sentence.
func Guardedf(format string, args ...any) *Error {
	return &Error{Regime: Guarded, Message: "Error: " + fmt.Sprintf(format, args...)}
}

// GuardedBlock reports a guarded failure whose diagnostic appspec/07 gives as
// a multi-line block with no "Error: " opener -- the legacy-config refusal and
// the "Unable to find your <provider> =(" message. The text is written to
// stderr as it stands.
func GuardedBlock(text string) *Error {
	return &Error{Regime: Guarded, Message: text}
}

// Unguardedf reports an unguarded failure. appspec/02 requires the diagnostic
// to name the offending value, so the format string must include it; the
// "mackup: " prefix that tells the regime apart is added here.
func Unguardedf(format string, args ...any) *Error {
	return &Error{Regime: Unguarded, Message: "mackup: " + fmt.Sprintf(format, args...)}
}

// RegimeOf reports which regime a failure belongs to, and whether it declared
// one at all.
//
// A plain error -- one from the standard library that reached a stage boundary
// without being classified -- is not silently called guarded. The caller
// prints it as a fatal diagnostic either way; what it must not do is let an
// unclassified failure be counted as evidence that the split is implemented.
func RegimeOf(err error) (Regime, bool) {
	var fault *Error
	if errors.As(err, &fault) {
		return fault.Regime, true
	}
	return Guarded, false
}

// Diagnostic renders the text the program writes to stderr for a failure.
//
// An error that carries no regime is given the guarded "Error: " shape, which
// is the safe answer of the two: it is what appspec/07 specifies for most
// startup rows, and it cannot make an unclassified failure look like it names
// an offending value when it does not.
func Diagnostic(err error) string {
	var fault *Error
	if errors.As(err, &fault) {
		return fault.Message
	}
	return "Error: " + err.Error()
}
