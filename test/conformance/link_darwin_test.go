//go:build conformance && darwin

// The macOS half of appspec/06 step 1's platform condition. Its partner is
// link_linux_test.go, whose header says why the pair are two files rather than
// one case branching on runtime.GOOS.

package conformance

import "testing"

func TestLinkLinksALibraryPathOnMacOS(t *testing.T) {
	// appspec/06 step 1: "on macOS there is no such restriction." The negative
	// half of the platform rule, and the half that stops the rule from being
	// applied everywhere -- a program that skipped ~/Library/ paths on both
	// systems satisfies every assertion its Linux partner makes.
	//
	// This is also the ordinary case rather than a corner: ~/Library/Preferences
	// is where the shipped catalog keeps most of what it manages on macOS, so a
	// program that skipped it here would sync almost nothing and still exit 0.
	world := newLibraryWorld(t)

	world.Run("link", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectLinkedIntoUnchangedStorage(t, world.Path(libraryFile), world.Mackup(libraryFile), "a macOS preference\n")
	expectLinkedIntoUnchangedStorage(t, world.Path(libraryControl), world.Mackup(libraryControl), "portable\n")
}
