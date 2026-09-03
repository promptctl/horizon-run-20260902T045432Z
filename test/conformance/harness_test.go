//go:build conformance

// This package imports nothing from cmd/ or internal/ -- it shells out to
// `go build` -- so nothing about the program under test naturally reaches the
// Go test cache, and a cached "ok" would outlive a program that has since been
// broken. Three separate things close that, because no one of them closes all
// of it:
//
//   - readImplementationSources, below, makes the cache honest: cmd/go records
//     the files a test binary opens and folds them into the cache key, so an
//     edit to the program invalidates the cached result. This is the part
//     that holds under any invocation that runs a case which observes the
//     program -- any tags, any tool, no flags of ours required. The
//     qualifier is load-bearing: NewWorld is this walk's only caller, so a
//     -run filter selecting only cases that build no world leaves it
//     unexecuted. Which cases those are, and why none of them is a stale-pass
//     hazard, is declared and enforced in casesThatBuildNoWorld rather than
//     described here, because described here it went stale twice. It is
//     stat-based, not content-based, and the limit that follows from that is
//     spelled out on readImplementationSources.
//   - The `conformance` build tag keeps the package out of untagged builds, so
//     a plain `go test ./...` does not report on it at all rather than
//     reporting something stale.
//   - `make conformance` passes -count=1, which needs none of the above to be
//     right.
//
// Run it with `make conformance`, or
// `go test -count=1 -tags conformance ./test/conformance/`.

// Package conformance observes the built command the way appspec/00-overview.md
// says the specification itself was written: by running the real program under
// a throwaway home directory and watching its boundary -- stdout, stderr, the
// exit code, and the filesystem it leaves behind.
//
// Nothing here reaches inside the program. A case that can only be checked by
// calling an internal function belongs in that package's own tests; this suite
// exists so every ticket's done-claim can be checked where the spec makes its
// promises.
package conformance

