//go:build conformance && unix

// Constrained to unix because it makes a FIFO. The rest of the suite is unix
// in practice too -- it shells out to make -- but only this file says so in a
// way the compiler enforces.

package conformance

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// snapshotBound is how long Snapshot gets before this case calls it hung. Far
// longer than a scratch root of a handful of entries needs, short enough that
// the answer arrives while someone is still watching.
const snapshotBound = 30 * time.Second

func TestTheSnapshotRecordsANonRegularFileWithoutOpeningIt(t *testing.T) {
	// A harness case, not a spec case: it pins the snapshot's own behavior,
	// which every ExpectUnchanged in the suite rests on.
	//
	// The path is the shape this actually turns up in. ~/.gnupg holds a socket
	// and an agent FIFO, this program walks home directories, and Snapshot
	// walks the whole scratch root -- so the first real fixture with a dotfile
	// directory in it would have met this, not a synthetic case.
	world := NewWorld(t)
	fifo := world.Path(".gnupg", "S.gpg-agent")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
		t.Fatalf("creating the .gnupg directory: %v", err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("creating a FIFO at %s: %v", fifo, err)
	}

	// Bounded, because the regression this guards against does not fail --
	// it hangs. Opening a FIFO no one is writing to blocks until a writer
	// arrives, so a Snapshot that opened this would stop here until the whole
	// binary timed out ten minutes later, reporting a deadlock in whichever
	// case happened to be running rather than a defect in Snapshot. A unix
	// socket, the other thing in that directory, takes the same branch and
	// fails differently: os.ReadFile returns an error and aborts the walk.
	before := snapshotWithinBound(t, world)

	key := world.SnapshotKey(".gnupg", "S.gpg-agent")
	got, ok := before[key]
	if !ok {
		t.Fatalf("snapshot has no entry for %s; it holds %v", key, snapshotPaths(before))
	}
	// Recorded by type. fs.FileMode renders a FIFO's type bits as "p" and the
	// rest as dashes, so this is the type the walk saw, not a name it guessed.
	if want := fs.ModeNamedPipe.String(); !strings.HasPrefix(got, want) {
		t.Errorf("snapshot recorded %s as %q, want it to begin with %q", key, got, want)
	}

	// And it still works as a snapshot: a run that changes nothing compares
	// equal, rather than the FIFO reading differently each time and every
	// ExpectUnchanged in a fixture like this reporting a phantom change.
	world.Run("--help").ExpectExit(0)
	world.ExpectUnchanged(before)
}

// snapshotWithinBound returns world.Snapshot(), failing the case if it has not
// returned within snapshotBound.
//
// The snapshot runs on a spawned goroutine and reports into a recorder rather
// than into t. Both reasons come down to the same rule: only the goroutine
// running a case may touch that case's *testing.T.
//
// It makes the diagnosis exact. Snapshot reports its own failures with Fatalf,
// which from a spawned goroutine logs and then stops that goroutine rather
// than the case -- so nothing was ever sent, the wait ran the full bound, and
// a harness error surfaced as "did not return", a cause the message could not
// tell apart from a real hang and so had to hedge between. The recorder
// carries that message back over the channel instead, and the case reports the
// actual reason immediately.
//
// And it makes the timeout safe. On the timeout path t.Fatalf ends the case,
// NewWorld's cleanups then chmod and remove the scratch root, and a snapshot
// goroutine still walking that tree would fail on the vanishing files and call
// Fatalf on a test that has completed -- which the testing package turns into
// an unrecovered panic, taking down the whole binary and losing every
// remaining case's result. A timeout would report itself as a crash somewhere
// else. With the recorder that goroutine cannot reach t at all: whatever it
// does after the bound, it does into an object nobody reads.
func snapshotWithinBound(t *testing.T, world *World) Snapshot {
	t.Helper()

	type outcome struct {
		snapshot Snapshot
		fatal    string
		failed   bool
	}
	// Buffered, so a goroutine that finishes after the bound has elapsed can
	// send and exit rather than blocking on a receive that will never come.
	done := make(chan outcome, 1)

	// A shallow copy: the snapshot shares the world's Root and reports into
	// the recorder. Only the reporter differs, and world itself is untouched,
	// since the case goes on using it on its own goroutine.
	recorder := &recordingReporter{}
	isolated := *world
	isolated.t = recorder

	go func() {
		// recordingReporter.Fatalf panics rather than returning, because the
		// real Fatalf does not return either. Caught here so a harness error
		// comes back as a message instead of killing the process.
		defer func() {
			if raised := recover(); raised != nil {
				fatal, ok := raised.(fatalFromRecorder)
				if !ok {
					panic(raised)
				}
				done <- outcome{fatal: string(fatal), failed: true}
			}
		}()
		done <- outcome{snapshot: isolated.Snapshot()}
	}()

	select {
	case got := <-done:
		if got.failed {
			t.Fatalf("the snapshot failed: %s", got.fatal)
		}
		// Snapshot reports only through Fatalf today. This is here so that a
		// later one reporting with Errorf is not swallowed by the recorder --
		// which would leave a snapshot that had complained looking clean.
		if len(recorder.messages) > 0 {
			t.Fatalf("the snapshot returned but reported %v", recorder.messages)
		}
		return got.snapshot
	case <-time.After(snapshotBound):
		// The message can name the cause now: a harness error takes the
		// branch above rather than expiring the bound.
		t.Fatalf("Snapshot did not return within %s, so it blocked rather than failed: opening a non-regular file is what this case exists to catch", snapshotBound)
		return nil
	}
}

// snapshotPaths lists what a snapshot holds, in a stable order, for a failure
// message that is worth reading.
func snapshotPaths(snapshot Snapshot) []string {
	paths := make([]string, 0, len(snapshot))
	for path := range snapshot {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
