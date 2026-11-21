package topics

import (
	"testing"
	"time"
)

func TestNextBox(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		box     int
		knew    bool
		want    int
		wantDur time.Duration
	}{
		{1, true, 2, 2 * day}, {2, true, 3, 4 * day}, {4, true, 5, 16 * day},
		{5, true, 5, 16 * day}, // capped
		{3, false, 1, day}, {1, false, 1, day},
	} {
		if b, d := NextBox(c.box, c.knew); b != c.want || d != c.wantDur {
			t.Errorf("NextBox(%d, %v) = %d, %v; want %d, %v", c.box, c.knew, b, d, c.want, c.wantDur)
		}
	}
}