import (
	"bytes"
	"debug/buildinfo"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// contentsUnreadable is appended to the record of a directory this process
// could not list. It is a marker rather than prose: ExpectUnchanged looks for
// it and refuses to compare two records that are equal only because both are
// blind. See there for what that does and does not cover.
const contentsUnreadable = " <contents unreadable>"

// entryUnstatable is the whole record of an entry this process could list but
// could not stat: its mode, type and modification time are all unknown, so
// nothing about it can be compared. ExpectUnchanged reports it for the same
// reason it reports contentsUnreadable.
const entryUnstatable = "<unstatable>"

// stampedVersion is the version the stamped binary is built with. It is
// deliberately not a plausible release number: a test asserting on it should
// be obviously reading the build's stamp, not a hardcoded product version.
const stampedVersion = "0.0.0-conformance"

// usageMarker is how THIS implementation opens its usage block. appspec/02
// says the exact wording of usage, help, and parser-warning text is
// human-facing and not a machine-read contract, so this token is not spec: it
// is the one place the suite is allowed to know it, so that rewording the help
// text is a one-line change here rather than an edit across every case. A case
// that means "the parser rejected this" says so with usageMarker; a case that
// means "the program did the thing" must assert the thing, never this.
const usageMarker = "Usage:"

// The binaries under test. appspec/00-overview.md "Provenance" makes the
// version contract observable in three shapes, and each needs its own build:
//
//   - mackupBin is the artifact a user actually gets: exactly what `make build`
//     produces, built through the Makefile rather than beside it.
//   - mackupVCSBin forces VCS stamping on, so the harder half of the
//     provenance contract is exercised on every machine and not only where the
//     toolchain happens to stamp. Empty when the toolchain cannot stamp at all.
//   - mackupStampedBin is a release build, `make build VERSION=...`. Built
//     through the Makefile on purpose: the -X symbol path is spelled out there,
//     the linker ignores a stale one in silence, and nothing else in the repo
//     would notice a release binary that had quietly stopped carrying its
//     version.
var (
	mackupBin        string
	mackupVCSBin     string
	mackupStampedBin string

	// mackupStampedVCSBin is a release build that ALSO carries a VCS stamp,
	// so the precedence half of the provenance rule can be observed at the
	// boundary. Empty when the toolchain cannot stamp.
	mackupStampedVCSBin string

	// mackupVCSBuildErr says why mackupVCSBin is empty, when it is.
	mackupVCSBuildErr error

	// mackupStampedVCSBuildErr says why mackupStampedVCSBin is empty.
	mackupStampedVCSBuildErr error
)

func TestMain(m *testing.M) {
	// This run gets a directory of its own, and reaps what earlier runs
	// abandoned. Both halves are needed:
	//
	// Its own, because a shared name is not this run's to delete. Two
	// checkouts or worktrees running the suite at once would replace each
	// other's binaries mid-suite -- either failing to exec, or worse, quietly
	// testing the other checkout's program and reporting a result for code it
	// never built -- and under a sticky /tmp a directory another user created
	// at 0700 cannot be removed at all, so the suite would abort before a
	// single case ran.
	//
	// And reaping, because a crashed run cannot clean up after itself: os.Exit
	// skips deferred functions, and a panicking test never returns from
	// m.Run() at all, since the testing package re-panics on the test's own
	// goroutine where nothing here can recover. Without a reaper each crash
	// abandons several megabytes for good.
	reapAbandonedBuildDirectories(os.TempDir())
	dir, err := os.MkdirTemp("", buildDirPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conformance: %v\n", err)
		os.Exit(1)
	}
	keepBuildDirFresh(dir)

	// The removal below is spelled out at each exit rather than deferred,
	// since os.Exit skips defers.
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "conformance: %v\n", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	mackupBin = filepath.Join(dir, "mackup")
	mackupStampedBin = filepath.Join(dir, "mackup-stamped")
	if err := buildWithMake(mackupBin, ""); err != nil {
		fail(err)
	}
	if err := buildWithMake(mackupStampedBin, stampedVersion); err != nil {
		fail(err)
	}

	// -buildvcs defaults to auto and declines, without saying so, when it
	// cannot read the repository. A build it declined to stamp reports the
	// fallback token for the trivial reason, so the case asserting the token
	// cannot fail -- this suite passed on a developer machine whose builds went
	// unstamped while CI, whose checkout was stamped, failed on exactly that
	// assertion. Forcing the stamp gives the hard half a binary of its own.
	//
	// Where the toolchain will not stamp -- a source tarball with no
	// repository, where -buildvcs=true builds clean and stamps nothing rather
	// than failing -- this build is simply unavailable, because
	// buildForcingVCSStamp checks the artifact instead of trusting the exit
	// status. The suite does not silently fall back to an unstamped binary,
	// which would restore the vacuous pass under a different name:
	// requireVCSStampedBuild reports the degradation, skipping locally and
	// failing under CI.
	// The release build's mirror of the same problem, and it went unnoticed
	// far longer. mackupStampedBin is built with plain `make build`, so it
	// inherits -buildvcs=auto -- and where the toolchain declines to stamp
	// (GOFLAGS=-buildvcs=false, which is set on developer machines; a source
	// tarball; a Docker COPY without .git) that release binary carries no
	// vcs.revision at all. The rule that the linker stamp OUTRANKS a
	// working-tree provenance then has no conflict to resolve, and the case
	// asserting it passes for the trivial reason. Verified: inverting the
	// precedence in internal/version, so every release build reports the
	// fallback token, left this suite entirely green and was caught only by
	// the unit test. This build is the release binary with the stamp forced
	// on, which is the only shape where the two sources disagree.
	stampedVCSBin := filepath.Join(dir, "mackup-stamped-vcs")
	if err := buildStampedForcingVCSStamp(stampedVCSBin, stampedVersion); err == nil {
		mackupStampedVCSBin = stampedVCSBin
	} else {
		mackupStampedVCSBuildErr = err
	}

	vcsBin := filepath.Join(dir, "mackup-vcs")
	if err := buildForcingVCSStamp(vcsBin); err == nil {
		mackupVCSBin = vcsBin
	} else {
		// Kept, not discarded: "this toolchain cannot stamp" is only one of
		// the reasons this build fails. git absent, git refusing a repository
		// it considers dubiously owned, a GOFLAGS setting that breaks this
		// invocation alone -- each would otherwise be reported as the wrong
		// cause, with nothing to diagnose from.
		mackupVCSBuildErr = err
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// readSourcesOnce guards readImplementationSources, which every case calls and
// only the first needs to do.
var readSourcesOnce sync.Once

// mainSourceAnchor and internalPrefix name the program's own source, which the
// walk must reach for the cache key to track it. Naming them means a rename
// that moves the program stops the suite loudly, on its next run, rather than
// leaving it passing over code it no longer covers.
var (
	mainSourceAnchor = filepath.Join("cmd", "mackup", "main.go")
	internalPrefix   = "internal" + string(filepath.Separator)
)

// readImplementationSources reads every file the build could read, for its
// side effect on the Go test cache.
//
// cmd/go records the files a test binary opens and folds them into the cache
// key, so reading the implementation here is what ties a cached result to the
// code it was a result about. Without it `go test -tags conformance
// ./test/conformance/` reports "ok (cached)" over a program broken since.
//
// It must run while a case is running. The testing package opens the log
// cmd/go reads inside m.Run(), so anything TestMain does before that call --
// and every package-level initializer, which runs earlier still -- is not
// recorded at all. Calling this from TestMain, the obvious place, produced a
// suite that read all of cmd/ and internal/ on every run and still served a
// cached pass over a mutated program: the reads happened, and nothing was
// listening. Hence sync.Once from NewWorld, which every case goes through.
//
// The residual gap is a run that never reaches NewWorld, which is this walk's
// only caller. A -run filter selecting no case is the obvious shape, and it
// asserts nothing either, so there is no pass to go stale. The less obvious
// shape is a filter selecting only cases that build no world, which leaves
// this walk unexecuted -- verified, against the earlier claim here that a
// no-case filter was the whole of it. What WOULD be a stale-pass hazard is a
// case that observes the program without going through NewWorld. There is
// none; do not write one.
//
// That last sentence is no longer left to a comment to be right about. This
// paragraph used to name the two such cases that existed and say why neither
// observed the program; five more were added in the rounds after, the sentence
// went on naming two, and it had already been corrected once before that. The
// set is now declared and enforced in casesThatBuildNoWorld and
// TestEveryCaseThatBuildsNoWorldIsAccountedFor, one reason per entry, failing
// both on an unlisted case and on a listed one that has gone away. Read the
// list, not this paragraph, for which cases those are -- and note that one of
// them does read cmd/ and internal/, so the blanket claim made here before
// ("neither observes the program") was not even true of the set it stood for.
//
// Moving this call back out of a case is the failure that matters, and
// the flag.Parsed check below is a tripwire for exactly that: flags are still
// unparsed before m.Run, so a caller that runs too early to be recorded
// panics instead of quietly buying back the stale pass.
//
// What cmd/go folds in is the stat, not the bytes: hashOpen hashes size, mode
// and modification time and says so in its own comment, "do not attempt to
// hash the entirety of their content". So the guarantee is an edit that moves
// a file's size or mtime, which every editor, compiler and checkout does, and
// not literally any edit -- rewriting a file in place with the same length and
// then restoring its mtime does serve a cached pass over a changed program.
// Verified, by doing exactly that. -count=1 is what covers it completely, and
// is why `make conformance` passes it rather than relying on this.
//
// It reads the whole tree rather than a list of source directories. Reading
// too little costs a stale pass; reading too much costs time and a cache miss
// on an unrelated edit, so the walk errs deliberately in the cheaper
// direction. "Cheaper" is not "free": it scales with the working tree, since
// every file read is a file opened and stat-hashed. If this tree ever grows a
// vendored or generated directory, skip that directory -- but never narrow
// the walk back to a guess at where the program lives.
// The earlier version read *.go under a hardcoded cmd/ and internal/, which
// misses whatever the next tickets add -- appspec/05's application database is
// a set of .cfg files, not Go source -- and would have gone on reading nothing
// at all if either directory were renamed.
//
// Errors on an individual file are ignored: this is a cache-key contribution,
// not a check, and a source file that cannot be read will fail the build a
// moment later and say so properly. Reading *nothing* is different, and
// panics: that is the silent-degradation shape this mechanism keeps failing
// in, and it looks exactly like success.
func readImplementationSources() {
	// flag.Parse runs at the top of m.Run and the testlog opens just after, so
	// unparsed flags mean this is running somewhere its reads go nowhere.
	// Panicking is the point: this defect is invisible -- the suite passes,
	// stale -- and it has already been shipped twice.
	if !flag.Parsed() {
		panic("conformance: readImplementationSources ran before m.Run, where cmd/go does not record its reads; call it from a case (see NewWorld), not from TestMain")
	}
	root, err := moduleRoot()
	if err != nil {
		panic("conformance: " + err.Error() + "; with no module root nothing is read and the cache stops tracking the program")
	}
	read := map[string]bool{}
	filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			// .git is history at whatever depth it appears -- a submodule has
			// one too. bin is skipped only at the module root, because that
			// is the one the Makefile writes; an internal/bin/ holding real
			// source would otherwise drop out of the cache key on its name
			// alone, which is the same silent degradation as the hardcoded
			// cmd/ and internal/ list this walk replaced.
			// path != root on the name test, for the reason the doc guard's
			// walk carries the same exemption: a checkout in a directory
			// literally named ".git" pruned the root on the first callback,
			// this walk read nothing, and the "read 0 files" panic below took
			// down the whole suite rather than failing one case. Reproduced by
			// unpacking this tree into a directory named .git. The bin test
			// needs no exemption -- it is already anchored to a path under the
			// root, so it cannot match the root itself.
			if (path != root && entry.Name() == ".git") || path == filepath.Join(root, "bin") {
				return fs.SkipDir
			}
			// The two prefixes the toolchain excludes outright, which .git is
			// one instance of. A `git worktree add .worktrees/x` puts another
			// branch's entire checkout under the module root, and .direnv,
			// .gopath and .tools hold third-party trees; opening those
			// file-by-file folds them into this suite's test cache key, so an
			// unrelated worktree or tool cache moving invalidates a
			// conformance result that does not depend on it, and the walk's
			// cost grows with trees that are not the program.
			//
			// This is not the narrowing the paragraph above refuses. That
			// refusal is about guessing where the program lives; these are
			// directories the compiler has already ruled out of the module, so
			// nothing the cache key is about can be inside one.
			if path != root && (strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_")) {
				return fs.SkipDir
			}
			return nil
		}
		// Never opened, for the same reason Snapshot does not open them: a
		// FIFO with no writer blocks until the run is killed. Nothing here is
		// worth hanging the suite over.
		//
		// This skips symlinks too, which reads like a hole in the cache key
		// and is not one: entry.Info() lstats, and that stat is itself
		// recorded, and cmd/go's hashStat runs both os.Stat and os.Lstat over
		// a recorded name -- so the target's size and mtime land in the key
		// through the link. Since hashOpen hashes the stat rather than the
		// bytes, a recorded stat and a recorded open say the same thing about
		// a file. Verified by linking a source into internal/ and editing the
		// target: the cached pass was correctly refused.
		if info, err := entry.Info(); err != nil || !info.Mode().IsRegular() {
			return nil
		}
		// Opened and closed rather than read: what reaches the cache key is
		// the open, which cmd/go records by name, and hashOpen then folds in
		// size, mode and mtime -- explicitly not the content. Reading the
		// bytes buys nothing and costs the whole tree, which this walk covers
		// on purpose: one large untracked file under the module root, a dump
		// or a testdata blob a later ticket adds, would be read into memory on
		// every run. Verified equivalent on the testlog: both spellings record
		// the same "open" line for every file.
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		f.Close()
		if relative, err := filepath.Rel(root, path); err == nil {
			read[relative] = true
		}
		return nil
	})

	// Anchors rather than a file count, so a failure says what is missing
	// rather than that a number came out low.
	//
	// Every anchor is inside the program on purpose. The first version of this
	// check asked for "any .go file", which this package's own sources satisfy
	// -- they sit under the module root and are read on every run by
	// construction -- so it was true whenever the walk ran at all and could
	// never report the thing it was written to report: the implementation
	// dropping out of the walk. An anchor that cannot fail is not a check.
	missing := []string{}
	for _, anchor := range []string{"go.mod", "Makefile", mainSourceAnchor} {
		if !read[anchor] {
			missing = append(missing, anchor)
		}
	}
	sawInternal := false
	for relative := range read {
		if strings.HasPrefix(relative, internalPrefix) && strings.HasSuffix(relative, ".go") {
			sawInternal = true
			break
		}
	}
	if !sawInternal {
		missing = append(missing, "any "+internalPrefix+"*.go")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf("conformance: the walk of %s read %d files but not %s, so the cache key does not track the program and a cached pass would outlive a broken one", root, len(read), strings.Join(missing, " or ")))
	}
}

