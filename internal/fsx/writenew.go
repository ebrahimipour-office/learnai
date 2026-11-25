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
	"regexp"
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
	top, err := r.mkdirParents(dir)
	if err != nil {
		return fmt.Errorf("creating parent directories for %s: %w", cleanRel, err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrRootClosed
	}

	tmpName, f, err := r.createTemp(dir, ".%s.tmp-%s", path.Base(cleanRel), perm)
	if err != nil {
		return err
	}

	if err := writeSyncHook(f, data); err != nil {
		_ = r.rawRoot.Remove(tmpName)
		return fmt.Errorf("writing data to %s: %w", cleanRel, err)
	}

	// Link temp to target (atomic creation with hard link)
	err = r.rawRoot.Link(tmpName, cleanRel)
	_ = r.rawRoot.Remove(tmpName) // temp is no longer needed whether link succeeds or fails

	if err == nil {
		r.syncNew(dir, top)
		return nil
	}

	if errors.Is(err, fs.ErrExist) || os.IsExist(err) {
		return fmt.Errorf("%w: %s", ErrFileExists, cleanRel)
	}

	// shortcut: platform or filesystem refuses hard links; fall back to direct O_EXCL write (CR-07)
	if isLinkUnsupported(err) {
		return r.writeNewDirect(cleanRel, top, data, perm)
	}

	return fmt.Errorf("linking %s to %s: %w", tmpName, cleanRel, err)
}

// writeNewDirect is the no-hard-link fallback: temp file, then rename, so a crash never leaves a
// partial file under the final name (T13.4).
func (r *Root) writeNewDirect(cleanRel, top string, data []byte, perm os.FileMode) error {
	dir := path.Dir(cleanRel)
	tmpName, f, err := r.createTemp(dir, ".%s.tmp-%s", path.Base(cleanRel), perm)
	if err != nil {
		return err
	}
	if err := writeSyncHook(f, data); err != nil {
		_ = r.rawRoot.Remove(tmpName)
		return fmt.Errorf("writing data to %s: %w", cleanRel, err)
	}
	// shortcut: check-then-rename, not atomic against a concurrent creator; fine for one local app.
	if _, err := r.rawRoot.Lstat(cleanRel); !errors.Is(err, fs.ErrNotExist) {
		_ = r.rawRoot.Remove(tmpName)
		if err == nil {
			return fmt.Errorf("%w: %s", ErrFileExists, cleanRel)
		}
		return fmt.Errorf("checking %s: %w", cleanRel, err)
	}
	if err := r.rawRoot.Rename(tmpName, cleanRel); err != nil {
		_ = r.rawRoot.Remove(tmpName)
		return fmt.Errorf("renaming %s to %s: %w", tmpName, cleanRel, err)
	}
	r.syncNew(dir, top)
	return nil
}

// createTemp creates a fresh O_EXCL temp file in dir named by format(base, randomHex). Caller holds r.mu.
func (r *Root) createTemp(dir, format, base string, perm os.FileMode) (string, *os.File, error) {
	var randBytes [8]byte
	if _, err := io.ReadFull(rand.Reader, randBytes[:]); err != nil {
		return "", nil, fmt.Errorf("generating temp filename for %s: %w", base, err)
	}
	tmpName := path.Join(dir, fmt.Sprintf(format, base, hex.EncodeToString(randBytes[:])))
	f, err := r.rawRoot.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return "", nil, fmt.Errorf("creating temp file %s: %w", tmpName, err)
	}
	return tmpName, f, nil
}

// tempName matches only the names WriteNew (.<base>.tmp-<hex>) and WriteAtomic (.tmp-<base>-<hex>) create.
var tempName = regexp.MustCompile(`^\.(tmp-.+-|.+\.tmp-)[0-9a-f]{16}$`)

// IsTempName reports whether a base name is one of this package's in-flight or crash-leftover temp files.
func IsTempName(name string) bool { return tempName.MatchString(name) }

// mkdirParents creates dir and returns its shallowest ancestor that did not exist before ("" if dir
// already existed), so syncNew can make every new directory entry durable (T13.4).
func (r *Root) mkdirParents(dir string) (string, error) {
	if dir == "." || dir == "/" {
		return "", nil
	}
	top := ""
	r.mu.RLock()
	for d := dir; d != "." && !r.closed; d = path.Dir(d) {
		if _, err := r.rawRoot.Lstat(d); !errors.Is(err, fs.ErrNotExist) {
			break
		}
		top = d
	}
	r.mu.RUnlock()
	return top, r.MkdirAll(dir, 0755)
}

// syncNew fsyncs dir (the new entry in it) and, for each directory created up to top, its parent.
func (r *Root) syncNew(dir, top string) {
	syncDir(r.rawRoot, dir)
	if top == "" {
		return
	}
	for d := dir; ; d = path.Dir(d) {
		syncDir(r.rawRoot, path.Dir(d))
		if d == top || d == "." {
			return
		}
	}
}

func isLinkUnsupported(err error) bool {
	if errors.Is(err, errors.ErrUnsupported) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		// Not a switch: on Linux EPERM == 1 collides with the Windows code below.
		return errno == syscall.ENOSYS || errno == syscall.EPERM ||
			errno == 0x32 /* ERROR_NOT_SUPPORTED */ || errno == 0x1 /* ERROR_INVALID_FUNCTION */
	}
	return false
}

// syncDirSpy, when set by a test, sees every directory fsync request (CR-34).
var syncDirSpy func(dir string)

// SetSyncDirSpyForTest registers a spy called with each directory that is fsynced (CR-34).
func SetSyncDirSpyForTest(spy func(dir string)) (cleanup func()) {
	syncDirSpy = spy
	return func() { syncDirSpy = nil }
}

// syncDir makes a create/rename in dir durable. Best effort: Windows cannot fsync a directory.
func syncDir(raw *os.Root, dir string) {
	if dir == "" || dir == "/" {
		dir = "."
	}
	if syncDirSpy != nil {
		syncDirSpy(dir)
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
	top, err := r.mkdirParents(dir)
	if err != nil {
		return fmt.Errorf("creating parent directories: %w", err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrRootClosed
	}

	tmpName, f, err := r.createTemp(dir, ".tmp-%s-%s", path.Base(cleanRel), perm)
	if err != nil {
		return err
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

	r.syncNew(dir, top)
	return nil
}
