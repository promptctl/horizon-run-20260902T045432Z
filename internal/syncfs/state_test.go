package syncfs

import (
	"os"
	"path/filepath"
	"testing"
)

// arrangement builds one home/mackup pair for a case: the two absolute paths
// appspec/06 "Shared vocabulary" defines, under a throwaway root.
//
// The two live in separate directories, as they do in the program -- $HOME/f
// and <Mackup folder>/f -- so that a predicate accidentally comparing basenames
// or relative paths cannot pass.
func arrangement(t *testing.T) (homePath, mackupPath string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	mackup := filepath.Join(root, "storage", "Mackup")
	for _, dir := range []string{home, mackup} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("preparing %s: %v", dir, err)
		}
	}
	return filepath.Join(home, ".config-file"), filepath.Join(mackup, ".config-file")
}

// TestTheAlreadyLinkedPredicateIsTrueOnlyForALiveLinkToTheMackupCopy walks the
// four conditions of appspec/06 "The already-linked predicate" -- symlink, not
// dangling, mackup copy exists, both resolve to the same file -- by arranging a
// state that fails exactly one of them at a time.
//
// One table rather than four cases, because the claim being pinned is that the
// answer is a conjunction: a version that dropped any single condition would
// pass every case but one, and the case it fails names the condition.
func TestTheAlreadyLinkedPredicateIsTrueOnlyForALiveLinkToTheMackupCopy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, homePath, mackupPath string)
		want  bool
	}{
		{
			name: "a live link to the mackup copy",
			build: func(t *testing.T, homePath, mackupPath string) {
				writeFile(t, mackupPath, 0o600, "content")
				symlink(t, mackupPath, homePath)
			},
			want: true,
		},
		{
			name: "a real file at home, with a mackup copy beside it",
			build: func(t *testing.T, homePath, mackupPath string) {
				writeFile(t, mackupPath, 0o600, "content")
				writeFile(t, homePath, 0o600, "content")
			},
			want: false,
		},
		{
			name: "a link to the mackup path, which does not exist",
			build: func(t *testing.T, homePath, mackupPath string) {
				symlink(t, mackupPath, homePath)
			},
			want: false,
		},
		{
			name: "a live link to somewhere else, with the mackup copy present",
			build: func(t *testing.T, homePath, mackupPath string) {
				writeFile(t, mackupPath, 0o600, "content")
				elsewhere := filepath.Join(filepath.Dir(homePath), "other")
				writeFile(t, elsewhere, 0o600, "content")
				symlink(t, elsewhere, homePath)
			},
			want: false,
		},
		{
			name: "a live link to somewhere else, and no mackup copy",
			build: func(t *testing.T, homePath, mackupPath string) {
				elsewhere := filepath.Join(filepath.Dir(homePath), "other")
				writeFile(t, elsewhere, 0o600, "content")
				symlink(t, elsewhere, homePath)
			},
			want: false,
		},
		{
			name:  "nothing at either path",
			build: func(t *testing.T, homePath, mackupPath string) {},
			want:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			homePath, mackupPath := arrangement(t)
			tc.build(t, homePath, mackupPath)
			if got := AlreadyLinked(homePath, mackupPath); got != tc.want {
				t.Fatalf("AlreadyLinked = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestADanglingHomeSymlinkReadsAsNotLinkedRatherThanRaising pins the clause
// appspec/01 section 2 calls out on its own: "a dangling home symlink, or a
// missing storage copy, reads as false -- NEVER an error".
//
// It is its own case despite the table above covering the same two states,
// because the claim here is about the SHAPE of the answer, not its value: the
// predicate has to have no error to return. A signature change to (bool, error)
// -- the obvious thing to reach for when a stat fails -- would not fail a table
// asserting values, and would hand four callers a decision the specification
// makes for them.
func TestADanglingHomeSymlinkReadsAsNotLinkedRatherThanRaising(t *testing.T) {
	homePath, mackupPath := arrangement(t)
	symlink(t, mackupPath, homePath)

	// The compiler is the assertion about the shape: a single bool, usable in a
	// condition, with nothing else to handle.
	var linked bool = AlreadyLinked(homePath, mackupPath)
	if linked {
		t.Fatalf("a home link pointing at an absent mackup copy read as already-linked")
	}
	if StateOf(homePath, mackupPath) != StateBrokenLink {
		t.Fatalf("StateOf = %v, want %v", StateOf(homePath, mackupPath), StateBrokenLink)
	}
}

// TestTheLinkedAnswerSurvivesAStorageRootReachedThroughASymlink pins the reason
// the last condition is asked with os.SameFile and not by comparing resolved
// path strings against the mackup path the caller computed.
//
// ~/Dropbox pointing at another volume is an ordinary arrangement, and it makes
// the home link's target -- written by an earlier run, or by another tool --
// spell the same file differently from the path appspec/06 derives. A predicate
// that compared the link's target text would call this pair unlinked and send
// `link install` on to copy, delete and relink a file that was already correct.
func TestTheLinkedAnswerSurvivesAStorageRootReachedThroughASymlink(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	volume := filepath.Join(root, "volume", "Mackup")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("preparing the home directory: %v", err)
	}
	if err := os.MkdirAll(volume, 0o700); err != nil {
		t.Fatalf("preparing the volume: %v", err)
	}
	// The storage root the configuration would resolve is a symlink to the
	// volume, which is what makes the two spellings differ.
	storage := filepath.Join(root, "Dropbox")
	symlink(t, filepath.Join(root, "volume"), storage)

	homePath := filepath.Join(home, ".config-file")
	viaVolume := filepath.Join(volume, ".config-file")
	viaStorage := filepath.Join(storage, "Mackup", ".config-file")
	writeFile(t, viaVolume, 0o600, "content")
	symlink(t, viaVolume, homePath)

	if !AlreadyLinked(homePath, viaStorage) {
		t.Fatalf("a link written as %q was not recognised as pointing at %q", viaVolume, viaStorage)
	}
}

// TestEveryArrangementDerivesTheStateAppspec06NamesForIt walks the five states
// of appspec/06 "The LinkState branch variable".
//
// Every arm is exercised, including the two that differ only in what is at the
// mackup path: absent and mackup-only are one home condition and two answers,
// and a derivation that forgot to look at the mackup side at all would report
// absent for both.
func TestEveryArrangementDerivesTheStateAppspec06NamesForIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, homePath, mackupPath string)
		want  LinkState
	}{
		{
			name:  "nothing at either path",
			build: func(t *testing.T, homePath, mackupPath string) {},
			want:  StateAbsent,
		},
		{
			name: "the mackup copy alone",
			build: func(t *testing.T, homePath, mackupPath string) {
				writeFile(t, mackupPath, 0o600, "content")
			},
			want: StateMackupOnly,
		},
		{
			name: "a real file at home",
			build: func(t *testing.T, homePath, mackupPath string) {
				writeFile(t, homePath, 0o600, "content")
			},
			want: StateRealFilePresent,
		},
		{
			name: "a real directory at home",
			build: func(t *testing.T, homePath, mackupPath string) {
				mustMkdir(t, homePath, 0o700)
			},
			want: StateRealFilePresent,
		},
		{
			name: "a live link at home pointing somewhere other than mackup",
			build: func(t *testing.T, homePath, mackupPath string) {
				writeFile(t, mackupPath, 0o600, "content")
				elsewhere := filepath.Join(filepath.Dir(homePath), "other")
				writeFile(t, elsewhere, 0o600, "content")
				symlink(t, elsewhere, homePath)
			},
			want: StateRealFilePresent,
		},
		{
			name: "a dangling link at home",
			build: func(t *testing.T, homePath, mackupPath string) {
				symlink(t, filepath.Join(filepath.Dir(homePath), "gone"), homePath)
			},
			want: StateBrokenLink,
		},
		{
			name: "a live link at home to the mackup copy",
			build: func(t *testing.T, homePath, mackupPath string) {
				writeFile(t, mackupPath, 0o600, "content")
				symlink(t, mackupPath, homePath)
			},
			want: StateAlreadyLinked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			homePath, mackupPath := arrangement(t)
			tc.build(t, homePath, mackupPath)
			if got := StateOf(homePath, mackupPath); got != tc.want {
				t.Fatalf("StateOf = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEveryStateNamesItselfAsAppspec06WritesIt pins the String method against
// the vocabulary of appspec/06, so that a state added later without a name does
// not silently render as the default.
func TestEveryStateNamesItselfAsAppspec06WritesIt(t *testing.T) {
	want := map[LinkState]string{
		StateAbsent:          "absent",
		StateMackupOnly:      "mackup-only",
		StateRealFilePresent: "real-file-present",
		StateBrokenLink:      "broken-link",
		StateAlreadyLinked:   "already-linked",
	}
	for state, name := range want {
		if got := state.String(); got != name {
			t.Errorf("LinkState(%d).String() = %q, want %q", int(state), got, name)
		}
	}
	// And no two states share a name. String is what a diagnostic and a test
	// failure report a state as, so two arms rendering alike would make both
	// unreadable while every assertion above still passed.
	seen := make(map[string]LinkState, len(want))
	for state, name := range want {
		if other, taken := seen[name]; taken {
			t.Errorf("states %d and %d both render as %q", int(other), int(state), name)
		}
		seen[name] = state
	}
}
