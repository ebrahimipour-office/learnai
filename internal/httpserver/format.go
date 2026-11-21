package httpserver

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"learninghub/internal/services/topics"
)

var faDigits = strings.NewReplacer("0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴", "5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹")

// faSize spells a byte limit in Persian: whole megabytes as «۱ مگابایت», anything else in kilobytes.
func faSize(n int) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return faDigits.Replace(strconv.Itoa(n>>20)) + " مگابایت"
	}
	return faDigits.Replace(strconv.Itoa(n>>10)) + " کیلوبایت"
}

// localTime shows a stored RFC 3339 UTC timestamp in the machine's local zone (B8) in Persian digits,
// as a Jalali date when the display option is on (O7); anything unparsable is shown as is.
func localTime(s string, useJalali bool) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	t = t.Local()
	if !useJalali {
		return faDigits.Replace(t.Format("2006-01-02 15:04"))
	}
	jy, jm, jd := jalali(t.Year(), int(t.Month()), t.Day())
	return faDigits.Replace(fmt.Sprintf("%04d/%02d/%02d %s", jy, jm, jd, t.Format("15:04")))
}

// jalali converts a Gregorian date to the Jalali (Solar Hijri) calendar with the standard arithmetic algorithm (33-year cycles).
func jalali(gy, gm, gd int) (jy, jm, jd int) {
	cum := [...]int{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
	gy2 := gy
	if gm > 2 {
		gy2++
	}
	days := 355666 + 365*gy + (gy2+3)/4 - (gy2+99)/100 + (gy2+399)/400 + gd + cum[gm-1]
	jy = -1595 + 33*(days/12053)
	days %= 12053
	jy += 4 * (days / 1461)
	days %= 1461
	if days > 365 {
		jy += (days - 1) / 365
		days = (days - 1) % 365
	}
	if days < 186 {
		return jy, 1 + days/31, 1 + days%31
	}
	return jy, 7 + (days-186)/30, 1 + (days-186)%30
}

// faDuration spells d in Persian, e.g. 2m57s ⇒ «۲ دقیقه و ۵۷ ثانیه»; zero parts are left out.
func faDuration(d time.Duration) string {
	s := int64(d.Round(time.Second) / time.Second)
	var parts []string
	for _, u := range []struct {
		secs int64
		name string
	}{{3600, "ساعت"}, {60, "دقیقه"}, {1, "ثانیه"}} {
		if n := s / u.secs; n > 0 {
			parts = append(parts, faDigits.Replace(strconv.FormatInt(n, 10))+" "+u.name)
			s %= u.secs
		}
	}
	if len(parts) == 0 {
		return "۰ ثانیه"
	}
	return strings.Join(parts, " و ")
}

// trendPoint is one attempt on the score chart, in viewBox units.
type trendPoint struct {
	X, Y  string
	No    int64
	Score float64
}

// trend is the score chart: Line feeds an SVG polyline, Points its markers.
type trend struct {
	Points []trendPoint
	Line   string
}

// scoreTrend lays attempts (newest first, as topics.Attempts returns them) out in a 300×110 viewBox:
// score 0–100 maps to y 105–5, and time runs right to left so attempt 1 sits on the right (RTL).
// Fewer than two attempts ⇒ nil: one point is not a trend.
func scoreTrend(as []topics.Attempt) *trend {
	if len(as) < 2 {
		return nil
	}
	t := &trend{}
	step := 280 / float64(len(as)-1)
	var b strings.Builder
	for k, a := range as {
		s := min(max(a.Score, 0), 100)
		x, y := strconv.FormatFloat(10+float64(k)*step, 'f', 1, 64), strconv.FormatFloat(105-s, 'f', 1, 64)
		t.Points = append(t.Points, trendPoint{X: x, Y: y, No: a.No, Score: a.Score})
		if k > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(x + "," + y)
	}
	t.Line = b.String()
	return t
}
