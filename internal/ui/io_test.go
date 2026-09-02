package ui

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// failingWriter fails every write, the way a write to a full disk does.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWriteErrorIsNilWhenEveryMessageIsDelivered(t *testing.T) {
	var out, errOut strings.Builder
	streams := &IO{In: strings.NewReader(""), Out: &out, Err: &errOut}
	streams.Outln("progress")
	streams.Errf("Error: %s\n", "boom")
	if err := streams.WriteError(); err != nil {
		t.Errorf("WriteError() = %v, want nil", err)
	}
	if out.String() != "progress\n" || errOut.String() != "Error: boom\n" {
		t.Errorf("streams = (%q, %q), want the two messages routed separately", out.String(), errOut.String())
	}
}

func TestWriteErrorKeepsTheFirstFailure(t *testing.T) {
	first := errors.New("no space left on device")
	streams := &IO{In: strings.NewReader(""), Out: failingWriter{first}, Err: failingWriter{errors.New("second")}}
	streams.Outln("this never lands")
	streams.Errln("neither does this")
	if !errors.Is(streams.WriteError(), first) {
		t.Errorf("WriteError() = %v, want the first failure %v", streams.WriteError(), first)
	}
}

func TestWriteErrorTracksEitherStream(t *testing.T) {
	boom := errors.New("boom")
	streams := &IO{In: strings.NewReader(""), Out: io.Discard, Err: failingWriter{boom}}
	streams.Outln("delivered")
	if streams.WriteError() != nil {
		t.Fatalf("WriteError() = %v after a good stdout write, want nil", streams.WriteError())
	}
	streams.Errln("lost")
	if !errors.Is(streams.WriteError(), boom) {
		t.Errorf("WriteError() = %v, want the stderr failure", streams.WriteError())
	}
}
