package fsx

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	ErrInvalidPath       = errors.New("invalid path: contains forbidden patterns or characters")
	ErrPathEscapesRoot   = errors.New("path escapes root directory")
	ErrReservedName      = errors.New("name is a reserved Windows device name")
	ErrPathTooLong       = errors.New("path exceeds maximum allowed length of 220 characters")
	ErrForbiddenExtension = errors.New("file extension is not permitted")
	ErrEmptyName         = errors.New("filename or slug cannot be empty")
)

// Windows reserved device names (case-insensitive)
var windowsReservedNames = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {},
	"COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {},
	"LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// Dangerous executable extensions that are unconditionally forbidden (Section 8.2)
var dangerousExtensions = map[string]struct{}{
	".exe": {}, ".bat": {}, ".cmd": {}, ".ps1": {}, ".psm1": {},
	".lnk": {}, ".scr": {}, ".com": {}, ".msi": {}, ".vbs": {},
	".vbe": {}, ".wsf": {}, ".wsh": {}, ".cpl": {}, ".reg": {},
	".pif": {}, ".hta": {}, ".jar": {}, ".app": {},
}

// Default allowed extensions for exercises/answers
var DefaultAllowedExtensions = []string{
	".go", ".py", ".js", ".ts", ".java", ".c", ".cpp", ".rs",
	".sql", ".sh", ".md", ".txt", ".json", ".yaml", ".yml",
}

var validSegmentRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// SanitizeSlug converts input text into a clean, safe slug (R5.2).
// Normalizes to Unicode NFC, lowercases, replaces non-alphanumeric chars with '-',
// enforces max length, and verifies no Windows reserved names.
func SanitizeSlug(s string) (string, error) {
	if s == "" {
		return "", ErrEmptyName
	}

	// 1. Unicode NFC normalization
	s = norm.NFC.String(s)

	// 2. Reject colons (NTFS Alternate Data Streams) and backslashes
	if strings.ContainsAny(s, `:\/<>|"?*`) {
		// Filter out or sanitize characters
	}

	var sb strings.Builder
	lastDash := false

	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' {
			sb.WriteRune(r)
			lastDash = false
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			// Non-latin letters/digits can be transcribed or replaced
			sb.WriteRune(r)
			lastDash = false
		} else {
			if !lastDash && sb.Len() > 0 {
				sb.WriteByte('-')
				lastDash = true
			}
		}
	}

	res := strings.Trim(sb.String(), "-. ")
	if res == "" {
		return "", ErrEmptyName
	}

	// Truncate to maximum 64 chars for slug
	if utf8.RuneCountInString(res) > 64 {
		runes := []rune(res)
		res = strings.Trim(string(runes[:64]), "-. ")
	}

	// Check reserved Windows name on base
	base := strings.ToUpper(res)
	if dot := strings.IndexByte(base, '.'); dot != -1 {
		base = base[:dot]
	}
	if _, ok := windowsReservedNames[base]; ok {
		return "", fmt.Errorf("%w: %s", ErrReservedName, res)
	}

	return res, nil
}

// ValidateRelPath validates that a relative path conforms strictly to safety guidelines (D12, Section 8.2).
// Rejects: absolute paths, drive letters, backslashes, UNC, "..", colons, and paths > 220 characters.
func ValidateRelPath(relPath string) error {
	if relPath == "" {
		return ErrInvalidPath
	}

	if len(relPath) > 220 {
		return ErrPathTooLong
	}

	// Reject backslashes; input relative paths must ALWAYS use '/'
	if strings.ContainsRune(relPath, '\\') {
		return fmt.Errorf("%w: backslashes not allowed in relative path", ErrInvalidPath)
	}

	// Reject drive letters like "C:" or NTFS stream colons
	if strings.ContainsRune(relPath, ':') {
		return fmt.Errorf("%w: colons not allowed in relative path", ErrInvalidPath)
	}

	// Reject absolute paths
	if strings.HasPrefix(relPath, "/") {
		return fmt.Errorf("%w: path must be relative, got leading slash", ErrInvalidPath)
	}

	clean := path.Clean(relPath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ErrPathEscapesRoot
	}

	parts := strings.Split(clean, "/")
	for _, part := range parts {
		if err := ValidateSegment(part); err != nil {
			return err
		}
	}

	return nil
}

// ValidateSegment ensures a single path component satisfies Windows & security rules.
func ValidateSegment(part string) error {
	if part == "" || part == "." {
		return ErrInvalidPath
	}
	if part == ".." {
		return ErrPathEscapesRoot
	}

	// Windows reserved names check
	upperPart := strings.ToUpper(part)
	baseName := upperPart
	if dot := strings.IndexByte(baseName, '.'); dot != -1 {
		baseName = baseName[:dot]
	}
	if _, ok := windowsReservedNames[baseName]; ok {
		return fmt.Errorf("%w: %s", ErrReservedName, part)
	}

	// No trailing dot or space (Windows filesystem forbids this)
	if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
		return fmt.Errorf("%w: segment cannot end with dot or space", ErrInvalidPath)
	}

	// Disallow dangerous control characters and NTFS stream indicators
	for _, ch := range part {
		if ch < 32 || ch == 127 || strings.ContainsRune(`<>:"/\|?*`, ch) {
			return fmt.Errorf("%w: invalid character in segment '%s'", ErrInvalidPath, part)
		}
	}

	return nil
}

// ValidateExtension checks that ext is an acceptable, non-executable extension.
func ValidateExtension(ext string, allowed []string) error {
	lower := strings.ToLower(ext)
	if !strings.HasPrefix(lower, ".") {
		lower = "." + lower
	}

	// 1. Unconditional check against dangerous executable formats
	if _, dangerous := dangerousExtensions[lower]; dangerous {
		return fmt.Errorf("%w: %s is an executable extension", ErrForbiddenExtension, lower)
	}

	// 2. Whitelist check
	if len(allowed) > 0 {
		for _, a := range allowed {
			if strings.EqualFold(lower, a) {
				return nil
			}
		}
		return fmt.Errorf("%w: %s is not in allowed extensions list", ErrForbiddenExtension, lower)
	}

	return nil
}
