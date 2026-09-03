//go:build conformance && unix

// The `link install` case that needs a directory the process cannot search,
// which is a Unix permission question.
//
// Behind the `unix` build tag rather than guarded at runtime, for the reason
// sync_unix_test.go and compare_unix_test.go give: mode bits do not deny a stat
// on every GOOS the toolchain compiles for, and a file that does not build says
// so more clearly than a case that silently passes. appspec/00 "Platform
// assumptions" targets Unix-like systems.

package conformance

import (
	"strings"
	"testing"
)

func TestAHomePathThatCannotBeInspectedIsSkippedRatherThanFailingTheRun(t *testing.T) {
	// This is the one place in the program where a stat error is deliberately
	// read as "not there", and it is the opposite of what backup does with the
	// same condition one file over (TestASourceThatCannotBeInspectedIsAFailure-
	// AndNotAnAbsence). The case exists so that the difference is a decision
	// with a test behind it rather than an inconsistency waiting to be tidied
	// away in either direction.
	//
	// Why the copy-side answer does not transfer, in the specification's own
	// terms. Backup's fix rests on appspec/01 section 5's unconditional "A
	// partial backup/restore can never exit 0", so a copy run that silently
	// skipped an unreadable source and exited 0 contradicted the specification
	// itself. appspec/00 promise 9 then withholds exactly that guarantee from
	// this strategy -- it is titled "(copy mode)" and says "this honesty is
	// asymmetric -- the link strategy does not uphold it as cleanly" -- and
	// appspec/07's error table has no row for an uninspectable home path under
	// any link command. Section 5's permission to improve link failures is
	// bounded by "without changing any successful-run behavior", and the
	// reference completes this run and exits 0.
	//
	// So: exit 0, no diagnostic, and the run carries on to the next file in
	// sorted order. The verbose trace is what keeps the skip observable at all,
	// and it is asserted here because silence plus exit 0 with nothing said is
	// the outcome that would make this decision indefensible rather than merely
	// conservative.
	world := newSyncWorld(t, ".probdir/inner", ".zprobrc")
	world.WriteFile(".probdir/inner", "cannot be read\n", 0o600)
	world.WriteFile(".zprobrc", "carried on\n", 0o600)
	denySearch(t, world.Path(".probdir"), "inner")

	result := world.Run("--verbose", "link", "install", probeKey).
		ExpectExit(0).ExpectSilentStderr()

	if want := doingNothing + "\n  " + world.Path(".probdir", "inner") + "\n"; !strings.Contains(result.StdoutText(), want) {
		t.Errorf("link install stdout = %q, want the skip trace %q for a home path it could not stat", result.Stdout, want)
	}
	if strings.Contains(result.StdoutText(), linkVerb+" .probdir/inner") {
		t.Errorf("link install stdout = %q, want no progress line for a file it did not act on", result.Stdout)
	}
	// The run carried on rather than stopping at the file it could not read,
	// which is the half that distinguishes a skip from the fail-hard regime of
	// appspec/01 section 5.
	expectLinkedInto(t, world.Path(".zprobrc"), world.Mackup(".zprobrc"), "carried on\n")
}