// snapshotPaths lists what a snapshot holds, in a stable order, for a failure
// message that is worth reading.
//
// Here rather than in harness_unix_test.go, where it was written. That file is
// behind `conformance && unix` because it makes a FIFO; this helper has
// nothing unix about it, and a case in the untagged argv_test.go calling it
// stopped the package compiling on any other GOOS -- `GOOS=windows go vet
// -tags conformance ./test/conformance/` reported it undefined, which is what
// a Windows contributor running this repo's own `make check` would have seen.
// A build constraint that is load-bearing for one file is not a place to keep
// shared helpers.
func snapshotPaths(snapshot Snapshot) []string {
	paths := make([]string, 0, len(snapshot))
	for path := range snapshot {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// buildDirPrefix names this suite's build directories, so that a later run can
// recognize one an earlier run abandoned.
const buildDirPrefix = "macklebox-conformance-bin-"

// buildDirAbandonedAfter is how long a build directory must have gone untouched
// before a later run treats it as abandoned.
const buildDirAbandonedAfter = time.Hour

// buildDirTouchInterval is how often a running suite refreshes its own build
// directory's modification time. Well inside buildDirAbandonedAfter, so a
// directory in use is never old enough to be reaped.
//
// Without the refresh the mtime is set once, when the binaries are built, and
// then never moves -- so "untouched for an hour" means "started over an hour
// ago", not "idle", and a suite that simply ran that long would have its
// binaries deleted out from under it by the next run to start. That is the
// mid-suite hazard the per-run directory exists to prevent.
//
// What this does NOT cover, since it is worth being exact about: a process
// that is stopped rather than slow. A debugger breakpoint or a SIGSTOP halts
// this goroutine along with every other, so an hour spent paused leaves the
// mtime as stale as no refresh at all. Closing that needs liveness rather than
// a timestamp -- a lock held for the run, released by the kernel on exit --
// which is more machinery than a disk-reclaiming reaper has earned.
const buildDirTouchInterval = 5 * time.Minute

// refreshingBuildDir holds the directory the running suite is refreshing, or
// nothing if it never started one.
//
// It exists so a case can assert the wiring, which nothing did.
// TestARefreshedBuildDirectoryOutlivesTheReaper pins touchBuildDir by calling
// it, and refreshBuildDir is pinned by driving it -- but the call in TestMain
// that connects the two to the real build directory was covered by neither:
// replacing `keepBuildDirFresh(dir)` with `_ = keepBuildDirFresh` left the
// whole gate green. Two mechanisms for one contract and only one of them
// falsifiable is the shape this branch keeps finding.
//
// The directory and not a bool, so that starting the refresher on the wrong
// directory fails too, which a bool would call success.
var refreshingBuildDir atomic.Value

// keepBuildDirFresh starts refreshing dir's modification time, and records dir
// so a case can check that this ran at all.
//
// The refresher is never stopped: the goroutine costs one timer, and the
// process it belongs to is a test binary that exits from TestMain.
func keepBuildDirFresh(dir string) {
	refreshingBuildDir.Store(dir)
	go refreshBuildDir(dir, buildDirTouchInterval, nil)
}

// refreshBuildDir touches dir every interval until stop is closed.
//
// Split out of keepBuildDirFresh, and taking its interval, for the reason
// touchBuildDir was split out of this loop: a loop that sleeps for minutes
// cannot be driven from a case, so the fact that it touches REPEATEDLY -- the
// thing that separates a refresher from a single Chtimes at build time -- had
// nothing pinning it. TestTheBuildDirectoryRefresherKeepsTouching drives it at
// a millisecond and watches the modification time move twice.
//
// A nil stop channel never becomes ready, which is what the suite's own
// refresher passes: a receive on a nil channel blocks forever, so the select
// waits on the timer alone. A case passes a real channel and closes it, so the
// goroutine does not outlive the case that started it and go on touching a
// directory the testing package has already removed.
func refreshBuildDir(dir string, interval time.Duration, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-time.After(interval):
			touchBuildDir(dir)
		}
	}
}

// touchBuildDir marks dir as still in use by moving its modification time to
// now.
//
// Split out of keepBuildDirFresh's loop so a case can call it. The loop sleeps
// for minutes at a time and cannot be driven from a test, so with the Chtimes
// call inlined there this half of the reaper contract had nothing pinning it:
// replacing it with `_ = now` left the whole suite green. The reaper's other
// half, that it judges the entry it removes, has had a case since it was
// written. TestARefreshedBuildDirectoryOutlivesTheReaper is this one's.
func touchBuildDir(dir string) {
	now := time.Now()
	os.Chtimes(dir, now, now)
}

// reapAbandonedBuildDirectories removes build directories left behind by runs
// that crashed. Errors are ignored throughout: another user's directory is not
// ours to remove, and failing to reclaim disk space is not a reason to fail a
// test run.
// The directory to sweep is a parameter so a case can point it at a scratch
// directory. Sweeping the real TMPDIR from a case would race every other
// conformance run on the machine, including a concurrent one of this suite.
func reapAbandonedBuildDirectories(within string) {
	// ReadDir and a prefix test rather than filepath.Glob: Glob would read
	// the directory's own path as pattern text, so a TMPDIR holding a glob
	// metacharacter breaks the sweep silently and permanently. Observed: with
	// a directory literally named "tmp[1]", Glob matches nothing at all --
	// "[1]" is a character class -- while ReadDir finds the entry; an
	// unterminated "[" returns ErrBadPattern into the return above. Every
	// error here is swallowed by design, so there would be no signal.
	entries, err := os.ReadDir(within)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), buildDirPrefix) {
			continue
		}
		path := filepath.Join(within, entry.Name())
		// Lstat, not Stat: the decision is about this entry, and RemoveAll
		// below unlinks a symlink rather than following it, so a Stat judges
		// one object and acts on another. The consequence
		// TestTheReaperJudgesTheEntryItRemoves pins is a symlink created
		// moments ago being destroyed because what it points at is old. The
		// mirror case -- a long-stale link kept alive forever by a busy
		// target, or skipped outright because a dangling one fails the stat --
		// follows from the same confusion but is not exercised: ageing a
		// symlink's own stamp needs a call the standard library does not
		// have.
		info, err := os.Lstat(path)
		if err != nil || time.Since(info.ModTime()) < buildDirAbandonedAfter {
			continue
		}
		os.RemoveAll(path)
	}
}

// requireVCSStampedBuild returns the binary built with VCS stamping forced on.
// Under CI a missing one is a failure; elsewhere the case skips, so that a
// degraded run is visible rather than green.
//
// The CI failure is deliberate for EVERY reason the build is missing, the
// benign-sounding one included, and a review has already read that as
// overreach once. `go build -buildvcs=true` in a tree with no repository does
// not fail: it exits 0 and stamps nothing (verified on go1.25.7 against a
// copy of this tree with no .git), so buildForcingVCSStamp reports "carries no
// vcs.revision setting" and this fatals. Skipping that case under CI instead
// would be backwards. A CI checkout that has stopped carrying .git -- a
// container COPY without it, a checkout action misconfigured -- is exactly the
// degradation this escalation exists to surface, and it is far likelier there
// than the tarball-with-CI=true reading that motivates the skip. Failing costs
// a red build whose message names the cause precisely; skipping costs the
// pseudo-version half of the provenance contract, silently, in the one
// environment nobody is watching a skip in.
func requireVCSStampedBuild(t *testing.T) string {
	t.Helper()
	if mackupVCSBin != "" {
		return mackupVCSBin
	}
	why := fmt.Sprintf("no VCS-stamped build is available, so the pseudo-version half of the provenance contract cannot be exercised here: %v", mackupVCSBuildErr)
	if runningUnderCI(os.Getenv("CI")) {
		t.Fatal(why)
	}
	t.Skip(why)
	return ""
}

