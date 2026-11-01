package roadmap

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Normalize folds cosmetic differences in a title (R2.3) so they do not change its stable key:
// NFC, Arabic ي/ى/ك → Persian ی/ک, kashida dropped, ZWNJ treated as a space, Persian/Arabic
// digits → ASCII, lower case, whitespace collapsed. Also used for topics.title_norm.
func Normalize(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == 'ي' || r == 'ى':
			return 'ی'
		case r == 'ك':
			return 'ک'
		case r == 'ـ': // kashida (tatweel)
			return -1
		case r == '‌': // ZWNJ: "نوع‌ها" and "نوع ها" are the same title
			return ' '
		case r >= '۰' && r <= '۹':
			return '0' + r - '۰'
		case r >= '٠' && r <= '٩':
			return '0' + r - '٠'
		}
		return r
	}, norm.NFC.String(s))
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// stableKey is the 16-hex-digit key (schema pattern ^[0-9a-f]{16}$) of a normalized path.
func stableKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

// childPath returns the normalized path of title under parent ("" = top level). A path already in
// used gets the first free "#N" suffix (N ≥ 2), so duplicate titles under one parent get distinct
// keys in document order. Renaming a heading changes its path and therefore its key.
func childPath(parent, title string, used map[string]bool) string {
	p := Normalize(title)
	if parent != "" {
		p = parent + "\n" + p
	}
	base := p
	for n := 2; used[p]; n++ {
		p = base + "#" + strconv.Itoa(n)
	}
	used[p] = true
	return p
}
