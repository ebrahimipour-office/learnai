package httpserver

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"learninghub/internal/services/topics"
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
	want := faDigits.Replace(time.Date(2026, 11, 8, 8, 23, 30, 0, time.UTC).Local().Format("2006-01-02 15:04"))
	if strings.ContainsAny(want, "0123456789") {
		t.Fatalf("want Persian digits, got %q", want)
	}
	if got := localTime(in, false); got != want {
		t.Errorf("localTime(%q) = %q, want %q", in, got, want)
	}
	if got := localTime("not a time", true); got != "not a time" {
		t.Errorf("unparsable input must pass through, got %q", got)
	}
}

// TestJalali: Nowruz, the last day of a common and of a leap Jalali year, and the Gregorian year boundary (O7).
func TestJalali(t *testing.T) {
	for _, c := range []struct{ g, j [3]int }{
		{[3]int{2025, 3, 21}, [3]int{1404, 1, 1}},
		{[3]int{2026, 3, 20}, [3]int{1404, 12, 29}},
		{[3]int{2025, 3, 20}, [3]int{1403, 12, 30}},
		{[3]int{2025, 12, 31}, [3]int{1404, 10, 10}},
		{[3]int{2026, 1, 1}, [3]int{1404, 10, 11}},
		{[3]int{2026, 11, 21}, [3]int{1405, 8, 30}},
	} {
		jy, jm, jd := jalali(c.g[0], c.g[1], c.g[2])
		if [3]int{jy, jm, jd} != c.j {
			t.Errorf("jalali(%v) = %d/%d/%d, want %v", c.g, jy, jm, jd, c.j)
		}
	}
	noon := time.Date(2025, 3, 21, 12, 0, 0, 0, time.Local).UTC().Format(time.RFC3339)
	if got, want := localTime(noon, true), "۱۴۰۴/۰۱/۰۱ ۱۲:۰۰"; got != want {
		t.Errorf("localTime jalali = %q, want %q", got, want)
	}
}

// TestScoreTrend: under two attempts no chart; otherwise one point per attempt, 0–100 on y 105–5, attempt 1 rightmost (T9.13).
func TestScoreTrend(t *testing.T) {
	if scoreTrend(nil) != nil || scoreTrend([]topics.Attempt{{No: 1, Score: 80}}) != nil {
		t.Fatal("fewer than two attempts must give no chart")
	}
	tr := scoreTrend([]topics.Attempt{{No: 3, Score: 100}, {No: 2, Score: 50}, {No: 1, Score: 0}}) // newest first
	want := []trendPoint{{"10.0", "5.0", 3, 100}, {"150.0", "55.0", 2, 50}, {"290.0", "105.0", 1, 0}}
	if !reflect.DeepEqual(tr.Points, want) || tr.Line != "10.0,5.0 150.0,55.0 290.0,105.0" {
		t.Errorf("got %+v %q", tr.Points, tr.Line)
	}
	if tr := scoreTrend([]topics.Attempt{{No: 2, Score: 120}, {No: 1, Score: -5}}); tr.Points[0].Y != "5.0" || tr.Points[1].Y != "105.0" {
		t.Errorf("out-of-range scores must clamp to the chart: %+v", tr.Points)
	}
}
