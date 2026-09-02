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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
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

	// Why mackupVCSBin is empty, when it is.
	mackupVCSBuildErr error
)

func TestMain(m *testing.M) {
	// A fixed directory rather than a fresh one per run, because it has to be
	// findable by a later run: os.Exit does not run deferred functions, and a
	// panicking test never returns from m.Run() at all -- the testing package
	// re-panics on the test's own goroutine, which cannot be recovered from
	// here -- so a crashed run always leaves its binaries behind. Clearing the
	// directory on the way in bounds that at one run's worth of megabytes
	// rather than one run's worth per crash.
	//
	// The cost is that two suites running against the same TMPDIR at the same
	// time would clobber each other's binaries. Go runs one instance of a
	// package's tests at a time, so that takes two concurrent `go test`
	// invocations by hand, and is the better trade against an unbounded leak.
	dir := filepath.Join(os.TempDir(), "macklebox-conformance-bin")
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "conformance: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "conformance: %v\n", err)
		os.Exit(1)
	}
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
	// Where the toolchain cannot stamp at all -- a source tarball with no
	// repository, where -buildvcs=true is a hard error -- this build is simply
	// unavailable. The suite does not silently fall back to an unstamped
	// binary, which would restore the vacuous pass under a different name:
	// requireVCSStampedBuild reports the degradation, skipping locally and
	// failing under CI.
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

// requireVCSStampedBuild returns the binary built with VCS stamping forced on.
// Under CI a missing one is a failure; elsewhere the case skips, so that a
// degraded run is visible rather than green.
func requireVCSStampedBuild(t *testing.T) string {
	t.Helper()
	if mackupVCSBin != "" {
		return mackupVCSBin
	}
	why := fmt.Sprintf("no VCS-stamped build is available, so the pseudo-version half of the provenance contract cannot be exercised here: %v", mackupVCSBuildErr)
	if os.Getenv("CI") != "" {
		t.Fatal(why)
	}
	t.Skip(why)
	return ""
}

// buildWithMake builds through the Makefile, so the suite exercises the same
// build the project ships rather than a second one written beside it. version
// is empty for a development build.
func buildWithMake(out, version string) error {
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
	cmd.Env = environWithoutMakeflags()
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

// buildForcingVCSStamp builds with -buildvcs=true, which is an error rather
// than a silent decline when the repository cannot be read.
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
	return nil
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

// World is one throwaway environment: a home directory, an environment
// containing only what the program is allowed to see, and the binary to run.
//
// The environment is built up rather than inherited, so a variable the spec
// reads -- HOME, XDG_CONFIG_HOME, MACKUP_CONFIG -- is never leaked in from the
// developer's shell and cannot make a case pass on one machine and fail on
// another.
type World struct {
	t *testing.T

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
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("creating the home directory: %v", err)
	}
	return &World{
		t:    t,
		Root: root,
		Home: home,
		bin:  mackupBin,
		env:  map[string]string{"HOME": home, "PATH": os.Getenv("PATH")},
	}
}

// UseBinary switches this world to another of the builds under test.
func (w *World) UseBinary(path string) { w.bin = path }

// UseStampedBinary switches this world to the release build, which carries a
// version stamp as an installed build does.
func (w *World) UseStampedBinary() { w.bin = mackupStampedBin }

// Setenv sets an environment variable for every command this world runs.
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

// Mkdir creates a home-relative directory.
func (w *World) Mkdir(relative string) string {
	w.t.Helper()
	path := w.Path(relative)
	if err := os.MkdirAll(path, 0o700); err != nil {
		w.t.Fatalf("creating %s: %v", relative, err)
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
		t:    w.t,
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
	t    *testing.T
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
	r.t.Helper()
	if r.ExitCode != code {
		r.t.Errorf("%s exited %d, want %d\nstdout: %q\nstderr: %q", r.invocation(), r.ExitCode, code, r.Stdout, r.Stderr)
	}
	return r
}

// ExpectStdout asserts stdout contains want. appspec/07 makes the stream a
// message lands on contract, so every message assertion names its stream.
func (r Result) ExpectStdout(want string) Result {
	r.t.Helper()
	if !strings.Contains(r.Stdout, want) {
		r.t.Errorf("%s stdout does not contain %q\nstdout: %q", r.invocation(), want, r.Stdout)
	}
	return r
}

// ExpectStderr asserts stderr contains want.
func (r Result) ExpectStderr(want string) Result {
	r.t.Helper()
	if !strings.Contains(r.Stderr, want) {
		r.t.Errorf("%s stderr does not contain %q\nstderr: %q", r.invocation(), want, r.Stderr)
	}
	return r
}

// ExpectStderrLine asserts stderr is exactly one line, equal to want. Used for
// the literal contract tokens of appspec/07.
func (r Result) ExpectStderrLine(want string) Result {
	r.t.Helper()
	if r.Stderr != want+"\n" {
		r.t.Errorf("%s stderr = %q, want exactly %q", r.invocation(), r.Stderr, want+"\n")
	}
	return r
}

// ExpectSilentStdout asserts nothing was written to stdout. Both config-failure
// regimes of appspec/01 section 6 share the post-condition "no stdout".
func (r Result) ExpectSilentStdout() Result {
	r.t.Helper()
	if r.Stdout != "" {
		r.t.Errorf("%s wrote to stdout, want nothing\nstdout: %q", r.invocation(), r.Stdout)
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
	r.t.Helper()
	return r.ExpectExit(1).
		ExpectStderrLine("Error: " + cmd + " is not implemented yet.").
		ExpectSilentStdout()
}

// ExpectSilentStderr asserts nothing was written to stderr.
func (r Result) ExpectSilentStderr() Result {
	r.t.Helper()
	if r.Stderr != "" {
		r.t.Errorf("%s wrote to stderr, want nothing\nstderr: %q", r.invocation(), r.Stderr)
	}
	return r
}

// Snapshot records every path under the world's scratch root with its content
// and mode, so a case can assert what a command did or did not change.
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
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(w.Root, path)
		if err != nil {
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
		default:
			content, err := os.ReadFile(path)
			if err != nil {
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
