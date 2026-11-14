// Package maintenance removes expired internal scratch files and backs up app.db (T6.5, TDD §8).
// It only ever touches .app/work, .app/staging, .app/logs/jobs, .app/backup and the app's own stale
// temp files (CR-34); the learner's files are never read or changed.
package maintenance

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"learninghub/internal/fsx"
	"learninghub/internal/store"
)

const (
	backupDir          = ".app/backup"
	defaultKeepBackups = 7
)

type Service struct {
	DB           *store.DB
	Root         *fsx.Root
	LogRetention time.Duration
	KeepBackups  int // config backup_keep; 0 = defaultKeepBackups
}

// Result says what one Run did.
type Result struct {
	Removed []string // relative dirs and files
	Backup  string   // relative path of the backup made; "" = today's already existed
}

// Run cleans expired job dirs, then makes today's backup and keeps the newest s.KeepBackups.
func (s *Service) Run(ctx context.Context, now time.Time) (*Result, error) {
	res := &Result{}
	cutoff := now.Add(-s.LogRetention).UTC().Format("2006-01-02T15:04:05.000Z") // same form as jobs.finished_at
	for _, dir := range []string{".app/work", ".app/staging", ".app/logs/jobs"} {
		if err := s.clean(ctx, dir, cutoff, res); err != nil {
			return res, err
		}
	}
	if err := s.sweepTmp(now, res); err != nil {
		return res, err
	}
	if err := s.backup(ctx, now, res); err != nil {
		return res, err
	}
	return res, nil
}

// tmpFile matches only the names fsx.WriteNew (.<base>.tmp-<hex>) and fsx.WriteAtomic (.tmp-<base>-<hex>) create.
var tmpFile = regexp.MustCompile(`^\.(tmp-.+-|.+\.tmp-)[0-9a-f]{16}$`)

// sweepTmp removes temp files a crash left behind; an hour is far longer than any write takes (CR-34).
func (s *Service) sweepTmp(now time.Time, res *Result) error {
	cutoff := now.Add(-time.Hour)
	return fs.WalkDir(s.Root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || !tmpFile.MatchString(d.Name()) {
			return nil // unreadable, symlink, directory or not ours
		}
		if info, err := d.Info(); err != nil || info.ModTime().After(cutoff) {
			return nil
		}
		if err := s.Root.Remove(p); err != nil {
			return fmt.Errorf("removing stale temp file %s: %w", p, err)
		}
		res.Removed = append(res.Removed, p)
		return nil
	})
}

// clean removes <dir>/<job id> unless its job is queued/running, or finished within the retention
// window and still needed: logs always are, work/staging only for a failed job (retry reuses them).
func (s *Service) clean(ctx context.Context, dir, cutoff string, res *Result) error {
	if err := s.Root.EnsureRealDir(dir); err != nil {
		return fmt.Errorf("checking maintenance dir %s: %w", dir, err)
	}
	entries, err := fs.ReadDir(s.Root.FS(), dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || !e.IsDir() {
			continue // not ours
		}
		rel := path.Join(dir, e.Name())
		if err := s.Root.EnsureRealDir(rel); err != nil {
			// Skip symlinked or irregular entries (CR-10)
			continue
		}
		var status, finished string
		err = s.DB.Writer().QueryRowContext(ctx, `SELECT status, COALESCE(finished_at, '') FROM jobs WHERE id = ?`, id).Scan(&status, &finished)
		switch {
		case errors.Is(err, sql.ErrNoRows): // orphan
		case err != nil:
			return err
		case status == "queued" || status == "running":
			continue
		case finished >= cutoff && (dir == ".app/logs/jobs" || status == "failed"):
			continue
		}
		if err := s.Root.RemoveAll(rel); err != nil {
			return fmt.Errorf("removing %s: %w", rel, err)
		}
		res.Removed = append(res.Removed, rel)
	}
	return nil
}

func (s *Service) backup(ctx context.Context, now time.Time, res *Result) error {
	if err := s.Root.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	rel := path.Join(backupDir, "app-"+now.UTC().Format("2006-01-02")+".db")
	if !s.Root.Exists(rel) {
		// VACUUM INTO refuses an existing file, so a backup is never overwritten.
		abs := filepath.Join(s.Root.BaseDir(), filepath.FromSlash(rel))
		if _, err := s.DB.Writer().ExecContext(ctx, `VACUUM INTO ?`, abs); err != nil {
			return fmt.Errorf("backing up database: %w", err)
		}
		res.Backup = rel
	}
	entries, err := fs.ReadDir(s.Root.FS(), backupDir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if n := e.Name(); e.Type().IsRegular() && strings.HasPrefix(n, "app-") && strings.HasSuffix(n, ".db") {
			names = append(names, n)
		}
	}
	slices.Sort(names) // dated names sort oldest first
	for _, n := range names[:max(0, len(names)-cmp.Or(s.KeepBackups, defaultKeepBackups))] {
		rel := path.Join(backupDir, n)
		if err := s.Root.Remove(rel); err != nil {
			return err
		}
		res.Removed = append(res.Removed, rel)
	}
	return nil
}