// runningUnderCI reports whether value -- the CI environment variable -- means
// this run is a CI run.
//
// The value is passed in rather than read here so that a case can drive every
// spelling; the two callers pass os.Getenv("CI").
//
// The falsy set is the point. CI=false and CI=0 are both things people export
// deliberately, neither means "running under CI", and treating either as such
// turns a skip this code chose to tolerate into a failed `make check` on a
// developer's own machine. That was already the intent when this was two
// inline switches -- but they compared the raw string against exactly "",
// "false" and "0", so CI=False, CI=FALSE, CI=no and CI=off all fell to the
// default and hard-failed the gate they were written to keep green. Lowercased
// and trimmed here, with "no" and "off" added, because those are the spellings
// people actually export.
//
// Anything else is CI, the empty-but-set case included in the falsy list
// above: an unset variable and CI= are indistinguishable through Getenv, and
// the safe reading of "set to nothing" is the developer one, since CI systems
// that set it at all set it to a truthy word.
func runningUnderCI(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "false", "0", "no", "off":
		return false
	}
	return true
}

// requireStampedVCSBuild returns the release binary that also carries a VCS
// stamp. When it is missing this escalates exactly as requireVCSStampedBuild
// does, and for the same reasons -- read that comment before changing this one.
func requireStampedVCSBuild(t *testing.T) string {
	t.Helper()
	if mackupStampedVCSBin != "" {
		return mackupStampedVCSBin
	}
	why := fmt.Sprintf("no VCS-stamped release build is available, so the rule that a linker stamp outranks working-tree provenance cannot be exercised here: %v", mackupStampedVCSBuildErr)
	if runningUnderCI(os.Getenv("CI")) {
		t.Fatal(why)
	}
	t.Skip(why)
	return ""
}

// buildWithMake builds through the Makefile, so the suite exercises the same
// build the project ships rather than a second one written beside it. version
// is empty for a development build.
func buildWithMake(out, version string) error {
	return buildWithMakeEnv(out, version, environWithoutMakeflags())
}

// buildWithMakeEnv is buildWithMake with the child environment given
// explicitly, so a caller can force a toolchain setting for one build.
func buildWithMakeEnv(out, version string, env []string) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}

	// VERSION is always assigned, empty included. make exports every
	// command-line variable through MAKEFLAGS, and that environment variable
	// survives the go test process in between and is read back by this make --
	// so `make check VERSION=0.1.0` would otherwise stamp the development
	// binary too, and the case asserting the fallback token would fail on the
	// project's own documented release command. An explicit assignment here
	// outranks the inherited one.
	cmd := exec.Command("make", "build", "BINARY="+out, "VERSION="+version)
	cmd.Dir = root
	// And MAKEFLAGS is dropped outright, so no other override on the outer
	// command line reaches the binary under test either.
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building %s with make: %v\n%s", out, err, output)
	}
	return nil
}

// environWithoutMakeflags is the current environment with make's own variable
// channels removed.
func environWithoutMakeflags() []string {
	env := os.Environ()
	kept := env[:0]
	for _, entry := range env {
		switch name, _, _ := strings.Cut(entry, "="); name {
		case "MAKEFLAGS", "MFLAGS":
		default:
			kept = append(kept, entry)
		}
	}
	return kept
}

// goflagsForcingVCSStamp is the GOFLAGS a build must carry to stamp VCS
// provenance: the value `go env GOFLAGS` reports, with -buildvcs=true appended.
//
// Merged rather than replaced, for a developer's sake. Their GOFLAGS may carry
// settings this build still needs -- "-mod=mod" is the ordinary one -- and
// replacing it outright would make the forced-stamp binary the only one in the
// suite built without them. If that broke the build, the case would skip, or
// fatal under CI, with a message naming VCS stamping for a cause that had
// nothing to do with it.
//
// Read from `go env` and not from the environment, which is the whole point
// and was got wrong once. The previous version scanned os.Environ() and
// appended to a GOFLAGS entry found there. GOFLAGS is more often set with
// `go env -w`, which writes the file at `go env GOENV` and never appears in
// the environment at all -- it is set that way on the machine this was written
// on -- so that version found nothing to append to, set a fresh GOFLAGS, and
// an environment GOFLAGS replaces the file's value WHOLESALE rather than
// merging with it. The protection the paragraph above promises did not exist
// in the common case. Verified both halves: `go env GOFLAGS` here reports
// -buildvcs=false with no GOFLAGS in the environment, and with GOENV pointed
// at a file holding "-buildvcs=false -mod=mod", running with
// GOFLAGS=-buildvcs=true makes `go env GOFLAGS` report just -buildvcs=true.
//
// Appending is an override at all only because a later duplicate wins, which
// is a claim about cmd/go and so was run rather than recalled: on go1.25.7,
// GOFLAGS="-buildvcs=false -buildvcs=true" produces a stamped binary and the
// reverse order an unstamped one.
//
// TestTheForcedStampGOFLAGSKeepsWhatGoEnvAlreadyCarries pins it, because a
// merge that silently dropped everything would look exactly like this one on a
// machine whose GOFLAGS holds only -buildvcs=false.
func goflagsForcingVCSStamp(env []string) (string, error) {
	command := exec.Command("go", "env", "GOFLAGS")
	command.Env = env
	reported, err := command.Output()
	if err != nil {
		// Output(), not CombinedOutput(), because the stdout of this command
		// IS the value being parsed and merging stderr into it would corrupt
		// the GOFLAGS string. That makes the error the one place the child's
		// own diagnosis can live, and %v alone throws it away: an *exec.
		// ExitError renders as "exit status 2" and nothing else, while the
		// reason -- a malformed GOENV file, a setting the toolchain rejects --
		// sits in ExitError.Stderr, which Output() populated. Verified against
		// a deliberately bad `go env` invocation. Discarding it made this the
		// one build helper in the file that reported a failure the way the
		// comment on requireVCSStampedBuild says none of them may: fatal under
		// CI, with nothing to diagnose from.
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("reading GOFLAGS from go env: %v\n%s", err, exit.Stderr)
		}
		return "", fmt.Errorf("reading GOFLAGS from go env: %v", err)
	}
	return strings.TrimSpace(strings.TrimSpace(string(reported)) + " -buildvcs=true"), nil
}

// environWith returns env with name set to value, replacing any existing
// setting rather than adding a second one.
//
// A plain replacement is right here now that the merging happens in
// goflagsForcingVCSStamp, against the effective value rather than against
// whatever half of it the environment happens to hold.
func environWith(env []string, name, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if existing, _, _ := strings.Cut(entry, "="); existing == name {
			continue
		}
		out = append(out, entry)
	}
	return append(out, name+"="+value)
}

// buildStampedForcingVCSStamp builds a release binary through the Makefile
// with VCS stamping forced on, and returns an error unless it came out
// stamped.
//
// Through the Makefile, because the -X symbol path lives there and this has to
// be a real release build. With GOFLAGS overridden, because -buildvcs=auto is
// what leaves the release binary unstamped on the machines where this matters.
// Both halves are needed for one binary to carry a linker stamp and a
// vcs.revision at once, which is the only state in which the precedence rule
// has anything to decide.
func buildStampedForcingVCSStamp(out, version string) error {
	base := environWithoutMakeflags()
	goflags, err := goflagsForcingVCSStamp(base)
	if err != nil {
		return err
	}
	env := environWith(base, "GOFLAGS", goflags)
	if err := buildWithMakeEnv(out, version, env); err != nil {
		return err
	}
	return requireVCSRevision(out)
}

// requireVCSRevision reports whether the artifact at out carries the VCS
// stamp, since -buildvcs=true is a request the toolchain may decline in
// silence. buildForcingVCSStamp says why the exit status cannot answer this.
func requireVCSRevision(out string) error {
	info, err := buildinfo.ReadFile(out)
	if err != nil {
		return fmt.Errorf("reading the build info of %s: %v", out, err)
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return nil
		}
	}
	return fmt.Errorf("%s built with -buildvcs=true but carries no vcs.revision setting, so the toolchain stamped nothing and the build cannot exercise the provenance rule", out)
}

