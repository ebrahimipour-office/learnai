package fsx

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"syscall"
)

// writeSyncHook is a testing hook for simulating write failures before linking (CR-07).
var writeSyncHook = writeSyncClose

// SetWriteSyncHookForTest sets a custom write function for testing crash scenarios (CR-07).
func SetWriteSyncHookForTest(hook func(f *os.File, data []byte) error) (cleanup func()) {
	orig := writeSyncHook
	writeSyncHook = hook
	return func() {
		writeSyncHook = orig
	}
}

// WriteNew creates a new file at relPath with crash-safe never-overwrite semantics (CR-07, D7).
// It writes data to a temporary file, fsyncs it, closes it, and hard-links it to the final name.
// If the target file already exists, it returns ErrFileExists and guarantees the existing file
// remains untouched. If the platform refuses hard links, it falls back to direct O_EXCL write.
func (r *Root) WriteNew(relPath string, data []byte, perm os.FileMode) error {
	if err := ValidateRelPath(relPath); err != nil {
		return err
	}

	cleanRel := path.Clean(relPath)
	dir := path.Dir(cleanRel)
	if dir != "." && dir != "/" {
		if err := r.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating parent directories for %s: %w", cleanRel, err)
		}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrRootClosed
	}

	// Generate random temporary filename in the same directory (CR-07)
	var randBytes [8]byte
	if _, err := io.ReadFull(rand.Reader, randBytes[:]); err != nil {
		return fmt.Errorf("generating temp filename for %s: %w", cleanRel, err)
	}
	tmpName := path.Join(dir, fmt.Sprintf(".%s.tmp-%s", path.Base(cleanRel), hex.EncodeToString(randBytes[:])))

	f, err := r.rawRoot.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("creating temp file %s: %w", tmpName, err)
	}

	if err := writeSyncHook(f, data); err != nil {
		_ = r.rawRoot.Remove(tmpName)
		return fmt.Errorf("writing data to %s: %w", cleanRel, err)
	}

	// Link temp to target (atomic creation with hard link)
	err = r.rawRoot.Link(tmpName, cleanRel)
	_ = r.rawRoot.Remove(tmpName) // temp is no longer needed whether link succeeds or fails

	if err == nil {
		syncDir(r.rawRoot, dir)
		return nil
	}

	if errors.Is(err, fs.ErrExist) || os.IsExist(err) {
		return fmt.Errorf("%w: %s", ErrFileExists, cleanRel)
	}

	// shortcut: platform or filesystem refuses hard links; fall back to direct O_EXCL write (CR-07)
	if isLinkUnsupported(err) {
		return r.writeNewDirect(cleanRel, data, perm)
	}

	return fmt.Errorf("linking %s to %s: %w", tmpName, cleanRel, err)
}

func (r *Root) writeNewDirect(cleanRel string, data []byte, perm os.FileMode) error {
	f, err := r.rawRoot.OpenFile(cleanRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		if errors.Is(err, fs.ErrExist) || os.IsExist(err) {
			return fmt.Errorf("%w: %s", ErrFileExists, cleanRel)
		}
		return fmt.Errorf("opening new file %s: %w", cleanRel, err)
	}
	if err := writeSyncHook(f, data); err != nil {
		_ = r.rawRoot.Remove(cleanRel)
		return fmt.Errorf("writing data to %s: %w", cleanRel, err)
	}
	dir := path.Dir(cleanRel)
	syncDir(r.rawRoot, dir)
	return nil
}

func isLinkUnsupported(err error) bool {
	if errors.Is(err, errors.ErrUnsupported) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ENOSYS, syscall.EPERM:
			return true
		case 0x32 /* ERROR_NOT_SUPPORTED */, 0x1 /* ERROR_INVALID_FUNCTION */:
			return true
		}
	}
	return false
}

func syncDir(raw *os.Root, dir string) {
	if dir == "" || dir == "/" {
		dir = "."
	}
	f, err := raw.Open(dir)
	if err != nil {
		return
	}
	defer f.Close()
	_ = f.Sync()
}

// writeSyncClose writes data, flushes it to disk, and closes f, returning the first error.
func writeSyncClose(f *os.File, data []byte) error {
	_, err := f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// WriteAtomic writes data to a temporary file in the same directory and atomically renames it.
// This is used ONLY where atomic replacement is explicitly specified (e.g., updating lesson.md
// AFTER its previous version was copied to history, or writing configuration/drafts).
func (r *Root) WriteAtomic(relPath string, data []byte, perm os.FileMode) error {
	if err := ValidateRelPath(relPath); err != nil {
		return err
	}

	cleanRel := path.Clean(relPath)
	dir := path.Dir(cleanRel)
	if dir != "." && dir != "/" {
		if err := r.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating parent directories: %w", err)
		}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrRootClosed
	}

	// Generate random temporary filename in the same directory
	var randBytes [8]byte
	if _, err := io.ReadFull(rand.Reader, randBytes[:]); err != nil {
		return fmt.Errorf("generating temp filename: %w", err)
	}
	tmpName := path.Join(dir, fmt.Sprintf(".tmp-%s-%s", path.Base(cleanRel), hex.EncodeToString(randBytes[:])))

	f, err := r.rawRoot.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("creating temp file %s: %w", tmpName, err)
	}

	if writeErr := writeSyncClose(f, data); writeErr != nil {
		_ = r.rawRoot.Remove(tmpName)
		return fmt.Errorf("writing to temp file: %w", writeErr)
	}

	// Atomic rename
	if err := r.rawRoot.Rename(tmpName, cleanRel); err != nil {
		_ = r.rawRoot.Remove(tmpName)
		return fmt.Errorf("renaming %s to %s: %w", tmpName, cleanRel, err)
	}

	syncDir(r.rawRoot, dir)
	return nil
}
