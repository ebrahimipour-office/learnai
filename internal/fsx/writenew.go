package fsx

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
)

// WriteNew creates a new file at relPath with O_CREATE|O_EXCL|O_WRONLY (D7).
// If the target file already exists, it immediately returns ErrFileExists and guarantees
// the existing content remains untouched.
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

	// Open with O_EXCL to ensure atomic non-overwriting behavior
	f, err := r.rawRoot.OpenFile(cleanRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%w: %s", ErrFileExists, cleanRel)
		}
		return fmt.Errorf("opening new file %s: %w", cleanRel, err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		// Clean up the partially created file if writing fails
		_ = r.rawRoot.Remove(cleanRel)
		return fmt.Errorf("writing data to %s: %w", cleanRel, err)
	}

	return nil
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

	writeErr := func() error {
		defer f.Close()
		_, err := f.Write(data)
		return err
	}()

	if writeErr != nil {
		_ = r.rawRoot.Remove(tmpName)
		return fmt.Errorf("writing to temp file: %w", writeErr)
	}

	// Atomic rename
	if err := r.rawRoot.Rename(tmpName, cleanRel); err != nil {
		_ = r.rawRoot.Remove(tmpName)
		return fmt.Errorf("renaming %s to %s: %w", tmpName, cleanRel, err)
	}

	return nil
}