// buildForcingVCSStamp builds with -buildvcs=true and returns an error unless
// the artifact actually came out stamped.
//
// The check is the point. -buildvcs=true reads like a demand and is not one:
// cmd/go treats "no repository here" as nothing to do rather than as a
// failure -- in load.setBuildInfo the error from vcs.FromDir is ignored when
// it wraps fs.ErrNotExist -- so a tree with no .git builds clean, exits 0, and
// carries no vcs.* settings at all. It errors only when a repository IS found
// and the VCS command then fails: git missing, git refusing a dubiously-owned
// repository, a repository that does not contain the module.
//
// Inferring "stamped" from the exit status therefore hands the version case a
// binary reporting "(devel)", which reports the fallback token for the
// trivial reason -- the vacuous pass this whole build exists to prevent,
// restored in exactly the environment the caller's comment claimed was
// covered: a source tarball, a vendored copy, a Docker build that COPYs
// without .git. Verified by building this module from a copy of the tree with
// .git removed: exit 0, "mod ... (devel)", no vcs settings.
func buildForcingVCSStamp(out string) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}

	cmd := exec.Command("go", "build", "-buildvcs=true", "-o", out, "./cmd/mackup")
	cmd.Dir = root
	cmd.Env = environWithoutMakeflags()
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building %s with VCS stamping forced on: %v\n%s", out, err, output)
	}

	return requireVCSRevision(out)
}

// moduleRoot is the directory holding go.mod, found by walking up from the
// working directory -- which `go test` sets to the package's source directory.
//
// Deliberately not runtime.Caller: -trimpath rewrites the compiled-in file
// path to a module-relative one, so a moduleRoot built from it points at a
// directory that does not exist and `go test -trimpath ./test/conformance/`
// loses the entire suite in TestMain.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("locating the module root: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("locating the module root: no go.mod above %s", dir)
		}
		dir = parent
	}
}

// reporter is the part of *testing.T the harness reports failures through.
//
// It is an interface for exactly one reason: without a seam here, nothing can
// observe that ExpectUnchanged reported anything, and a case that asserts the
// filesystem is unchanged is unfalsifiable. That is not hypothetical --
// replacing ExpectUnchanged's body with `_ = before` left the whole suite
// reporting ok, with six cases carrying the spec's "touched nothing" promises
// asserting nothing at all. captureReport is what closes that.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// World is one throwaway environment: a home directory, an environment
// containing only what the program is allowed to see, and the binary to run.
//
// The environment is built up rather than inherited, so a variable the spec
// reads -- HOME, XDG_CONFIG_HOME, MACKUP_CONFIG -- is never leaked in from the
// developer's shell and cannot make a case pass on one machine and fail on
// another.
type World struct {
	t reporter

	// Root is the scratch directory; Home is the home directory inside it.
	Root string
	Home string

	bin string
	env map[string]string
}

// NewWorld creates a throwaway world whose home directory is empty. The
// scratch directory is removed when the test finishes.
func NewWorld(t *testing.T) *World {
	t.Helper()
	// Here rather than in TestMain: see readImplementationSources for why the
	// cache key is only honest when this runs inside a case.
	readSourcesOnce.Do(readImplementationSources)
	root := t.TempDir()
	// Registered after t.TempDir's own cleanup and so run before it, this
	// makes the scratch root removable again. A case that leaves a directory
	// unreadable -- the natural fixture for a tool that manages ~/.ssh and
	// ~/.gnupg -- otherwise fails in testing's RemoveAll with a message about
	// the harness, after every assertion in it has already passed. Verified by
	// leaving a 0000 directory behind and watching the case fail in cleanup.
	//
	// WalkDir reports a directory before it reads it, so widening the mode
	// here is what lets the walk descend to the next one.
	t.Cleanup(func() {
		filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("creating the home directory: %v", err)
	}
	// TMPDIR inside the root, so the scratch root really is the whole world.
	// Snapshot walks the root and nothing else, so a temporary file the
	// program writes through os.TempDir would land outside it and go
	// unrecorded -- and "changed nothing" would hold over a run that wrote.
	// That is not a remote shape for this program: an atomic copy through
	// os.CreateTemp is the ordinary way to implement appspec/01 section 3's
	// copy operations, and it would arrive with every existing --dry-run and
	// rejected-run assertion silently narrowed.
	//
	// Created here rather than left to the program, which is entitled to
	// assume TMPDIR exists.
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatalf("creating the temporary directory: %v", err)
	}
	return &World{
		t:    t,
		Root: root,
		Home: home,
		bin:  mackupBin,
		env: map[string]string{
			"HOME":   home,
			"PATH":   os.Getenv("PATH"),
			"TMPDIR": tmp,
		},
	}
}

// UseBinary switches this world to another of the builds under test.
func (w *World) UseBinary(path string) { w.bin = path }

// UseStampedBinary switches this world to the release build, which carries a
// version stamp as an installed build does.
func (w *World) UseStampedBinary() { w.bin = mackupStampedBin }

// Setenv sets an environment variable for every command this world runs.
// TestAVariableTheWorldSetsReachesTheProgram pins it: until that case existed
// nothing called this, and an untested seam in the harness is worth no more
// than an untested guard.
func (w *World) Setenv(name, value string) { w.env[name] = value }

// Path resolves a home-relative path. appspec/00 promise 7 makes the
// home-relative path the unit the program stores things at, so cases are
// written in those terms.
func (w *World) Path(relative ...string) string {
	return filepath.Join(append([]string{w.Home}, relative...)...)
}

// SnapshotKey is how Snapshot names a home-relative path, so a case can look
// one up without hardcoding the "home/" prefix Snapshot's root-relative keys
// carry.
func (w *World) SnapshotKey(relative ...string) string {
	return filepath.Join(append([]string{"home"}, relative...)...)
}

// WriteFile creates a home-relative file, making its parent directories.
func (w *World) WriteFile(relative, content string, perm fs.FileMode) string {
	w.t.Helper()
	path := w.Path(relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		w.t.Fatalf("creating the parent of %s: %v", relative, err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		w.t.Fatalf("writing %s: %v", relative, err)
	}
	return path
}

// UseResolvableStorage gives the world a config file whose storage engine can
// resolve, and returns the storage root it resolves to.
//
// appspec/02 makes config load a gate every subcommand passes -- "a run whose
// configured (or default) engine cannot locate its storage folder fails at
// load time ... regardless of which subcommand was requested" -- and a fresh
// world has no config at all, so the default Dropbox engine finds nothing and
// every subcommand dies before its own behavior is reached. A case about
// anything past that gate has to get through it first.
//
// file_system is the engine used because it is the only one of the four that
// resolves without a third-party sync client installed: appspec/04 gives it a
// path the user supplies and, deliberately, no existence check. The directory
// is created anyway, so the world also satisfies the storage-root existence
// check of appspec/01 section 4 once that gate lands
// (macklebox-resolvers-5iw.4) -- a case written now should not have to be
// revisited then.
//
// Written to ~/.mackup.cfg, the first discovery candidate, so this helper does
// not depend on any environment variable a case might also be setting.
func (w *World) UseResolvableStorage() string {
	w.t.Helper()
	w.WriteFile(".mackup.cfg", "[storage]\nengine = file_system\npath = storage\n", 0o600)
	root := w.Path("storage")
	if err := os.MkdirAll(root, 0o700); err != nil {
		w.t.Fatalf("creating the storage root: %v", err)
	}
	return root
}

// UseMackupFolder gives the world a resolvable storage root AND the Mackup
// folder inside it, and returns that folder.
//
// appspec/01 section 4 makes the Mackup folder a FIFTH gate, and it is the only
// per-command one: backup and link install ENSURE it exists, prompting to
// create it when it does not, while restore, link and link uninstall REQUIRE it
// and fail when it does not. A world built with UseResolvableStorage alone is
// therefore in a state where backup stops to ask a question and restore
// refuses, which is correct and is exactly what a case about something else
// does not want to observe.
//
// The name is the default sub-directory of appspec/03 -- "Mackup" when
// [storage] directory is absent -- so a case that also sets `directory` must
// create its own folder rather than calling this.
func (w *World) UseMackupFolder() string {
	w.t.Helper()
	folder := filepath.Join(w.UseResolvableStorage(), "Mackup")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		w.t.Fatalf("creating the Mackup folder: %v", err)
	}
	return folder
}

