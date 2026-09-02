package ui

import (
	"errors"
	"strings"
	"testing"
)

// TestEveryLevelIsFullySpecified refuses a level that was declared without
// being given a colour and a stream, and a spec entry for a level that no
// longer exists.
//
// Both directions, for the reason the conformance suite's no-world guard
// carries the same shape: a one-directional check passes forever once someone
// deletes the thing it was watching. Without the first half a new level
// silently prints uncoloured on stderr -- streamFor's deliberate fallback,
// which is the safe answer to a bug and not a behaviour to ship. Without the
// second half a renamed level leaves a spec row nothing reads, and the next
// reader takes it for the live definition.
func TestEveryLevelIsFullySpecified(t *testing.T) {
	for level := Level(0); level < levelCount; level++ {
		spec, ok := levels[level]
		if !ok {
			t.Errorf("level %d has no entry in levels: it would print uncoloured on stderr", level)
			continue
		}
		if spec.sgr == "" {
			t.Errorf("level %d has an empty SGR parameter", level)
		}
		if spec.why == "" {
			t.Errorf("level %d has no recorded reason; every row cites the appspec/07 sentence it comes from", level)
		}
		if spec.on != toStdout && spec.on != toStderr {
			t.Errorf("level %d has stream %d, want toStdout or toStderr", level, spec.on)
		}
	}
	for level := range levels {
		if level < 0 || level >= levelCount {
			t.Errorf("levels has an entry for %d, which is not a declared level", level)
		}
	}
}

// TestTheSchemeMatchesTheSpecification pins each level's SGR parameter and
// stream to the value appspec/07 "Colored output" and "Output streams" give
// it.
//
// Written out longhand rather than derived from the levels map, which would
// only assert that the map equals itself. These are the spec's numbers,
// transcribed once here and once in the program, so a typo has to happen twice
// in the same direction to survive.
func TestTheSchemeMatchesTheSpecification(t *testing.T) {
	for _, want := range []struct {
		level Level
		name  string
		sgr   string
		on    stream
	}{
		{Progress, "Progress", "33", toStdout},
		{Anomaly, "Anomaly", "1;33", toStdout},
		{Success, "Success", "32", toStdout},
		{Fatal, "Fatal", "91", toStderr},
		{CopyFailure, "CopyFailure", "31", toStderr},
		{Verbose, "Verbose", "35", toStdout},
		{DiffFileHeader, "DiffFileHeader", "1", toStdout},
		{DiffAdded, "DiffAdded", "32", toStdout},
		{DiffRemoved, "DiffRemoved", "31", toStdout},
		{DiffHunk, "DiffHunk", "36", toStdout},
		{AppRule, "AppRule", "34", toStdout},
		{AppName, "AppName", "1", toStdout},
	} {
		got := levels[want.level]
		if got.sgr != want.sgr {
			t.Errorf("%s SGR = %q, want %q", want.name, got.sgr, want.sgr)
		}
		if got.on != want.on {
			t.Errorf("%s stream = %d, want %d", want.name, got.on, want.on)
		}
	}
}

// TestFatalAndCopyFailureAreDifferentColors pins the distinction on its own,
// because it is the one a reader is most likely to flatten. appspec/07 gives
// fatal errors bright red (91) and non-fatal copy failures red (31): the
// colour is the only thing that says whether the program stopped.
func TestFatalAndCopyFailureAreDifferentColors(t *testing.T) {
	if levels[Fatal].sgr == levels[CopyFailure].sgr {
		t.Errorf("Fatal and CopyFailure are both %q; appspec/07 distinguishes bright red 91 from red 31, and colour alone conveys the level", levels[Fatal].sgr)
	}
}

// TestTheStdoutWarningsStayOnStdout is the negative appspec/07 states in its
// own voice: "Do not generalize warnings -> stderr." The drift header and the
// link-uninstall warning are Anomaly, and Anomaly is stdout.
func TestTheStdoutWarningsStayOnStdout(t *testing.T) {
	if levels[Anomaly].on != toStdout {
		t.Error(`Anomaly routes to stderr; appspec/07 puts the drift "differs between ..." header and the link uninstall "does not point to Mackup" warning on STDOUT, and says so under "Do not generalize warnings -> stderr"`)
	}
}

func TestColorizeWrapsAndTerminates(t *testing.T) {
	got := Colorize(Progress, "Backing up vim")
	if want := "\x1b[33mBacking up vim\x1b[0m"; got != want {
		t.Errorf("Colorize(Progress, ...) = %q, want %q", got, want)
	}
}

// TestColorizeReappliesAfterAnEmbeddedReset is appspec/07's reset-safety
// sentence, checked for every spelling of a reset a terminal accepts. Without
// it the remainder of a line after an embedded reset prints in the terminal's
// default colour, and appspec/07 makes colour the only thing carrying the
// level.
func TestColorizeReappliesAfterAnEmbeddedReset(t *testing.T) {
	for _, embedded := range []string{"\x1b[0m", "\x1b[m", "\x1b[00m"} {
		got := Colorize(Progress, "before"+embedded+"after")
		want := "\x1b[33mbefore\x1b[0m\x1b[33mafter\x1b[0m"
		if got != want {
			t.Errorf("Colorize over an embedded %q = %q, want %q", embedded, got, want)
		}
		if stripped := StripColor(got); stripped != "beforeafter" {
			t.Errorf("StripColor(%q) = %q, want the message back unchanged", got, stripped)
		}
	}
}

