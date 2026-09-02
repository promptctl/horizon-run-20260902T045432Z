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

// The two binaries under test. appspec/00-overview.md "Provenance" makes both
// halves of the version contract observable, so the suite builds both: the
// program as `make build` produces it (no version stamped, so it reports the
// fallback token) and as an installed build behaves (its own version stamped).
var (
	mackupBin        string
	mackupStampedBin string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "macklebox-conformance-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "conformance: %v\n", err)
		os.Exit(1)
	}
	// os.Exit does not run deferred functions, so the removal is spelled out
	// at every exit below rather than deferred; otherwise each run leaks two
	// binaries of a couple of megabytes into the temporary directory.
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "conformance: %v\n", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	mackupBin = filepath.Join(dir, "mackup")
	mackupStampedBin = filepath.Join(dir, "mackup-stamped")

	// The unstamped binary is built with VCS stamping forced on so that every
	// machine exercises the harder half of the provenance contract of
	// appspec/00-overview.md. -buildvcs defaults to auto and declines, without
	// saying so, when it cannot read the repository; a build it declined to
	// stamp reports the fallback token for the easy reason, and the case that
	// asserts the token cannot then fail. This suite passed on a developer
	// machine whose builds went unstamped while CI, whose checkout was
	// stamped, failed on exactly that assertion.
	//
	// Where the toolchain cannot stamp at all -- a source tarball with no
	// repository, where -buildvcs=true is a hard error -- the build is retried
	// unstamped rather than losing the whole suite.
	if err := build(mackupBin, "", forceVCSStamp); err != nil {
		if err := build(mackupBin, "", defaultVCSStamp); err != nil {
			fail(err)
		}
	}
	if err := build(mackupStampedBin, stampedVersion, defaultVCSStamp); err != nil {
		fail(err)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// Whether to force VCS stamping on a build; see its use in TestMain.
const (
	defaultVCSStamp = false
	forceVCSStamp   = true
)

func build(out, version string, forceVCS bool) error {
	args := []string{"build", "-o", out}
	if forceVCS {
		args = append(args, "-buildvcs=true")
	}
	if version != "" {
		args = append(args, "-ldflags", "-X github.com/promptctl/macklebox/internal/version.value="+version)
	}
	args = append(args, "./cmd/mackup")

	root, err := moduleRoot()
	if err != nil {
		return err
	}

	cmd := exec.Command("go", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building %s: %v\n%s", out, err, output)
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

// UseStampedBinary switches this world to the build that carries a version
// stamp, as an installed build does.
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
		t:        w.t,
		Args:     args,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: cmd.ProcessState.ExitCode(),
	}
}

// Environ is the environment the program is run with, as name=value strings.
// Exposed so a case can assert on the isolation itself rather than only on its
// consequences.
func (w *World) Environ() []string { return w.environ() }

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
	t        *testing.T
	Args     []string
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
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot[relative] = fmt.Sprintf("symlink %04o -> %s", info.Mode().Perm(), target)
		case entry.IsDir():
			snapshot[relative] = fmt.Sprintf("dir %04o", info.Mode().Perm())
		default:
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot[relative] = fmt.Sprintf("file %04o %q", info.Mode().Perm(), content)
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
