package httpserver

import (
	"strconv"
	"strings"
	"time"
)

var faDigits = strings.NewReplacer("0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴", "5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹")

// localTime shows a stored RFC 3339 UTC timestamp in the machine's local zone (B8); anything unparsable is shown as is.
func localTime(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
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
