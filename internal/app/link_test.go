package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/promptctl/macklebox/internal/fault"
)

// The unit half of the link strategy: the pieces of appspec/06's link commands
// that can be asked a question directly.
//
// test/conformance observes the program through argv and two streams, so it can
// pin what a run left on disk and what it printed. What it cannot show is the
// difference between a function that answered a question and one that never
// asked it -- and for link install exactly one function turns on that
// difference, because it discards an error on purpose. That is what this file
// is mostly about.

func TestLinkableSourceReadsAnUnreadableHomePathAsAbsent(t *testing.T) {
	// The decision recorded at linkableSource, asserted rather than described.
	//
	// sourcePresent returns (bool, error) so that backup CANNOT read a stat
	// failure as an absence; appspec/01 section 5 makes "a partial
	// backup/restore can never exit 0" unconditional, so a copy run that
	// skipped an unreadable source and exited 0 broke the specification. link
	// install has no such clause -- appspec/00 promise 9 is titled "(copy
	// mode)" and says the link strategy "does not uphold it as cleanly", and
	// appspec/07's table has no row for the condition under any link command --
	// so the reference's two-valued answer stands here and the error is
	// dropped.
	//
	// The two rows together are the whole claim: the function must agree with
	// sourcePresent wherever sourcePresent has an answer, and must say false
	// where it has none. A function that returned false unconditionally passes
	// the second row alone.
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	// A path UNDER a regular file is the portable stat failure -- ENOTDIR, not
	// ENOENT -- and the same shape TestStepOnesTestAdmitsFilesAndDirectories-
	// AndFollowsSymlinks uses for the third answer one file over. The
	// conformance suite's unix case carries the EACCES shape a real home
	// reaches this through.
	unreadable := filepath.Join(file, "under-a-file")
	if _, err := sourcePresent(unreadable); err == nil {
		t.Fatalf("the fixture is wrong: sourcePresent(%s) reported no error, so this case would assert nothing", unreadable)
	}

	for _, test := range []struct {
		what string
		path string
		want bool
	}{
		{"a regular file", file, true},
		{"a directory", root, true},
		{"nothing at all", filepath.Join(root, "absent"), false},
		{"a path it cannot stat", unreadable, false},
	} {
		if got := linkableSource(test.path); got != test.want {
			t.Errorf("linkableSource(%s) = %v, want %v", test.what, got, test.want)
		}
	}
}

func TestLinkInstallsTwoProgressWordsAreTheOnesAppspec06Writes(t *testing.T) {
	// appspec/06 step 2 gives this one command two different words: "Linking
	// <f> ..." in the short form and "Backing up\n ... " in the verbose one.
	// The form is data for that reason, and this pins the surprising half --
	// the verbose word is BACKUP's, which reads like a copy-paste slip and is
	// what the specification writes.
	//
	// The comparison against backupDirection.verb is the point: spelling the
	// literal twice would let one of them be "corrected" without the other
	// noticing, and the claim is that they are the same word. Compared through
	// copyProgress rather than against a hand-written template, so that the
	// second claim is the one that matters -- link install prints the SAME
	// four-line verbose shape the copy commands do, which is what `link`, one
	// door along, does not.
	if linkInstallProgress.short != "Linking" {
		t.Errorf("link install's short progress word is %q, want %q", linkInstallProgress.short, "Linking")
	}
	if want := copyProgress("Linking", backupDirection.verb); linkInstallProgress != want {
		t.Errorf("link install's progress form is %+v, want backup's word in the copy shape: %+v", linkInstallProgress, want)
	}
	if strings.HasPrefix(linkInstallProgress.long, linkInstallProgress.short) {
		t.Errorf("link install's verbose form opens with %q, the short word; appspec/06 gives it different words in the two forms", linkInstallProgress.short)
	}
}

func TestOnlyLinuxAppliesTheLibraryRuleAndOnlyToPathsUnderIt(t *testing.T) {
	// appspec/06 step 1's third condition for `link`, and appspec/00 "Platform
	// assumptions" item 2: "on Linux, a file whose home path is under
	// ~/Library/ is not synced (skipped); on macOS there is no such
	// restriction."
	//
	// Both platforms are asked, which is the whole reason the rule is a pure
	// function of goos: the conformance suite can only ever observe the machine
	// it runs on, and a rule whose content is "these two platforms differ" is
	// half untested there. The darwin rows are not padding -- a program that
	// skipped Library paths everywhere would satisfy every Linux row.
	//
	// The two near-misses are the prefix's edges. "Library" alone is the
	// directory itself rather than something under it, which is how appspec/06
	// words the rule; "Librarian.cfg" is a path a separator-less prefix test
	// would have skipped on Linux for no reason at all.
	for _, test := range []struct {
		goos     string
		relative string
		want     bool
	}{
		{"linux", "Library/Preferences/com.apple.Terminal.plist", false},
		{"linux", "Library/Fonts", false},
		{"linux", ".vimrc", true},
		{"linux", "Library", true},
		{"linux", "Librarian.cfg", true},
		{"linux", ".config/Library/thing", true},
		{"darwin", "Library/Preferences/com.apple.Terminal.plist", true},
		{"darwin", "Library", true},
		{"darwin", ".vimrc", true},
	} {
		if got := linkableOnPlatform(test.goos, test.relative); got != test.want {
			t.Errorf("linkableOnPlatform(%s, %s) = %v, want %v", test.goos, test.relative, got, test.want)
		}
	}
}

func TestAFailureInsideALinkOperationIsUnguardedAndNamesThePath(t *testing.T) {
	// appspec/07's error table: "Failure inside a link operation | stderr |
	// nonzero | uncaught error, run stops mid-way | UNGUARDED". The regime is
	// the assertion a black-box case cannot make cleanly -- both regimes exit 1
	// and both write one line to stderr -- and internal/fault exists precisely
	// so the split stays observable.
	//
	// The path is the second half, because appspec/02 requires an unguarded
	// diagnostic to "name the offending value". A message that said only that
	// linking failed would leave a user mid-migration with no way to tell which
	// half of appspec/01 section 2's non-atomic window they are in.
	err := linkFailure(os.ErrPermission, "copy %s to %s", "/home/u/.vimrc", "/store/Mackup/.vimrc")

	regime, declared := fault.RegimeOf(err)
	if !declared {
		t.Fatalf("linkFailure produced an error carrying no regime: the guarded/unguarded split is contract as observed")
	}
	if regime != fault.Unguarded {
		t.Errorf("linkFailure is %s, want unguarded", regime)
	}

	diagnostic := fault.Diagnostic(err)
	for _, want := range []string{"mackup: ", "/home/u/.vimrc", "/store/Mackup/.vimrc", os.ErrPermission.Error()} {
		if !strings.Contains(diagnostic, want) {
			t.Errorf("linkFailure diagnostic = %q, want it to contain %q", diagnostic, want)
		}
	}
	if strings.HasPrefix(diagnostic, "Error: ") {
		t.Errorf("linkFailure diagnostic = %q, want the unguarded prefix rather than the guarded one", diagnostic)
	}
}
