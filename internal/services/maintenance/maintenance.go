// Package maintenance removes expired internal scratch files and backs up app.db (T6.5, TDD §8).
// It only ever touches .app/work, .app/staging, .app/logs/jobs, .app/backup and the app's own stale
// temp files under topics/, roadmaps/ and .app/ (CR-34, T10.7); the learner's files are never read or changed.
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
	"sync"
	"time"

	"learninghub/internal/fsx"
	"learninghub/internal/store"
)

const (
	backupDir          = ".app/backup"
	defaultKeepBackups = 7
	keepSyncRuns       = 20                   // only the latest run is read; a few more for an apply in flight
	auditRetention     = 365 * 24 * time.Hour // nothing reads audit_log yet; a year is plenty
)

type Service struct {
	DB           *store.DB
	Root         *fsx.Root
	LogRetention time.Duration
	KeepBackups  int // config backup_keep; 0 = defaultKeepBackups

	mu sync.Mutex // the hourly loop and «پشتیبان‌گیری الان» (T10.20) must not share one VACUUM INTO temp file
}

// Result says what one Run did.
type Result struct {
	Removed []string // relative dirs and files
	Backup  string   // relative path of the backup made; "" = today's already existed
}

// quickCheck verifies a finished backup before it gets its final name (T13.3); tests swap it.
var quickCheck = store.QuickCheck

// Run cleans expired job dirs, then makes today's backup and keeps the newest s.KeepBackups.
// force remakes today's backup even if it exists («پشتیبان‌گیری الان», T13.3); the hourly loop passes false.
// A cleanup error never stops the backup (T13.3): all errors are joined and returned at the end.
func (s *Service) Run(ctx context.Context, now time.Time, force bool) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &Result{}
	cutoff := now.Add(-s.LogRetention).UTC().Format("2006-01-02T15:04:05.000Z") // same form as jobs.finished_at
	var errs []error
	for _, dir := range []string{".app/work", ".app/staging", ".app/logs/jobs"} {
		errs = append(errs, s.clean(ctx, dir, cutoff, res))
	}
	errs = append(errs, s.sweepTmp(now, res), s.prune(ctx, now), s.backup(ctx, now, force, res))
	return res, errors.Join(errs...)
}

// prune bounds the rows that grow with every scan or action, so backups stop repeating them (T13.12).
// jobs stays: lessons, sets and reports reference it.
func (s *Service) prune(ctx context.Context, now time.Time) error {
	_, err := s.DB.Writer().ExecContext(ctx, `DELETE FROM sync_runs WHERE id NOT IN (SELECT id FROM sync_runs ORDER BY id DESC LIMIT ?)`, keepSyncRuns)
	if err != nil {
		return fmt.Errorf("pruning sync_runs: %w", err)
	}
	cutoff := now.Add(-auditRetention).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := s.DB.Writer().ExecContext(ctx, `DELETE FROM audit_log WHERE ts < ?`, cutoff); err != nil {
		return fmt.Errorf("pruning audit_log: %w", err)
	}
	return nil
}

// sweepTmp removes temp files a crash left behind; an hour is far longer than any write takes (CR-34).
// Only the trees the app writes into are walked, not the whole base dir (T10.7).
func (s *Service) sweepTmp(now time.Time, res *Result) error {
	cutoff := now.Add(-time.Hour)
	for _, dir := range []string{"topics", "roadmaps", ".app"} {
		err := fs.WalkDir(s.Root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() || !fsx.IsTempName(d.Name()) {
				return nil // missing, unreadable, symlink, directory or not ours
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
		if err != nil {
			return err
		}
	}
	return nil
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
	var errs []error
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
		err = s.DB.Reader().QueryRowContext(ctx, `SELECT status, COALESCE(finished_at, '') FROM jobs WHERE id = ?`, id).Scan(&status, &finished)
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
			errs = append(errs, fmt.Errorf("removing %s: %w", rel, err)) // e.g. locked by an antivirus; try the next dir
			continue
		}
		res.Removed = append(res.Removed, rel)
	}
	return errors.Join(errs...)
}

func (s *Service) backup(ctx context.Context, now time.Time, force bool, res *Result) error {
	if err := s.Root.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	rel := path.Join(backupDir, "app-"+now.UTC().Format("2006-01-02")+".db")
	if force || !s.nonEmpty(rel) {
		// Only a complete, checked backup gets the final name (CR-36, T13.3): VACUUM INTO refuses an existing
		// file, so drop a leftover temp first, and rename only on success. Rename replaces atomically, so a
		// forced backup never leaves today's file half-written.
		tmp := rel + ".tmp"
		if err := s.Root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		abs := filepath.Join(s.Root.BaseDir(), filepath.FromSlash(tmp))
		// T13.10: VACUUM INTO only reads the source, so the read pool runs it without blocking writes.
		if _, err := s.DB.Reader().ExecContext(ctx, `VACUUM INTO ?`, abs); err != nil {
			_ = s.Root.Remove(tmp)
			return fmt.Errorf("backing up database: %w", err)
		}
		if err := quickCheck(abs); err != nil {
			_ = s.Root.Remove(tmp)
			return fmt.Errorf("backup failed its integrity check, older backups kept: %w", err)
		}
		if err := s.Root.Rename(tmp, rel); err != nil {
			_ = s.Root.Remove(tmp)
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
		n := e.Name()
		if staleTmp.MatchString(n) { // a crash mid-VACUUM on an earlier day; today's was handled above
			if err := s.Root.Remove(path.Join(backupDir, n)); err != nil {
				return err
			}
			res.Removed = append(res.Removed, path.Join(backupDir, n))
			continue
		}
		// 0-byte files are interrupted backups from before CR-36: they must not push a real one out.
		if IsBackupName(n) && s.nonEmpty(path.Join(backupDir, n)) {
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

// nonEmpty reports whether rel is a regular file with content.
func (s *Service) nonEmpty(rel string) bool {
	info, err := s.Root.Lstat(rel)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

var (
	backupName = regexp.MustCompile(`^app-\d{4}-\d{2}-\d{2}\.db$`)
	staleTmp   = regexp.MustCompile(`^app-\d{4}-\d{2}-\d{2}\.db\.tmp$`)
)

// IsBackupName reports whether n is a name backup gives (app-YYYY-MM-DD.db); the only names served for download.
func IsBackupName(n string) bool { return backupName.MatchString(n) }

// BackupRel is the root-relative path of backup n; callers check IsBackupName first.
func BackupRel(n string) string { return path.Join(backupDir, n) }

// Backup is one complete backup file as listed on the settings page.
type Backup struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// Backups lists complete backups newest first; no backup dir yet = none.
func (s *Service) Backups() ([]Backup, error) {
	entries, err := fs.ReadDir(s.Root.FS(), backupDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Backup
	for _, e := range entries {
		if !IsBackupName(e.Name()) {
			continue
		}
		info, err := s.Root.Lstat(BackupRel(e.Name()))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue // symlink, gone, or interrupted
		}
		out = append(out, Backup{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	slices.Reverse(out) // ReadDir sorts by name; dated names sort oldest first
	return out, nil
}
