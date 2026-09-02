package fault

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestTheTwoRegimesAreDistinguishableInTheDiagnostic(t *testing.T) {
	// The point of this package, and the half of macklebox-resolvers-5iw.2's
	// done-claim that would otherwise be unobservable. appspec/01 section 6
	// says "which cases fall in which regime is itself contract as observed";
	// if both regimes produced the same text at the same exit code, nothing
	// could hold the program to that.
	guarded := Diagnostic(Guardedf("The config file '%s' does not exist. Aborting.", "/home/u/nope.cfg"))
	unguarded := Diagnostic(Unguardedf("Unknown storage engine: %s", "onedrive"))

	if guarded == unguarded {
		t.Fatal("the two regimes render identically, so the split appspec/01 section 6 calls contract is unobservable")
	}
	if !strings.HasPrefix(guarded, "Error: ") {
		t.Errorf("a guarded diagnostic = %q, want appspec/07's \"Error: \" opener", guarded)
	}
	if strings.HasPrefix(unguarded, "Error: ") {
		t.Errorf("an unguarded diagnostic = %q, want a shape distinct from the guarded rows", unguarded)
	}
}

func TestAGuardedDiagnosticIsExactlyAppspec07sSentence(t *testing.T) {
	// Two rows of appspec/07's table are given literally, and scripts match
	// them. The constructor adds the prefix and nothing else -- no trailing
	// newline, no wrapping, no second sentence -- so what reaches stderr is
	// the row.
	err := Guardedf("The config file '%s' is not in your home directory. Aborting.", "/etc/hosts")
	want := "Error: The config file '/etc/hosts' is not in your home directory. Aborting."
	if got := Diagnostic(err); got != want {
		t.Errorf("Diagnostic = %q, want %q", got, want)
	}
	if err.Error() != want {
		t.Errorf("Error() = %q, want it to render the same text Diagnostic does", err.Error())
	}
}

func TestAGuardedBlockIsWrittenAsItStands(t *testing.T) {
	// The two multi-line guarded rows -- the legacy-config refusal and
	// "Unable to find your <provider> =(" -- carry no "Error: " opener in
	// appspec/07's shape column, so the constructor for them adds nothing.
	block := "Unable to find your Dropbox install =(\nsecond line\nthird line"
	if got := Diagnostic(GuardedBlock(block)); got != block {
		t.Errorf("Diagnostic = %q, want the block verbatim", got)
	}
	if regime, declared := RegimeOf(GuardedBlock(block)); !declared || regime != Guarded {
		t.Errorf("a block failure is %v (declared %v), want guarded", regime, declared)
	}
}

func TestAnUnguardedDiagnosticNamesItsValue(t *testing.T) {
	// appspec/02's exit-code table states the machine-readable half of the
	// unguarded regime: "write a diagnostic naming the offending value to
	// stderr". The prefix is this package's; the value is the caller's, and
	// this checks the prefix does not displace it.
	got := Diagnostic(Unguardedf("Unknown storage engine: %s", "onedrive"))
	if !strings.Contains(got, "onedrive") {
		t.Errorf("Diagnostic = %q, want the offending value inside it", got)
	}
	if !strings.HasPrefix(got, "mackup: ") {
		t.Errorf("Diagnostic = %q, want the unguarded opener", got)
	}
}

func TestARegimeSurvivesBeingWrapped(t *testing.T) {
	// Errors cross three package boundaries between a resolver and the
	// pipeline that prints them, and a caller is free to add context on the
	// way. RegimeOf and Diagnostic both unwrap, so wrapping does not silently
	// demote a classified failure to an unclassified one.
	wrapped := fmt.Errorf("resolving the storage root: %w", Unguardedf("Unknown storage engine: %s", "onedrive"))
	regime, declared := RegimeOf(wrapped)
	if !declared || regime != Unguarded {
		t.Errorf("a wrapped unguarded failure is %v (declared %v), want unguarded", regime, declared)
	}
	// The inner diagnostic is what the user sees: the wrapper's prose is
	// context for a reader of the code, not a line of appspec/07's table.
	if got := Diagnostic(wrapped); got != "mackup: Unknown storage engine: onedrive" {
		t.Errorf("Diagnostic = %q, want the failure's own diagnostic", got)
	}
}

func TestAnUnclassifiedErrorIsNotCountedAsGuarded(t *testing.T) {
	// RegimeOf reports two things because a caller needs both. A plain error
	// that reached a stage boundary without being classified must still be
	// printable -- Diagnostic gives it the guarded shape, which is the safe
	// one -- but it must not be evidence that the split is implemented, or a
	// test asserting "this row is guarded" would pass over a resolver that
	// classified nothing at all.
	plain := errors.New("something went wrong")
	if _, declared := RegimeOf(plain); declared {
		t.Error("a plain error declared a regime")
	}
	if got := Diagnostic(plain); got != "Error: something went wrong" {
		t.Errorf("Diagnostic = %q, want the guarded shape for an unclassified failure", got)
	}
}

func TestARegimeNamesItself(t *testing.T) {
	// Read by test failures rather than by the program, which is the point:
	// "Load failed guarded, want unguarded" says what went wrong, where
	// "Load failed 0, want 1" does not.
	if Guarded.String() != "guarded" || Unguarded.String() != "unguarded" {
		t.Errorf("the regimes name themselves %q and %q", Guarded, Unguarded)
	}
	if got := Regime(99).String(); got != "guarded" {
		t.Errorf("an unknown regime names itself %q; the zero-valued default is guarded", got)
	}
}
