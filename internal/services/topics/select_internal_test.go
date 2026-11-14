package topics

import (
	"strings"
	"testing"
)

// endless never returns EOF: an unbounded read would never finish.
type endless struct{ served int64 }

func (e *endless) Read(p []byte) (int, error) { e.served += int64(len(p)); return len(p), nil }

func TestReadCappedIsBounded(t *testing.T) {
	src := &endless{}
	b, over, err := readCapped(src, 100)
	if err != nil || !over || len(b) != 101 || src.served != 101 {
		t.Fatalf("len=%d over=%v served=%d err=%v", len(b), over, src.served, err)
	}
	if b, over, _ := readCapped(strings.NewReader("abc"), 3); over || string(b) != "abc" {
		t.Fatalf("at the cap: %q over=%v", b, over)
	}
}
