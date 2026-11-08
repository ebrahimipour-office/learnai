package httpserver

import (
	"testing"
	"time"
)

func TestFaDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		2*time.Minute + 57*time.Second: "۲ دقیقه و ۵۷ ثانیه",
		9 * time.Second:                "۹ ثانیه",
		time.Hour + 10*time.Second:     "۱ ساعت و ۱۰ ثانیه",
		0:                              "۰ ثانیه",
	} {
		if got := faDuration(d); got != want {
			t.Errorf("faDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestLocalTime(t *testing.T) {
	in := "2026-11-08T08:23:30Z"
	want := time.Date(2026, 11, 8, 8, 23, 30, 0, time.UTC).Local().Format("2006-01-02 15:04")
	if got := localTime(in); got != want {
		t.Errorf("localTime(%q) = %q, want %q", in, got, want)
	}
	if got := localTime("not a time"); got != "not a time" {
		t.Errorf("unparsable input must pass through, got %q", got)
	}
}
