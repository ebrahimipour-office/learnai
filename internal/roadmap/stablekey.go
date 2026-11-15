package roadmap

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Normalize folds cosmetic differences in a title (R2.3) so they do not change its stable key:
// NFC, Arabic ي/ى/ك → Persian ی/ک, kashida dropped, ZWNJ treated as a space, invisible format
// characters (unicode.Cf) dropped except ZWNJ, Persian/Arabic digits → ASCII, lower case, whitespace collapsed.
// Also used for topics.title_norm.
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
		case unicode.Is(unicode.Cf, r): // drop invisible format characters (ZWSP, ZWJ, LRM, RLM, BOM, etc.)
			return -1
		case r >= '۰' && r <= '۹':
			return '0' + r - '۰'
		case r >= '٠' && r <= '٩':
			return '0' + r - '٠'
		}
		return r
	}, norm.NFC.String(s))
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// NormalizeLegacy is the pre-CR-51 normalizer, kept so re-imports can match existing topics.
func NormalizeLegacy(s string) string {
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

// StableKey is the 16-hex-digit key (schema pattern ^[0-9a-f]{16}$) of a normalized path.
func StableKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

// stableKey is an unexported alias for backward compatibility.
func stableKey(path string) string {
	return StableKey(path)
}

// childPath returns the normalized path of title under parent ("" = top level). A path already in
// used gets the first free "#N" suffix (N ≥ 2), so duplicate titles under one parent get distinct
// keys in document order. Renaming a heading changes its path and therefore its key.
// next tracks the next suffix number to try for each base path to keep de-duplication linear O(1).
func childPath(parent, title string, used map[string]bool, next map[string]int) string {
	return buildChildPath(parent, Normalize(title), used, next)
}

// childPathLegacy returns the path normalized using NormalizeLegacy for backwards compatibility.
func childPathLegacy(parent, title string, used map[string]bool, next map[string]int) string {
	return buildChildPath(parent, NormalizeLegacy(title), used, next)
}

func buildChildPath(parent, normTitle string, used map[string]bool, next map[string]int) string {
	p := normTitle
	if parent != "" {
		p = parent + "\n" + p
	}
	if !used[p] {
		used[p] = true
		return p
	}
	base := p
	n := 2
	if next != nil {
		n = max(2, next[base])
	}
	for {
		p = base + "#" + strconv.Itoa(n)
		if !used[p] {
			break
		}
		n++
	}
	if next != nil {
		next[base] = n + 1
	}
	used[p] = true
	return p
}