// Run executes the command with no input available on stdin.
func (w *World) Run(args ...string) Result { return w.RunWithInput("", args...) }

// RunWithInput executes the command with stdin holding input. appspec/07 reads
// stdin only to answer confirmation prompts, so a case that expects a prompt
// supplies the answer here; every other case runs with stdin at end-of-input.
func (w *World) RunWithInput(input string, args ...string) Result {
	w.t.Helper()

	cmd := exec.Command(w.bin, args...)
	cmd.Dir = w.Root
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = w.environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// A non-zero exit is an observation, not a harness failure; anything else
	// means the process could not be run at all.
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			w.t.Fatalf("running mackup %s: %v", strings.Join(args, " "), err)
		}
	}

	return Result{
		w:    w,
		Args: args,
		// Read back off the exec.Cmd, not from w.environ(): what makes the
		// world isolating is that this field was assigned, and a case that
		// re-derived the value it should hold would pass just as happily if
		// the assignment were deleted.
		Env:      cmd.Env,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: cmd.ProcessState.ExitCode(),
	}
}

func (w *World) environ() []string {
	env := make([]string, 0, len(w.env))
	for name, value := range w.env {
		env = append(env, name+"="+value)
	}
	sort.Strings(env)
	return env
}

// Result is one observation of the boundary.
type Result struct {
	// The world rather than its reporter: captureReport swaps w.t for the
	// duration of a call and puts it back, so a Result that had copied the
	// reporter would keep a recorder nobody reads once the capture ends, and
	// every assertion made on it afterwards would report into the void. That
	// is the unfalsifiable assertion this seam exists to prevent, so the
	// reporter is resolved when the assertion is made, not when the process
	// was run. TestAssertionsOnAResultSurviveACapturedRegion pins it.
	w    *World
	Args []string
	// Env is the environment the process was run with, as name=value strings.
	Env      []string
	Stdout   string
	Stderr   string
	ExitCode int
}

func (r Result) invocation() string { return "mackup " + strings.Join(r.Args, " ") }

// sgrSequence matches one ANSI SGR sequence -- ESC [ parameters m -- which is
// the only escape this program emits.
//
// Deliberately NOT internal/ui's own pattern, and this is the one place the
// duplication is the point rather than a cost. This package observes the
// program from outside and imports nothing from it; a suite that stripped
// colour with the program's own definition of colour would agree with it by
// construction, and the cases below would hold for any pair of definitions
// that happened to match -- including a broken pair. The header of this file
// says nothing here reaches inside the program; the colour scheme is not an
// exception to that, it is a case of it.
var sgrSequence = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripSGR removes every colour sequence, leaving the message.
//
// appspec/07 colours whole messages, so what is left is the exact text -- and
// that is what makes it legitimate to assert a literal contract token against
// a stream appspec/02 requires to be a "single colored diagnostic line".
func stripSGR(text string) string { return sgrSequence.ReplaceAllString(text, "") }

// hasSGR reports whether text carries colour.
func hasSGR(text string) bool { return sgrSequence.MatchString(text) }

// StdoutText and StderrText are the streams with their colour removed. The raw
// fields stay raw: an emptiness assertion has to be able to tell "nothing" from
// "escape sequences and nothing else".
func (r Result) StdoutText() string { return stripSGR(r.Stdout) }

// StderrText is the stderr half of StdoutText.
func (r Result) StderrText() string { return stripSGR(r.Stderr) }

// ExpectExit asserts the process exit code.
func (r Result) ExpectExit(code int) Result {
	r.w.t.Helper()
	if r.ExitCode != code {
		r.w.t.Errorf("%s exited %d, want %d\nstdout: %q\nstderr: %q", r.invocation(), r.ExitCode, code, r.Stdout, r.Stderr)
	}
	return r
}

// ExpectFailureExit asserts the process failed, without saying how. For a
// condition the spec gives an exit code, use ExpectExit; this is for the ones
// it leaves to the implementation, where pinning the number would fail a
// conformant program for making a different choice.
func (r Result) ExpectFailureExit() Result {
	r.w.t.Helper()
	if r.ExitCode == 0 {
		r.w.t.Errorf("%s: exit = 0, want a non-zero code: the run did not complete the action asked of it", r.invocation())
	}
	return r
}

// ExpectStdout asserts stdout contains want. appspec/07 makes the stream a
// message lands on contract, so every message assertion names its stream.
func (r Result) ExpectStdout(want string) Result {
	r.w.t.Helper()
	if !strings.Contains(r.Stdout, want) {
		r.w.t.Errorf("%s stdout does not contain %q\nstdout: %q", r.invocation(), want, r.Stdout)
	}
	return r
}

// ExpectStderr asserts stderr contains want.
func (r Result) ExpectStderr(want string) Result {
	r.w.t.Helper()
	if !strings.Contains(r.Stderr, want) {
		r.w.t.Errorf("%s stderr does not contain %q\nstderr: %q", r.invocation(), want, r.Stderr)
	}
	return r
}

// ExpectEitherStream asserts the text reached the user, without saying which
// stream carried it. For the cases where appspec/02 requires the program to
// show something and does not say where -- pinning a stream there would fail a
// conforming implementation over a promise the spec declines to make.
func (r Result) ExpectEitherStream(want string) Result {
	r.w.t.Helper()
	if !strings.Contains(r.Stdout, want) && !strings.Contains(r.Stderr, want) {
		r.w.t.Errorf("%s: neither stream contains %q\nstdout: %q\nstderr: %q", r.invocation(), want, r.Stdout, r.Stderr)
	}
	return r
}

// ExpectStderrLine asserts stderr is exactly one line, equal to want. Used for
// the literal contract tokens of appspec/07.
//
// Compared against the text rather than the raw bytes, because appspec/02's
// exit-code table requires these to be "a single colored diagnostic line": the
// colour is contract too, and asserted separately by ExpectStderrColor, but it
// is not part of the token.
//
// The contiguity check beside it is the half that stripping would otherwise
// throw away, and it is the actual promise appspec/07 makes about a token
// "matched by scripts/tests". A program that opened a colour in the MIDDLE of
// the token would leave the stripped text equal and the raw stream ungreppable
// -- exactly the failure a script would hit and the suite would not. Verified
// by splitting the force-conflict line with an escape: stripped-only passes,
// this fails.
func (r Result) ExpectStderrLine(want string) Result {
	r.w.t.Helper()
	if r.StderrText() != want+"\n" {
		r.w.t.Errorf("%s stderr = %q, want exactly %q once colour is stripped", r.invocation(), r.Stderr, want+"\n")
	} else if !strings.Contains(r.Stderr, want) {
		r.w.t.Errorf("%s stderr = %q: the text is right but colour splits it, so a script matching %q verbatim would miss it", r.invocation(), r.Stderr, want)
	}
	return r
}

// ExpectStdoutLine asserts stdout is exactly one line, equal to want. The
// stdout mirror of ExpectStderrLine, for the literal tokens appspec/00 names.
//
// Contains is not enough for a value the spec gives literally. Every case
// asserting the version VALUE used ExpectStdout, and ExpectVersionLine pins
// only the shape -- its \S+ matches a suffixed token just as happily -- so a
// Banner() returning "Mackup unknown-extra" passed the entire black-box suite
// and was caught only by internal/version's unit test. Observed. That is the
// same vacuity round 21 closed for the line's shape, left open for its value.
func (r Result) ExpectStdoutLine(want string) Result {
	r.w.t.Helper()
	if r.StdoutText() != want+"\n" {
		r.w.t.Errorf("%s stdout = %q, want exactly %q once colour is stripped", r.invocation(), r.Stdout, want+"\n")
	} else if !strings.Contains(r.Stdout, want) {
		r.w.t.Errorf("%s stdout = %q: the text is right but colour splits it, so a script matching %q verbatim would miss it", r.invocation(), r.Stdout, want)
	}
	return r
}

// versionLine is the whole of stdout for a --version run: the banner,
// one line, nothing after it.
var versionLine = regexp.MustCompile(`^Mackup \S+\n$`)

