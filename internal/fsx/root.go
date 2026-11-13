package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

var (
	ErrFileExists          = errors.New("file already exists: cannot overwrite protected file")
	ErrRootClosed          = errors.New("root directory handle is closed")
	ErrSymlinkNotAllowed   = errors.New("symlink or reparse point is not allowed")
	ErrRemoveAllRestricted = errors.New("RemoveAll is restricted to paths under .app/work/, .app/staging/, or .app/logs/")
)

var allowedRemoveAllPrefixes = []string{
	".app/work/",
	".app/staging/",
	".app/logs/",
}

func isAllowedRemoveAll(clean string) bool {
	for _, p := range allowedRemoveAllPrefixes {
		if strings.HasPrefix(clean, p) && len(clean) > len(p) {
			return true
		}
	}
	return false
}

// Root wraps Go 1.24+ os.Root to provide hardened, secure file access inside the base directory (D12).
type Root struct {
	baseDir string
	rawRoot *os.Root
	mu      sync.RWMutex
	closed  bool
}

// OpenRoot initializes and locks onto a base directory using os.OpenRoot.
func OpenRoot(baseDir string) (*Root, error) {
	absDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving absolute path: %w", err)
	}

	if err := os.MkdirAll(absDir, 0755); err != nil {
		return nil, fmt.Errorf("ensuring base directory exists: %w", err)
	}

	raw, err := os.OpenRoot(absDir)
	if err != nil {
		return nil, fmt.Errorf("opening os.Root on %s: %w", absDir, err)
	}

	return &Root{
		baseDir: absDir,
		rawRoot: raw,
	}, nil
}

// Close closes the underlying os.Root handle.
func (r *Root) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.rawRoot.Close()
}

// BaseDir returns the absolute path of the base directory.
func (r *Root) BaseDir() string {
	return r.baseDir
}

// FS returns an fs.FS view of the root.
func (r *Root) FS() fs.FS {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rawRoot.FS()
}

// ReadFile reads the full contents of relPath after validating path safety.
func (r *Root) ReadFile(relPath string) ([]byte, error) {
	if err := ValidateRelPath(relPath); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, ErrRootClosed
	}

	return r.rawRoot.ReadFile(path.Clean(relPath))
}

// Stat returns FileInfo for relPath.
func (r *Root) Stat(relPath string) (os.FileInfo, error) {
	if err := ValidateRelPath(relPath); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, ErrRootClosed
	}

	return r.rawRoot.Stat(path.Clean(relPath))
}

// Lstat returns FileInfo for relPath without following symbolic links.
func (r *Root) Lstat(relPath string) (os.FileInfo, error) {
	if err := ValidateRelPath(relPath); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, ErrRootClosed
	}

	return r.rawRoot.Lstat(path.Clean(relPath))
}

// EnsureRealDir verifies that each path component of relPath exists or will exist as a real
// directory without following symlinks or Windows junctions/reparse points.
func (r *Root) EnsureRealDir(relPath string) error {
	if err := ValidateRelPath(relPath); err != nil {
		return err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrRootClosed
	}

	clean := path.Clean(relPath)
	if clean == "." || clean == "" {
		return nil
	}

	parts := strings.Split(clean, "/")
	current := ""
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if current == "" {
			current = p
		} else {
			current = current + "/" + p
		}

		fi, err := r.rawRoot.Lstat(current)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
				break
			}
			return err
		}

		mode := fi.Mode()
		if mode&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fmt.Errorf("%w: %s", ErrSymlinkNotAllowed, current)
		}
		if !fi.IsDir() {
			return fmt.Errorf("path component is not a directory: %s", current)
		}
	}

	return nil
}

// Exists checks whether a file or directory exists at relPath.
func (r *Root) Exists(relPath string) bool {
	_, err := r.Stat(relPath)
	return err == nil
}

// MkdirAll creates a directory and any necessary parents beneath the root, ensuring no
// path components are symlinks or reparse points.
func (r *Root) MkdirAll(relPath string, perm os.FileMode) error {
	if err := ValidateRelPath(relPath); err != nil {
		return err
	}

	if err := r.EnsureRealDir(relPath); err != nil {
		return err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrRootClosed
	}

	return r.rawRoot.MkdirAll(path.Clean(relPath), perm)
}

// Open opens an existing file for reading within the root.
func (r *Root) Open(relPath string) (*os.File, error) {
	if err := ValidateRelPath(relPath); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, ErrRootClosed
	}

	return r.rawRoot.Open(path.Clean(relPath))
}

// OpenFile opens a file within the root with specified flags.
func (r *Root) OpenFile(relPath string, flag int, perm os.FileMode) (*os.File, error) {
	if err := ValidateRelPath(relPath); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, ErrRootClosed
	}

	return r.rawRoot.OpenFile(path.Clean(relPath), flag, perm)
}

// Remove removes a file or empty directory within the root.
func (r *Root) Remove(relPath string) error {
	if err := ValidateRelPath(relPath); err != nil {
		return err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrRootClosed
	}

	return r.rawRoot.Remove(path.Clean(relPath))
}

// RemoveAll removes relPath and everything under it; restricted to allowed scratch paths
// (.app/work/, .app/staging/, .app/logs/). Refuses to follow symlinks or reparse points.
func (r *Root) RemoveAll(relPath string) error {
	if err := ValidateRelPath(relPath); err != nil {
		return err
	}

	clean := path.Clean(relPath)
	if !isAllowedRemoveAll(clean) {
		return fmt.Errorf("%w: %s", ErrRemoveAllRestricted, clean)
	}

	if err := r.EnsureRealDir(clean); err != nil {
		return err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrRootClosed
	}

	return r.rawRoot.RemoveAll(clean)
}

// HashFile calculates the SHA-256 hash and byte length of a file within the root.
func (r *Root) HashFile(relPath string) (string, int64, error) {
	f, err := r.Open(relPath)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	return HashReader(f)
}
