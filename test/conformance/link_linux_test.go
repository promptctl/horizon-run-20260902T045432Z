//go:build conformance && linux

// The Linux half of appspec/06 step 1's platform condition.
//
// Behind a GOOS build tag and not a runtime branch, because the rule IS the
// difference between two operating systems: a case that asked runtime.GOOS
// which assertion to make would be one case with two meanings, and the meaning
// it carried would depend on the machine it ran on. Two files instead, each
// stating one platform's outcome outright, and CI runs this one while a
// developer on macOS runs its partner. internal/app's
// TestOnlyLinuxAppliesTheLibraryRuleAndOnlyToPathsUnderIt asks the rule itself
// for both platforms at once, which is the half neither of these files can do.

package conformance

import (
	"strings"
	"testing"
)

func TestLinkSkipsALibraryPathOnLinuxAndCarriesOn(t *testing.T) {
	// appspec/06 step 1: "the file is ALLOWED TO BE SYNCED ON THIS PLATFORM.
	// The platform rule: on Linux, a file whose home path is under ~/Library/
	// is not synced (skipped)". appspec/00 "Platform assumptions" lists it as
	// one of exactly three places behavior differs between the two systems.
	//
	// Three assertions, because the skip has three parts a broken program can
	// get individually right. Nothing is created at the home path -- a Linux
	// machine does not grow a ~/Library. The run carries ON to the next file,
	// which is what distinguishes a per-file skip from a stop and from a run
	// that dropped the whole application. And the storage copy is left exactly
	// as it was, NOT clamped: a program that ran syncfs.Link and then declined
	// to record the result would still have narrowed the target's mode on its
	// way past, which is a write to the shared folder for a file this platform
	// is specified not to touch.
	world := newLibraryWorld(t)
	loosen(t, world.Mackup(libraryFile), 0o644)

	result := world.Run("--verbose", "link", probeKey).
		ExpectExit(0).ExpectSilentStderr()

	expectAbsent(t, world.Path(libraryFile))
	expectMode(t, world.Mackup(libraryFile), 0o644)
	expectContent(t, world.Mackup(libraryFile), "a macOS preference\n")
	expectLinkedIntoUnchangedStorage(t, world.Path(libraryControl), world.Mackup(libraryControl), "portable\n")

	stdout := result.StdoutText()
	if want := doingNothing + "\n  " + world.Path(libraryFile) + "\n"; !strings.Contains(stdout, want) {
		t.Errorf("--verbose link stdout = %q, want the skip trace %q", result.Stdout, want)
	}
	// The trace says WHICH rule skipped it. A user comparing two machines'
	// output is owed the reason, and "Doing nothing" alone is also what the
	// missing-copy and already-linked arms print.
	if !strings.Contains(stdout, "~/Library") {
		t.Errorf("--verbose link stdout = %q, want the trace to name the ~/Library rule that skipped the file", result.Stdout)
	}
	if strings.Contains(stdout, joinVerb+" "+libraryFile) {
		t.Errorf("--verbose link stdout = %q, want no progress line for a file it did not act on", result.Stdout)
	}
}

func TestLinkInstallDoesNotApplyTheLibraryRuleOnLinux(t *testing.T) {
	// appspec/00 "Platform assumptions" scopes the rule to "THE PLAIN `link`
	// COMMAND", and appspec/06 states it in that command's step 1 and in no
	// other. So the same path, on the same machine, is acted on by link install.
	//
	// It reads like an inconsistency and it is not one: the rule is about
	// pulling a macOS-only preference file DOWN onto a Linux machine that has
	// no use for it, where link install is pushing a file the user demonstrably
	// has -- it is sitting in their home directory -- up into the shared folder
	// for the macOS machine that will want it.
	//
	// Without this case the rule could be implemented one level too low, in the
	// executor or in syncfs, and every other case in the suite would pass.
	world := newSyncWorld(t, libraryFile)
	world.WriteFile(libraryFile, "written on Linux\n", 0o600)

	world.Run("link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectLinkedInto(t, world.Path(libraryFile), world.Mackup(libraryFile), "written on Linux\n")
}
