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
func snapshotWithinBound(t *testing.T, world *World) Snapshot {
	t.Helper()
	done := make(chan Snapshot, 1)
	// Snapshot reports its own errors through t, which the testing package
	// requires be done from the goroutine running the case. It does that with
	// Fatalf, which logs the message and then stops this goroutine rather than
	// the case -- so on that path nothing is ever sent and the timeout below
	// is what ends the wait, a full bound later. Both routes fail the case, so
	// neither is missed; what the message must not do is name a cause, since
	// it cannot tell the two apart. Verified that Fatalf's own message
	// survives from a spawned goroutine, so the real reason is on the line
	// above whenever there is one.
	go func() { done <- world.Snapshot() }()
	select {
	case snapshot := <-done:
		return snapshot
	case <-time.After(snapshotBound):
		t.Fatalf("Snapshot did not return within %s: either it opened a non-regular file and blocked, which is what this case exists to catch, or it failed on the harness error logged above", snapshotBound)
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