// TestColorizeComposes is the per-app verbose header of appspec/07 in
// miniature: blue rules around a bold name. The inner span ends in a reset,
// and without reset-safety the trailing rule would print uncoloured.
func TestColorizeComposes(t *testing.T) {
	line := Colorize(AppRule, "--- "+Colorize(AppName, "vim")+" ---")
	if StripColor(line) != "--- vim ---" {
		t.Errorf("StripColor(%q) = %q, want %q", line, StripColor(line), "--- vim ---")
	}
	if !strings.HasSuffix(line, "---\x1b[0m") {
		t.Errorf("composed line = %q; the text after the inner reset lost its colour", line)
	}
}

// TestStripColorReturnsTheMessage is the property the conformance suite leans
// on: colour wraps whole messages and never splits one, so removing the
// sequences gives the exact message back. A contract token stays contiguous
// and greppable inside a coloured diagnostic.
func TestStripColorReturnsTheMessage(t *testing.T) {
	const token = "Options --force and --force-no are mutually exclusive."
	colored := Colorize(Fatal, token)
	if got := StripColor(colored); got != token {
		t.Errorf("StripColor(Colorize(Fatal, token)) = %q, want %q", got, token)
	}
	if !strings.Contains(colored, token) {
		t.Errorf("colored = %q; the contract token is not contiguous inside it, so a script grepping for it would miss", colored)
	}
	if !HasColor(colored) {
		t.Errorf("HasColor(%q) = false, want true", colored)
	}
	if HasColor(token) {
		t.Errorf("HasColor(%q) = true over uncoloured text", token)
	}
}

// TestSayRoutesByLevel checks that naming a level is enough to land on the
// right stream -- the property that makes misrouting impossible at a call
// site, since no caller names a stream.
func TestSayRoutesByLevel(t *testing.T) {
	for _, c := range []struct {
		level          Level
		wantOut        bool
		wantParameters string
	}{
		{Progress, true, "33"},
		{Anomaly, true, "1;33"},
		{Success, true, "32"},
		{Verbose, true, "35"},
		{Fatal, false, "91"},
		{CopyFailure, false, "31"},
	} {
		var out, errOut strings.Builder
		streams := &IO{In: strings.NewReader(""), Out: &out, Err: &errOut}
		streams.Say(c.level, "message")

		landed, other := out.String(), errOut.String()
		if !c.wantOut {
			landed, other = errOut.String(), out.String()
		}
		if other != "" {
			t.Errorf("Say(level %d) also wrote %q to the other stream", c.level, other)
		}
		if want := "\x1b[" + c.wantParameters + "mmessage\x1b[0m\n"; landed != want {
			t.Errorf("Say(level %d) wrote %q, want %q", c.level, landed, want)
		}
	}
}

// TestSayPutsTheNewlineOutsideTheReset keeps a line from carrying its colour
// into whatever the terminal prints next.
func TestSayPutsTheNewlineOutsideTheReset(t *testing.T) {
	var out strings.Builder
	streams := &IO{In: strings.NewReader(""), Out: &out, Err: &strings.Builder{}}
	streams.Say(Progress, "done")
	if !strings.HasSuffix(out.String(), "\x1b[0m\n") {
		t.Errorf("Say wrote %q, want the newline after the reset", out.String())
	}
}

func TestSayfFormatsBeforeColoring(t *testing.T) {
	var errOut strings.Builder
	streams := &IO{In: strings.NewReader(""), Out: &strings.Builder{}, Err: &errOut}
	streams.Sayf(Fatal, "Error: %s", "the config file does not exist")
	want := "\x1b[91mError: the config file does not exist\x1b[0m\n"
	if errOut.String() != want {
		t.Errorf("Sayf wrote %q, want %q", errOut.String(), want)
	}
}

// TestColoredWritesReachTheWriteErrorLatch is the property
// macklebox-foundation-waw.1 left behind and this ticket had to preserve: IO
// latches the first failed write on either stream and app.Main turns it into a
// non-zero exit, so a run whose output never reached the user cannot report
// success. A colour wrapper that wrote to the io.Writer directly, rather than
// through IO.write, would drop that guarantee silently -- nothing else in the
// program would look any different.
func TestColoredWritesReachTheWriteErrorLatch(t *testing.T) {
	for _, level := range []Level{Progress, Anomaly, Success, Verbose, Fatal, CopyFailure} {
		boom := errors.New("no space left on device")
		streams := &IO{In: strings.NewReader(""), Out: failingWriter{boom}, Err: failingWriter{boom}}
		streams.Say(level, "this never lands")
		if !errors.Is(streams.WriteError(), boom) {
			t.Errorf("after Say(level %d) to a failing stream, WriteError() = %v, want the failure", level, streams.WriteError())
		}
	}
}

// TestNothingConditionsColorOnATerminal states the appspec/07 sentence "The
// program does not condition color on whether stdout is a TTY" where it is
// cheapest to check: an IO whose streams are plain in-memory writers -- as far
// from a terminal as a stream gets -- still gets colour. The conformance suite
// makes the same assertion against a real process writing down a pipe.
func TestNothingConditionsColorOnATerminal(t *testing.T) {
	var out strings.Builder
	streams := &IO{In: strings.NewReader(""), Out: &out, Err: &strings.Builder{}}
	streams.Say(Progress, "Backing up vim")
	if !HasColor(out.String()) {
		t.Errorf("Say wrote %q with no colour to a non-terminal stream", out.String())
	}
}
