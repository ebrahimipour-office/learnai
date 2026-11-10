package fsx_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"learninghub/internal/fsx"
)

func TestPathValidation(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid simple", "topics/01-intro/lesson.md", false},
		{"valid nested", "roadmaps/backend/rev-001.md", false},
		{"empty path", "", true},
		{"escapes with dot dot", "../foo/bar.txt", true},
		{"middle dot dot", "topics/../../secret.txt", true},
		{"absolute unix", "/etc/passwd", true},
		{"drive letter windows", "C:/Windows/System32", true},
		{"ntfs alternate data stream", "file.txt:hidden", true},
		{"backslash not allowed", `topics\01\lesson.md`, true},
		{"windows reserved name CON", "topics/CON/file.txt", true},
		{"windows reserved name file NUL.txt", "topics/NUL.txt", true},
		{"windows reserved name COM1", "COM1", true},
		{"windows reserved lowercase aux.go", "topics/aux.go", true},
		{"ends with dot", "topics/folder./file.txt", true},
		{"ends with space", "topics/folder /file.txt", true},
		{"path too long", strings.Repeat("a", 225), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fsx.ValidateRelPath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRelPath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestSanitizeSlug(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"Hello World", "hello-world", false},
		{"HTTP & Networking (v2)", "http-networking-v2", false},
		{"مبانی برنامه نویسی", "مبانی-برنامه-نویسی", false},
		{"CON", "", true},      // Windows reserved
		{"nul", "", true},      // Windows reserved
		{"COM3.txt", "", true}, // Windows reserved
		{"", "", true},
		{"---", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := fsx.SanitizeSlug(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SanitizeSlug(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("SanitizeSlug(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateExtension(t *testing.T) {
	// Forbidden executable extensions
	forbidden := []string{".exe", ".bat", ".cmd", ".ps1", ".lnk", ".vbs", ".msi", ".scr"}
	for _, ext := range forbidden {
		if err := fsx.ValidateExtension(ext, fsx.DefaultAllowedExtensions); err == nil {
			t.Errorf("ValidateExtension should reject dangerous extension %s", ext)
		}
	}

	// Allowed extensions
	allowed := []string{".go", ".py", ".ts", ".js", ".sql", ".md"}
	for _, ext := range allowed {
		if err := fsx.ValidateExtension(ext, fsx.DefaultAllowedExtensions); err != nil {
			t.Errorf("ValidateExtension should accept %s, got: %v", ext, err)
		}
	}

	// Unknown extension
	if err := fsx.ValidateExtension(".unknownext", fsx.DefaultAllowedExtensions); err == nil {
		t.Errorf("ValidateExtension should reject extension not in allowed list")
	}
}

func TestRoot_WriteNew_Immutability(t *testing.T) {
	tempDir := t.TempDir()

	root, err := fsx.OpenRoot(tempDir)
	if err != nil {
		t.Fatalf("OpenRoot failed: %v", err)
	}
	defer root.Close()

	relPath := "answers/v1/q01-hello.go"
	originalContent := []byte("package main\n\nfunc main() {}\n")

	// 1. First WriteNew should succeed
	if err := root.WriteNew(relPath, originalContent, 0644); err != nil {
		t.Fatalf("WriteNew first call failed: %v", err)
	}

	// 2. Second WriteNew with same path MUST fail with ErrFileExists
	newContent := []byte("MALICIOUS OVERWRITE ATTEMPT")
	err = root.WriteNew(relPath, newContent, 0644)
	if !errors.Is(err, fsx.ErrFileExists) {
		t.Fatalf("WriteNew second call expected ErrFileExists, got: %v", err)
	}

	// 3. Verify original content is completely intact
	readBack, err := root.ReadFile(relPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(readBack) != string(originalContent) {
		t.Fatalf("Original content was modified! got: %s, want: %s", string(readBack), string(originalContent))
	}
}

func TestRoot_WriteAtomic(t *testing.T) {
	tempDir := t.TempDir()

	root, err := fsx.OpenRoot(tempDir)
	if err != nil {
		t.Fatalf("OpenRoot failed: %v", err)
	}
	defer root.Close()

	relPath := "lesson/lesson.md"
	v1 := []byte("# Version 1")
	v2 := []byte("# Version 2 Updated")

	if err := root.WriteAtomic(relPath, v1, 0644); err != nil {
		t.Fatalf("WriteAtomic v1 failed: %v", err)
	}

	if err := root.WriteAtomic(relPath, v2, 0644); err != nil {
		t.Fatalf("WriteAtomic v2 failed: %v", err)
	}

	read, err := root.ReadFile(relPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(read) != string(v2) {
		t.Errorf("WriteAtomic read = %s, want %s", string(read), string(v2))
	}
}

func TestAnswerTemplate_GenerationAndExtraction(t *testing.T) {
	stableKey := "a1b2c3d4e5f60718"
	setVersion := 1
	exID := "q01"
	title := "Write a simple HTTP handler"
	starter := "func helloHandler(w http.ResponseWriter, r *http.Request) {\n\t// TODO\n}"

	tpl := fsx.GenerateAnswerTemplate(stableKey, setVersion, exID, title, ".go", starter)
	tplStr := string(tpl)

	if !strings.Contains(tplStr, "@hub:topic="+stableKey) {
		t.Errorf("Template missing @hub topic tag")
	}
	if !strings.Contains(tplStr, fsx.AnswerMarker) {
		t.Errorf("Template missing answer marker")
	}
	if !strings.Contains(tplStr, starter) {
		t.Errorf("Template missing starter code")
	}

	// Test student answering below the marker
	studentCode := "fmt.Fprintf(w, \"Hello World\")"
	fileWithStudentAnswer := tplStr + "\n" + studentCode

	extracted := fsx.ExtractAnswerContent([]byte(fileWithStudentAnswer))
	if !strings.Contains(extracted, studentCode) {
		t.Errorf("ExtractAnswerContent failed to extract student code, got: %s", extracted)
	}
}

func TestHashing(t *testing.T) {
	data := "hello world"
	// known SHA-256 for "hello world"
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	if got := fsx.HashString(data); got != expected {
		t.Errorf("HashString(%q) = %q, want %q", data, got, expected)
	}

	tempDir := t.TempDir()
	root, err := fsx.OpenRoot(tempDir)
	if err != nil {
		t.Fatalf("OpenRoot failed: %v", err)
	}
	defer root.Close()

	if err := root.WriteNew("test.txt", []byte(data), 0644); err != nil {
		t.Fatalf("WriteNew failed: %v", err)
	}

	hash, size, err := root.HashFile("test.txt")
	if err != nil {
		t.Fatalf("HashFile failed: %v", err)
	}
	if hash != expected || size != int64(len(data)) {
		t.Errorf("HashFile = (%q, %d), want (%q, %d)", hash, size, expected, len(data))
	}
}

func TestRoot_PathTraversalBlocked(t *testing.T) {
	tempDir := t.TempDir()
	root, err := fsx.OpenRoot(tempDir)
	if err != nil {
		t.Fatalf("OpenRoot failed: %v", err)
	}
	defer root.Close()

	// Try reading outside baseDir
	outsideFile := filepath.Join(tempDir, "..", "secret.txt")
	_ = os.WriteFile(outsideFile, []byte("secret"), 0644)
	defer os.Remove(outsideFile)

	_, err = root.ReadFile("../secret.txt")
	if err == nil {
		t.Fatalf("Expected error when attempting path traversal, got nil")
	}
}

func TestAnswerTemplate_EncodingAndCommentStyle(t *testing.T) {
	want := map[string]string{".go": "// ", ".py": "# ", ".sql": "-- ", ".md": "<!-- ", ".sh": "# "}
	for _, ext := range fsx.DefaultAllowedExtensions {
		tpl := fsx.GenerateAnswerTemplate("k", 1, "q01", "t --> x", ext, "\uFEFFa\r\nb\rc")
		s := string(tpl)
		if strings.HasPrefix(s, "\uFEFF") || strings.Contains(s, "\uFEFF") || strings.Contains(s, "\r") {
			t.Errorf("%s: BOM or CR in template %q", ext, s)
		}
		if !strings.HasSuffix(s, "a\nb\nc\n") {
			t.Errorf("%s: starter not normalized: %q", ext, s)
		}
		if p, ok := want[ext]; ok && !strings.HasPrefix(s, p+"@hub:topic=k,set=v1,ex=q01") {
			t.Errorf("%s: header %q", ext, strings.SplitN(s, "\n", 2)[0])
		}
		if ext == ".md" && strings.Count(s, "-->") != 4 {
			t.Errorf(".md: title closed the comment early: %q", s)
		}
	}
}

func TestReservedNames(t *testing.T) {
	for _, name := range []string{"con", "CONIN$", "conout$.txt", "com0", "LPT0", "com¹", "lpt³.md", "nul.tar.gz"} {
		if err := fsx.ValidateSegment(name); !errors.Is(err, fsx.ErrReservedName) {
			t.Errorf("ValidateSegment(%q) = %v, want ErrReservedName", name, err)
		}
	}
	for _, name := range []string{"console", "com10", "lpt", "conin"} {
		if err := fsx.ValidateSegment(name); err != nil {
			t.Errorf("ValidateSegment(%q) = %v, want nil", name, err)
		}
	}
}