// ExpectVersionLine asserts stdout is exactly the "Mackup <version>" line
// appspec/00-overview.md "Provenance" names, and nothing else.
//
// It exists because the obvious spelling is vacuous. Three cases asserted
// ExpectStdout("Mackup "), and the usage block opens with "Mackup - Keep your
// application settings in sync." -- which contains that substring, so every
// one of them was satisfied by the help text. A program answering `mackup
// --version list` with the whole usage block passed the entire suite.
// Observed, by making it do exactly that. The anchored match is what makes
// these cases able to fail for the reason they claim.
func (r Result) ExpectVersionLine() Result {
	r.w.t.Helper()
	if !versionLine.MatchString(r.StdoutText()) {
		r.w.t.Errorf("%s stdout = %q, want exactly one \"Mackup <version>\" line once colour is stripped", r.invocation(), r.Stdout)
	}
	return r
}

// ExpectStdoutColor asserts stdout opens in the given SGR parameters and that
// every sequence in it is terminated -- appspec/07: "Every colored string is
// terminated with a reset."
//
// The parameters are passed as the spec writes them ("33", "91"), so a case
// reads as the line of appspec/07 it comes from rather than as a byte string.
func (r Result) ExpectStdoutColor(parameters string) Result {
	r.w.t.Helper()
	return r.expectColor("stdout", r.Stdout, parameters)
}

// ExpectStderrColor is the stderr half of ExpectStdoutColor.
func (r Result) ExpectStderrColor(parameters string) Result {
	r.w.t.Helper()
	return r.expectColor("stderr", r.Stderr, parameters)
}

func (r Result) expectColor(name, text, parameters string) Result {
	r.w.t.Helper()
	if !hasSGR(text) {
		r.w.t.Errorf("%s %s = %q, want it coloured: appspec/07 emits colour unconditionally, and this stream is a pipe, not a terminal", r.invocation(), name, text)
		return r
	}
	// Line by line, because appspec/07's promise is about every colored
	// STRING, not about the stream. A stream can legitimately mix the two: a
	// usage error writes a coloured diagnostic and then the uncoloured usage
	// block, and asking that the whole of stderr end in a reset failed there
	// over output that is exactly right. Observed, which is why this is not
	// the one-line HasSuffix it started as.
	first := true
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if !hasSGR(line) {
			continue
		}
		if first {
			if open := "\x1b[" + parameters + "m"; !strings.HasPrefix(line, open) {
				r.w.t.Errorf("%s %s first coloured line = %q, want it to open with %q", r.invocation(), name, line, open)
			}
			first = false
		}
		if !strings.HasSuffix(line, "\x1b[0m") {
			r.w.t.Errorf("%s %s coloured line = %q, want it to end in a reset; appspec/07: every colored string is terminated with a reset", r.invocation(), name, line)
		}
	}
	return r
}

// ExpectUncolored asserts a stream carries no colour at all. For the argument
// parser's usage block, which appspec/07's scheme gives no level.
func (r Result) ExpectUncolored(name string) Result {
	r.w.t.Helper()
	text := r.Stdout
	if name == "stderr" {
		text = r.Stderr
	}
	if hasSGR(text) {
		r.w.t.Errorf("%s %s = %q, want no colour", r.invocation(), name, text)
	}
	return r
}

// ExpectSilentStdout asserts nothing was written to stdout. Both config-failure
// regimes of appspec/01 section 6 share the post-condition "no stdout".
func (r Result) ExpectSilentStdout() Result {
	r.w.t.Helper()
	if r.Stdout != "" {
		r.w.t.Errorf("%s wrote to stdout, want nothing\nstdout: %q", r.invocation(), r.Stdout)
	}
	return r
}

// ExpectNotImplemented asserts the invocation got past the parser, reached
// cmd's dispatch arm, and found it unimplemented.
//
// Like usageMarker this wording is scaffolding, not spec. It exists so "argv
// accepts this form" can be asserted positively -- the form reached its
// dispatch arm -- instead of as the absence of a usage error, which also holds
// when the program is broken some other way and so cannot fail for the reason
// the case claims. Each use is replaced by an assertion on the command's real
// behavior as that command's ticket lands.
func (r Result) ExpectNotImplemented(cmd string) Result {
	r.w.t.Helper()
	return r.ExpectExit(1).
		ExpectStderrLine("Error: " + cmd + " is not implemented yet.").
		ExpectSilentStdout()
}

// ExpectSilentStderr asserts nothing was written to stderr.
func (r Result) ExpectSilentStderr() Result {
	r.w.t.Helper()
	if r.Stderr != "" {
		r.w.t.Errorf("%s wrote to stderr, want nothing\nstderr: %q", r.invocation(), r.Stderr)
	}
	return r
}

// Snapshot records every path under the world's scratch root -- its mode, its
// modification time, and its content, or for a symlink its target -- so a case
// can assert what a command did or did not change.
//
// The modification time is in that list because it is load-bearing, not
// incidental: it is the only field that separates "left alone" from "rewritten
// with the bytes it already held", which is the shape a copy takes when the
// source and destination already agree, and so the field that makes the
// dry-run "performed no copy" contract of appspec/01 section 3 checkable at
// all. TestTheSnapshotSeesAFileRewrittenWithTheBytesItAlreadyHeld pins it, and
// the battery's "Snapshot mtime dropped" entry kills a record without it.
type Snapshot map[string]string

