package syncfs

import (
	"io/fs"
	"os"
)

// The per-file state model of appspec/01-architecture.md section 2, and the
// single predicate at its heart. appspec/06's "Shared vocabulary" names five
// states and says to "model it once as a type rather than re-deriving it in
// each operation"; appspec/01 says of the predicate that "a reimplementer who
// codes this check four times risks four subtly different answers; there must
// be one definition". Both are the same instruction about the same thing, so
// both live here and nothing else in the program asks a filesystem whether a
// home path is linked.

// A LinkState is the derived per-file state every sync operation branches on
// (appspec/06 "The LinkState branch variable").
//
// It is derived from the pair of paths, never stored: appspec/07 says the next
// run "has no crash-recovery logic; it simply re-evaluates each file's
// LinkState with the same rules", which is what makes re-running the recovery
// mechanism. A cached state would be a second source of truth for a question
// the filesystem already answers.
type LinkState int

const (
	// StateAbsent means nothing is at the home path and nothing is at the
	// mackup path.
	StateAbsent LinkState = iota
	// StateMackupOnly means the mackup path exists and the home path does
	// not. appspec/01 calls it Storage-only: it occurs transiently mid-`link
	// install`, or on a machine that synced storage but never restored.
	StateMackupOnly
	// StateRealFilePresent means the home path exists as a real file or
	// directory -- or as a live symlink that does NOT resolve to the mackup
	// copy, which appspec/01 groups with it as Foreign / conflict. What the
	// name asserts is that something is there and it is not this program's
	// link.
	StateRealFilePresent
	// StateBrokenLink means the home path is a symlink that does not resolve.
	StateBrokenLink
	// StateAlreadyLinked means AlreadyLinked reports true: the home path is a
	// live symlink resolving to an existing mackup copy.
	StateAlreadyLinked
)

// String names a state as appspec/06 "The LinkState branch variable" writes it.
//
// The spec's own hyphenated vocabulary rather than a phrase of this program's
// own, so that a diagnostic or a test failure can be read straight against the
// list in that section. It is not the wording of any user-facing trace:
// appspec/06 gives `link install` its own "Doing nothing ..." phrasings, which
// are that command's to write.
func (s LinkState) String() string {
	switch s {
	case StateMackupOnly:
		return "mackup-only"
	case StateRealFilePresent:
		return "real-file-present"
	case StateBrokenLink:
		return "broken-link"
	case StateAlreadyLinked:
		return "already-linked"
	default:
		return "absent"
	}
}

// AlreadyLinked reports whether the home path is a live symlink to its mackup
// copy -- the one predicate of appspec/01 section 2 and appspec/06 "The
// already-linked predicate", used identically by backup as a skip, by link
// install as a guard, by link as a guard, and by link uninstall as the safety
// check.
//
// True only when all four of appspec/06's conditions hold: the home path is a
// symlink, it is not dangling, the mackup path exists, and the two resolve to
// the same file. Each of the three false arms below is one of the conditions,
// in that order.
//
// Two of those arms are, strictly, already implied by the fourth: os.SameFile
// is false when handed the nil FileInfo a failed stat leaves, so deleting
// either existence check changes no answer, and injection confirms it -- no
// case in this package can tell the difference. They are written out because
// this is the definition four operations are promised to share, and a reader
// checking it against appspec/06's four conditions must find four of them
// rather than two and an argument. What is genuinely load-bearing is the
// os.SameFile itself, and a "simplification" that kept the two stats and
// replaced it with a path comparison would break the case below about a storage
// root reached through a symlink.
//
// It returns a bool and no error, which is the contract rather than a
// simplification: "a dangling home symlink, or a missing mackup copy, reads as
// false -- never an error". Returning (bool, error) would let a caller treat
// the ordinary state of a half-synced machine as a failure, and four callers
// would each decide differently what to do with it.
//
// The last condition is asked with os.SameFile rather than by comparing
// resolved path strings, because appspec/06 words it as "the two resolve to
// the same file" and identity is what that means. It also survives the
// arrangement this program has to expect: a storage root reached through a
// symlink (~/Dropbox pointing at a volume) gives the home link a target spelled
// differently from the mackup path the caller computed, and the two are still
// the same file.
func AlreadyLinked(homePath, mackupPath string) bool {
	link, err := os.Lstat(homePath)
	if err != nil || link.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	home, err := os.Stat(homePath)
	if err != nil {
		return false
	}
	mackup, err := os.Stat(mackupPath)
	if err != nil {
		return false
	}
	return os.SameFile(home, mackup)
}

// StateOf derives the LinkState of one file from its two absolute paths.
//
// The paths are the ones appspec/06 "Shared vocabulary" defines -- $HOME/f and
// <Mackup folder>/f -- and deriving them is the caller's, because the Mackup
// folder is a fact the configuration resolved (appspec/03, appspec/04) and this
// package does not read configuration.
//
// AlreadyLinked is asked first and its answer is taken whole, so that the state
// machine cannot disagree with the predicate the four operations consult
// directly. The remaining arms are then about a home path that is NOT this
// program's link.
//
// A home path this process cannot stat -- an unreadable parent directory, not
// merely an absent file -- reads as absent, and the mackup side decides between
// absent and mackup-only. That matches the reference, whose os.path.exists is
// false for both, and it is the safe direction of the two: the commands treat
// absent as "nothing to do here" (appspec/06's step 1 skips silently), where a
// wrong report of a real file present would send `link install` on to copy and
// delete it.
func StateOf(homePath, mackupPath string) LinkState {
	if AlreadyLinked(homePath, mackupPath) {
		return StateAlreadyLinked
	}
	home, err := os.Lstat(homePath)
	if err != nil {
		if _, err := os.Stat(mackupPath); err == nil {
			return StateMackupOnly
		}
		return StateAbsent
	}
	if home.Mode()&fs.ModeSymlink != 0 {
		if _, err := os.Stat(homePath); err != nil {
			return StateBrokenLink
		}
	}
	return StateRealFilePresent
}
