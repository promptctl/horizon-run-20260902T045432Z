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
//     that holds under any invocation that runs a case -- any tags, any tool,
//     no flags of ours required. It is stat-based, not content-based, and
//     the limit that follows from that is spelled out on
//     readImplementationSources.
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
	"testing"
	"time"
)

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
// shape is a filter selecting only cases that build no world:
// TestTheReaperJudgesTheEntryItRemoves and
// TestTheReaperFindsDirectoriesUnderAPathHoldingAGlobMetacharacter are both
// like that today, and running either alone leaves this walk unexecuted --
// verified, against the earlier claim here that a no-case filter was the whole
// of it. Neither is a stale-pass hazard, because neither observes the program:
// they exercise this package's own code, which cmd/go already folds into the
// test binary's build ID. What WOULD be one is a case that observes the
// program without going through NewWorld. There is none; do not write one.
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
			if entry.Name() == ".git" || path == filepath.Join(root, "bin") {
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

// keepBuildDirFresh refreshes dir's modification time until the process ends.
// It is never stopped: the goroutine costs one timer, and the process it
// belongs to is a test binary that exits from TestMain.
func keepBuildDirFresh(dir string) {
	go func() {
		for {
			time.Sleep(buildDirTouchInterval)
			touchBuildDir(dir)
		}
	}()
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
	// CI=false and CI=0 are both things people export deliberately; neither
	// means "running under CI", and treating them as such would turn a skip
	// this code chose to tolerate into a failed `make check` on a developer's
	// own machine.
	switch os.Getenv("CI") {
	case "", "false", "0":
	default:
		t.Fatal(why)
	}
	t.Skip(why)
	return ""
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
	switch os.Getenv("CI") {
	case "", "false", "0":
	default:
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
func (r Result) ExpectStderrLine(want string) Result {
	r.w.t.Helper()
	if r.Stderr != want+"\n" {
		r.w.t.Errorf("%s stderr = %q, want exactly %q", r.invocation(), r.Stderr, want+"\n")
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
	if r.Stdout != want+"\n" {
		r.w.t.Errorf("%s stdout = %q, want exactly %q", r.invocation(), r.Stdout, want+"\n")
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
	if !versionLine.MatchString(r.Stdout) {
		r.w.t.Errorf("%s stdout = %q, want exactly one \"Mackup <version>\" line", r.invocation(), r.Stdout)
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
				snapshot[relative] += " <contents unreadable>"
				return fs.SkipDir
			}
			return err
		}
		info, err := entry.Info()
		if err != nil {
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
			snapshot[relative] = fmt.Sprintf("symlink %04o @%d -> %s", info.Mode().Perm(), stamp, target)
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
				// Mode and stamp still come from the stat, so a rewrite is
				// still visible.
				if errors.Is(err, fs.ErrPermission) {
					snapshot[relative] = fmt.Sprintf("file %04o @%d <unreadable>", info.Mode().Perm(), stamp)
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