// Snapshot captures the current state of the whole scratch root. The failure
// post-condition shared by both regimes of appspec/01 section 6, and the
// dry-run contract of appspec/01 section 3, are both "no filesystem change" --
// which is only checkable against a recorded before-state.
//
// The root, not Home: the Mackup folder can sit outside home (appspec/04's
// file_system engine takes an arbitrary path), and the command runs with its
// working directory at Root, so a snapshot of Home alone is blind to storage-
// side and cwd mutations -- exactly the writes a "changed nothing" assertion
// exists to catch. Paths are root-relative, so a file in home reads as
// "home/.mackup.cfg".
func (w *World) Snapshot() Snapshot {
	w.t.Helper()
	snapshot := Snapshot{}
	err := filepath.WalkDir(w.Root, func(path string, entry fs.DirEntry, err error) error {
		relative, relErr := filepath.Rel(w.Root, path)
		if relErr != nil {
			return relErr
		}
		if err != nil {
			// A directory this process may not list. WalkDir has already
			// reported it once without an error, so the entry is recorded;
			// what is lost is what is inside it, which is said out loud in
			// the record rather than by aborting the whole snapshot.
			if errors.Is(err, fs.ErrPermission) {
				snapshot[relative] += contentsUnreadable
				return fs.SkipDir
			}
			return err
		}
		info, err := entry.Info()
		if err != nil {
			// The third permission shape, and the only one this walk used to
			// abort over. A directory at 0400 is readable but not searchable:
			// ReadDir lists its children, and the lstat behind Info() on each
			// of them fails with EACCES. Verified with a standalone WalkDir
			// over a 0400 directory.
			//
			// Aborting there fataled the harness -- "snapshotting the scratch
			// root: ... permission denied" -- and took every remaining
			// assertion in the case with it, which is the exact outcome the
			// two branches around this one were written to avoid, in the same
			// words: "aborting here would replace the case's own assertion
			// with a complaint about the harness". This one was left behind.
			//
			// Recorded rather than skipped, so the entry's existence is still
			// compared: it appearing or vanishing is caught by the created and
			// removed branches of ExpectUnchanged. What is lost is everything
			// else about it, which is why the marker is reported there.
			if errors.Is(err, fs.ErrPermission) {
				snapshot[relative] = entryUnstatable
				return nil
			}
			return err
		}
		// The modification time is part of the record. Without it a run that
		// rewrites a file with the bytes it already held -- a --dry-run that
		// copied anyway, an "already in sync" backup that copied regardless --
		// leaves an identical snapshot, and the assertion that exists to catch
		// exactly that passes.
		stamp := info.ModTime().UnixNano()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			// %q, not %s. The target is program-controlled text -- appspec/05's
			// link engine makes these links, so their targets are this
			// program's own output -- and it lands at the END of the record,
			// where ExpectUnchanged's blindness scan matches contentsUnreadable
			// with HasSuffix. A raw target ending in " <contents unreadable>"
			// therefore reported every ExpectUnchanged in that world as blind,
			// with a diagnostic telling the author to make a fixture readable
			// that already is: the same spurious misdirecting failure the
			// anchored scan was written to remove for file content, surviving
			// one branch over. Quoting escapes it, and gives the record the
			// same closing-quote terminator a readable file's has.
			snapshot[relative] = fmt.Sprintf("symlink %04o @%d -> %q", info.Mode().Perm(), stamp, target)
		case entry.IsDir():
			snapshot[relative] = fmt.Sprintf("dir %04o @%d", info.Mode().Perm(), stamp)
		case !info.Mode().IsRegular():
			// Recorded by type, never opened. A home directory holds FIFOs and
			// unix sockets -- ~/.gnupg is full of them, and this program walks
			// home directories -- and opening a FIFO with no writer blocks
			// until the test binary's own timeout kills the run, turning a
			// filesystem change into a hang.
			snapshot[relative] = fmt.Sprintf("%s %04o @%d", info.Mode().Type(), info.Mode().Perm(), stamp)
		default:
			content, err := os.ReadFile(path)
			if err != nil {
				// Recorded, not fatal. This is a tool for dotfiles, so a
				// fixture like a 0600 ~/.ssh/id_rsa that this process cannot
				// read is ordinary; aborting here would replace the case's
				// own assertion with a complaint about the harness, and the
				// post-condition the case exists to check would go unmade.
				// Mode, size and stamp all still come from the stat, so a
				// rewrite is still visible -- and the size is here because
				// mode and stamp alone were not enough. For a READABLE file
				// the recorded bytes are a second witness that does not depend
				// on clock granularity; an unreadable one has none, so on a
				// filesystem with one-second stamps (ext3, HFS+ -- this suite
				// already concedes they exist and skips a case for it) a
				// dry-run that copied anyway within the same second moved
				// nothing in the record at all. The size is free, it is
				// already in hand from this stat, and it catches every rewrite
				// that changes the length.
				//
				// What it does not catch, stated rather than glossed: a
				// same-length rewrite of an unreadable file inside the stamp's
				// granularity. That is a strictly smaller gap than the one the
				// suite already accepts for readable files on such a
				// filesystem, and closing it would mean reading bytes this
				// process is not allowed to read.
				if errors.Is(err, fs.ErrPermission) {
					snapshot[relative] = fmt.Sprintf("file %04o %dB @%d <unreadable>", info.Mode().Perm(), info.Size(), stamp)
					return nil
				}
				return err
			}
			snapshot[relative] = fmt.Sprintf("file %04o @%d %q", info.Mode().Perm(), stamp, content)
		}
		return nil
	})
	if err != nil {
		w.t.Fatalf("snapshotting the scratch root: %v", err)
	}
	return snapshot
}

// recordingReporter stands in for *testing.T so a case can assert on what the
// harness reported instead of being failed by it.
//
// Fatalf panics rather than recording and continuing: the real one does not
// return, and a stand-in that let execution run on would turn a harness
// failure into whatever the code after it happened to do next.
type recordingReporter struct {
	messages []string
}

// fatalFromRecorder carries a recorded Fatalf out through a panic, so it can
// be told apart from a genuine one on the way back up.
type fatalFromRecorder string

func (r *recordingReporter) Helper() {}

func (r *recordingReporter) Errorf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

func (r *recordingReporter) Fatalf(format string, args ...any) {
	panic(fatalFromRecorder(fmt.Sprintf(format, args...)))
}

// captureReport runs fn with the world reporting into a recorder, and returns
// what it reported. A Fatalf inside fn is re-raised on the real *testing.T,
// so a broken harness still stops the case rather than being swallowed here.
func (w *World) captureReport(t *testing.T, fn func()) []string {
	t.Helper()
	original := w.t
	recorder := &recordingReporter{}
	w.t = recorder
	defer func() {
		w.t = original
		if raised := recover(); raised != nil {
			fatal, ok := raised.(fatalFromRecorder)
			if !ok {
				panic(raised)
			}
			t.Fatalf("the harness failed while its report was being captured: %s", string(fatal))
		}
	}()
	fn()
	return recorder.messages
}

// ExpectUnchanged asserts the scratch root still matches before.
func (w *World) ExpectUnchanged(before Snapshot) {
	w.t.Helper()
	after := w.Snapshot()

	// A directory this process could not list is recorded, but nothing inside
	// it is, so two records of it compare equal whatever happened in there.
	// Reported rather than compared, by this suite's own standard that an
	// assertion which cannot fail is not an assertion. Nothing drove this
	// branch before -- the arm existed and no case reached it, which is the
	// shape that has already shipped twice on this branch.
	//
	// The blind spot is narrower than it looks, and the difference is worth
	// stating exactly rather than overclaiming it. The directory's own mtime
	// IS in the record, and creating or removing an entry moves it, so a file
	// appearing or vanishing inside an unlistable directory is still caught.
	// What is invisible is an in-place rewrite of a file already there, which
	// moves that file's mtime and not its parent's -- precisely the
	// dry-run-that-copied-anyway shape the stamp field exists for. Measured on
	// this filesystem, all three, not assumed.
	//
	// The file half of the same situation needs none of this: an unreadable
	// FILE still contributes its mode and stamp from the stat, so a rewrite of
	// it remains visible. It is the two branches that lose the stat which are
	// blind -- the unlistable directory above, and the entry that could not be
	// stat'd at all, which is what a 0400 (readable, not searchable) parent
	// produces. The second is blind to more: nothing about the entry is known,
	// so only its appearance or removal is still compared.
	// Matched at a fixed end of the record, never with Contains. A regular
	// file's record carries its %q-rendered CONTENT, so a Contains scan reads
	// a fixture whose bytes happen to hold the marker text as a degraded
	// record: the case fails with nothing changed and nothing blind, and the
	// diagnostic tells the author to make a fixture readable that already is.
	// Anchoring removes the ambiguity outright rather than making it
	// unlikely. entryUnstatable REPLACES the record so it is a prefix;
	// contentsUnreadable is appended so it is a suffix; and neither can be
	// faked from the other side. That last half is a property of every branch
	// of Snapshot, checked branch by branch rather than assumed: the two
	// carrying arbitrary text -- a regular file's content and a symlink's
	// target -- both render it with %q and so end in a closing quote, and the
	// rest (dir, unreadable file, other types) end in a digit or a fixed
	// literal. None of them begins with entryUnstatable either; they begin
	// with "file ", "dir ", "symlink " or a mode type. The symlink branch was
	// the one that did NOT hold when this was first written.
	//
	// entryUnstatable is tested first because an entry can be both -- an
	// unstatable directory that also cannot be listed records one then the
	// other -- and its message is the stronger of the two.
	blindness := []struct {
		blind func(record string) bool
		why   string
	}{
		{func(record string) bool { return strings.HasPrefix(record, entryUnstatable) },
			"could not be examined at all -- its mode, type and modification time are all unknown -- so this assertion is blind to every change to it but its removal; make its parent directory searchable in the fixture"},
		{func(record string) bool { return strings.HasSuffix(record, contentsUnreadable) },
			"could not be listed, so this assertion is blind to a file rewritten in place inside it; make the fixture readable, or assert on what is in there directly"},
	}
	blind, seen := []string{}, map[string]bool{}
	for _, snapshot := range []Snapshot{before, after} {
		for path, record := range snapshot {
			if seen[path] {
				continue
			}
			for _, shape := range blindness {
				if shape.blind(record) {
					seen[path] = true
					blind = append(blind, path+" "+shape.why)
					break
				}
			}
		}
	}
	sort.Strings(blind)
	for _, message := range blind {
		w.t.Errorf("%s", message)
	}

	for path, want := range before {
		got, ok := after[path]
		if !ok {
			w.t.Errorf("%s was removed; it held %s", path, want)
			continue
		}
		if got != want {
			w.t.Errorf("%s changed:\n  before: %s\n   after: %s", path, want, got)
		}
	}
	for path, got := range after {
		if _, ok := before[path]; !ok {
			w.t.Errorf("%s was created; it holds %s", path, got)
		}
	}
}
