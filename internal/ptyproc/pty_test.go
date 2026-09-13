package ptyproc

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestReadUntilPreservesOutputAfterReadTimeouts(t *testing.T) {
	p, err := Start([]string{"sh", "-c", "sleep 0.5; printf __DELAYED_MARKER__"}, ".", os.Environ(), 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Terminate()

	data, err := p.ReadUntil([]byte("__DELAYED_MARKER__"), 2*time.Second)
	if err != nil {
		t.Fatalf("delayed PTY output was lost: %v", err)
	}
	if !bytes.Contains(data, []byte("__DELAYED_MARKER__")) {
		t.Fatalf("marker missing from %q", data)
	}
}
