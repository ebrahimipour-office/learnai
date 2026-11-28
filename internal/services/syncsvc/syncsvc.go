// Package syncsvc compares the files table with the disk (R5.3, T6.3). A scan never deletes or
// rewrites a file or a record: it only updates files.presence, files.observed_* and the topics.status cache, and stores
// its findings in sync_runs. A move is recorded only when the user applies it.
package syncsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/services/lessons"
	"learninghub/internal/services/status"
	"learninghub/internal/store"
)

const (
	nowSQL    = `strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	topicsDir = "topics"
)

// hubRe reads the answer-template header written by fsx.GenerateAnswerTemplate.
var hubRe = regexp.MustCompile(`@hub:topic=([^,\s]+),set=v(\d+),ex=([A-Za-z0-9_.-]+)`)

// emptySHA is the SHA-256 hash of an empty byte slice. Empty files are never matched as moves.
var emptySHA = fsx.HashString("")

// Item is a registered file in a finding.
type Item struct {
	FileID  int64  `json:"file_id"`
	TopicID int64  `json:"topic_id"`
	Kind    string `json:"kind"`
	RelPath string `json:"rel_path"`
}

// Move is a missing registered file found at another path.
type Move struct {
	Item
	To      string `json:"to"`
	By      string `json:"by"` // "hash" | "header"
	SHA     string `json:"sha,omitempty"`
	FileSHA string `json:"file_sha,omitempty"`
}

// Report is sync_runs.report.
type Report struct {
	Missing  []Item   `json:"missing"`
	Moved    []Move   `json:"moved"`
	New      []string `json:"new"`      // unregistered files under topics/
	Modified []Item   `json:"modified"` // content differs from what the app wrote; answer files are expected to change and are left out
	Skipped  []string `json:"skipped"`  // files under topics/ whose path fsx rejects; never read
}

// Run is one sync_runs row.
type Run struct {
	ID         int64
	StartedAt  string
	FinishedAt string
	Report     Report
	Applied    []Move // moves the user applied; nil = none yet
}

type Service struct {
	DB   *store.DB
	Root *fsx.Root
}

type row struct {
	Item
	SHA, Observed string
	Present       bool
	Size          int64 // size_bytes when the app wrote it
	ObsSize       int64 // -1 = unknown
	ObsMtime      int64
}

// Scan checks every registered file and every file under topics/, records observations and the report.
// beforeCommitTx runs between disk observation and the DB transaction; tests replace it to simulate concurrent DB changes (CR-52).
var beforeCommitTx = func() {}

func (s *Service) Scan(ctx context.Context) (*Run, error) {
	started := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.rows(ctx)
	if err != nil {
		return nil, err
	}
	registered := map[string]bool{}
	for _, r := range rows {
		registered[r.RelPath] = true
	}

	type obs struct {
		sha         string
		size, mtime int64
	}
	type observed struct {
		r   row
		obs obs
	}
	var seen []observed
	rep := Report{Missing: []Item{}, Moved: []Move{}, New: []string{}, Modified: []Item{}, Skipped: []string{}}
	var missing []row
	reindex := map[int64][]byte{}
	for _, r := range rows {
		sha, size, mtime, err := s.observe(r)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) { // ENOTDIR: a parent folder became a file
			missing = append(missing, r)
			continue
		}
		if err != nil { // unreadable (permissions, symlink escape): reported, row left as it was (T13.5)
			rep.Skipped = append(rep.Skipped, r.RelPath)
			continue
		}
		if !r.Present || sha != r.Observed || size != r.ObsSize || mtime != r.ObsMtime {
			seen = append(seen, observed{r: r, obs: obs{sha, size, mtime}}) // unchanged rows cost no write (T13.11)
		}
		// T9.15: a current lesson edited by hand since the last look gets its search text refreshed.
		if r.Kind == "lesson" && strings.HasSuffix(r.RelPath, "/lesson/lesson.md") && sha != r.Observed {
			if md, err := s.Root.ReadFile(r.RelPath); err == nil {
				reindex[r.TopicID] = md
			} else {
				rep.Skipped = append(rep.Skipped, r.RelPath)
			}
		}
		if sha != r.SHA && r.Kind != "answer" {
			rep.Modified = append(rep.Modified, r.Item)
		}
	}

	unregistered, skipped, err := s.unregistered(registered)
	if err != nil {
		return nil, err
	}
	rep.Skipped = append(rep.Skipped, skipped...)
	var unhashed []string
	rep.Moved, rep.New, unhashed, err = s.match(ctx, missing, unregistered)
	if err != nil {
		return nil, err
	}
	rep.Skipped = append(rep.Skipped, unhashed...)
	moved := map[int64]bool{}
	for _, m := range rep.Moved {
		moved[m.FileID] = true
	}
	for _, r := range missing {
		if !moved[r.FileID] {
			rep.Missing = append(rep.Missing, r.Item)
		}
	}

	raw, err := json.Marshal(rep)
	if err != nil {
		return nil, err
	}
	run := &Run{StartedAt: started, Report: rep}
	beforeCommitTx()
	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		for _, o := range seen {
			if _, err := tx.ExecContext(ctx, `UPDATE files SET observed_sha256 = ?, observed_size = ?, observed_mtime = ?, observed_at = `+nowSQL+`,
				presence = 'present' WHERE id = ? AND rel_path = ? AND sha256 = ?`, o.obs.sha, o.obs.size, o.obs.mtime, o.r.FileID, o.r.RelPath, o.r.SHA); err != nil {
				return err
			}
		}
		for _, r := range missing {
			if !r.Present {
				continue // already recorded as missing
			}
			if _, err := tx.ExecContext(ctx, `UPDATE files SET presence = 'missing', observed_at = `+nowSQL+` WHERE id = ? AND rel_path = ? AND sha256 = ?`, r.FileID, r.RelPath, r.SHA); err != nil {
				return err
			}
		}
		for topicID, md := range reindex {
			if err := lessons.IndexText(ctx, tx, topicID, md); err != nil {
				return err
			}
		}
		if _, err := status.Recompute(ctx, tx); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO sync_runs (started_at, finished_at, report) VALUES (?, `+nowSQL+`, ?)`, started, string(raw))
		if err != nil {
			return err
		}
		run.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

func (s *Service) rows(ctx context.Context) ([]row, error) {
	q, err := s.DB.Writer().QueryContext(ctx, `SELECT id, topic_id, kind, rel_path, sha256, COALESCE(observed_sha256, ''), presence = 'present', size_bytes,
		COALESCE(observed_size, -1), COALESCE(observed_mtime, 0) FROM files ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	var out []row
	for q.Next() {
		var r row
		if err := q.Scan(&r.FileID, &r.TopicID, &r.Kind, &r.RelPath, &r.SHA, &r.Observed, &r.Present, &r.Size, &r.ObsSize, &r.ObsMtime); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, q.Err()
}

// hashFile is swapped by tests to count hashing (T10.7).
var hashFile = (*fsx.Root).HashFile

// observe stats r and hashes it, unless size and mtime still match the last observation (T10.7).
// Pass a row with only RelPath set to always hash.
func (s *Service) observe(r row) (sha string, size, mtime int64, err error) {
	fi, err := s.Root.Stat(r.RelPath)
	if err != nil {
		return "", 0, 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, 0, fs.ErrNotExist // a directory where the file was is as good as missing
	}
	size, mtime = fi.Size(), fi.ModTime().UnixNano()
	if r.Observed != "" && r.ObsSize == size && r.ObsMtime == mtime {
		return r.Observed, size, mtime, nil
	}
	sha, _, err = hashFile(s.Root, r.RelPath)
	return sha, size, mtime, err
}

// candidate is an unregistered file under topics/.
type candidate struct {
	path string
	size int64
}

// unregistered lists regular files under topics/ that no files row points at, in path order.
// A path fsx would refuse to open (CR-04) goes to skipped instead of failing the scan.
func (s *Service) unregistered(registered map[string]bool) (out []candidate, skipped []string, err error) {
	err = fs.WalkDir(s.Root.FS(), topicsDir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == topicsDir {
			return fs.SkipAll // no topic folder yet
		}
		if err != nil && p == topicsDir {
			return err
		}
		if err != nil { // an unreadable folder (e.g. permissions) is reported, not fatal (T12.1)
			skipped = append(skipped, p)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || registered[p] || fsx.IsTempName(d.Name()) {
			return nil // temp files are in-flight writes or crash leftovers, not the learner's (T13.4)
		}
		fi, ierr := d.Info()
		if ierr != nil || fsx.ValidateRelPath(p) != nil || !s.readable(p) {
			skipped = append(skipped, p)
		} else {
			out = append(out, candidate{p, fi.Size()})
		}
		return nil
	})
	return out, skipped, err
}

// readable reports whether rel can be opened: match hashes and reads every candidate, and one
// unreadable file must not abort the whole scan (T12.1).
func (s *Service) readable(rel string) bool {
	f, err := s.Root.Open(rel)
	if err == nil {
		f.Close()
	}
	return err == nil
}

// match pairs missing files with unregistered ones: first by content hash (the app's or the last
// observed one), then answer files by their @hub header, which survives editing. A pairing must be
// unambiguous on both sides; anything else stays missing or new.
// A candidate that cannot be hashed is returned in skipped (T13.5).
func (s *Service) match(ctx context.Context, missing []row, candidates []candidate) (moves []Move, newFiles, skipped []string, err error) {
	moves = []Move{}
	if len(missing) == 0 {
		newFiles = []string{}
		for _, c := range candidates {
			newFiles = append(newFiles, c.path)
		}
		return moves, newFiles, nil, nil
	}
	byHash := map[string][]int{} // sha → indexes into missing
	sizes := map[int64]bool{}    // only a candidate of one of these sizes can match by hash (T13.11)
	for i, r := range missing {
		if r.SHA != "" && r.SHA != emptySHA {
			byHash[r.SHA] = append(byHash[r.SHA], i)
			sizes[r.Size] = true
		}
		if r.Observed != "" && r.Observed != r.SHA && r.Observed != emptySHA {
			byHash[r.Observed] = append(byHash[r.Observed], i)
			sizes[r.ObsSize] = true
		}
	}
	type cand struct {
		path string
		sha  string
	}
	var cands []cand
	hashHits := map[int][]int{} // missing index → candidate indexes
	for _, c := range candidates {
		p, sha := c.path, ""
		if sizes[c.size] {
			if sha, _, err = hashFile(s.Root, p); err != nil {
				skipped = append(skipped, p)
				continue
			}
		}
		ci := len(cands)
		cands = append(cands, cand{p, sha})
		if sha != "" && sha != emptySHA {
			if idx := byHash[sha]; len(idx) == 1 {
				hashHits[idx[0]] = append(hashHits[idx[0]], ci)
			}
		}
	}
	taken := map[int]bool{}   // candidate index
	matched := map[int]bool{} // missing index
	for mi, cis := range hashHits {
		if len(cis) == 1 {
			moves = append(moves, Move{
				Item:    missing[mi].Item,
				To:      cands[cis[0]].path,
				By:      "hash",
				SHA:     cands[cis[0]].sha,
				FileSHA: missing[mi].SHA,
			})
			taken[cis[0]], matched[mi] = true, true
		}
	}

	headerHits := map[int][]int{}
	for ci, c := range cands {
		if taken[ci] {
			continue
		}
		id, err := s.headerFile(ctx, c.path)
		if err != nil {
			return nil, nil, nil, err
		}
		for mi, r := range missing {
			if !matched[mi] && r.FileID == id {
				headerHits[mi] = append(headerHits[mi], ci)
			}
		}
	}
	for mi, cis := range headerHits {
		if len(cis) == 1 {
			moves = append(moves, Move{
				Item:    missing[mi].Item,
				To:      cands[cis[0]].path,
				By:      "header",
				FileSHA: missing[mi].SHA,
			})
			taken[cis[0]] = true
		}
	}
	slices.SortFunc(moves, func(a, b Move) int { return int(a.FileID - b.FileID) })
	newFiles = []string{}
	for ci, c := range cands {
		if !taken[ci] {
			newFiles = append(newFiles, c.path)
		}
	}
	return moves, newFiles, skipped, nil
}

// headerFile returns the answer file id named by the @hub header at the top of rel, or 0.
func (s *Service) headerFile(ctx context.Context, rel string) (int64, error) {
	f, err := s.Root.Open(rel)
	if err != nil {
		return 0, err
	}
	head := make([]byte, 1024)
	n, err := io.ReadFull(f, head)
	f.Close()
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, err
	}
	m := hubRe.FindSubmatch(head[:n])
	if m == nil {
		return 0, nil
	}
	version, _ := strconv.Atoi(string(m[2]))
	var ids []int64
	q, err := s.DB.Writer().QueryContext(ctx, `SELECT e.answer_file_id FROM exercises e JOIN exercise_sets x ON x.id = e.set_id
		JOIN topics t ON t.id = x.topic_id WHERE t.stable_key = ? AND x.version = ? AND e.ex_id = ?`, string(m[1]), version, string(m[3]))
	if err != nil {
		return 0, err
	}
	defer q.Close()
	for q.Next() {
		var id int64
		if err := q.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 { // the same stable key in two roadmaps: ambiguous, leave it to the user
		return 0, q.Err()
	}
	return ids[0], q.Err()
}

// Latest returns the newest sync run, or nil if there was none.
func (s *Service) Latest(ctx context.Context) (*Run, error) {
	return s.run(ctx, `ORDER BY id DESC LIMIT 1`)
}

func (s *Service) run(ctx context.Context, where string, args ...any) (*Run, error) {
	var r Run
	var report string
	var fin, applied sql.NullString
	err := s.DB.Writer().QueryRowContext(ctx, `SELECT id, started_at, finished_at, report, applied FROM sync_runs `+where, args...).
		Scan(&r.ID, &r.StartedAt, &fin, &report, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.FinishedAt = fin.String
	if err := json.Unmarshal([]byte(report), &r.Report); err != nil {
		return nil, fmt.Errorf("decoding sync report: %w", err)
	}
	if applied.Valid {
		if err := json.Unmarshal([]byte(applied.String), &r.Applied); err != nil {
			return nil, fmt.Errorf("decoding sync applied: %w", err)
		}
	}
	return &r, nil
}

// ErrStale means the disk changed since the scan; scan again before applying.
var ErrStale = errors.New("sync: disk changed since the scan")

// ApplyMoves records the moves the user confirmed (file ids from run's report): rel_path points at
// the new location. No file is touched. Each move is re-checked against the disk first, and either
// all of them are recorded or none.
func (s *Service) ApplyMoves(ctx context.Context, runID int64, fileIDs []int64) ([]Move, error) {
	run, err := s.run(ctx, `WHERE id = ?`, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, domain.ErrNotFound
	}
	if run.Applied != nil {
		return nil, fmt.Errorf("%w: sync run %d was already applied", domain.ErrInvalidState, runID)
	}
	var chosen []Move
	for _, id := range fileIDs {
		i := slices.IndexFunc(run.Report.Moved, func(m Move) bool { return m.FileID == id })
		if i < 0 {
			return nil, fmt.Errorf("%w: file %d is not a move in sync run %d", domain.ErrInvalidInput, id, runID)
		}
		if !slices.ContainsFunc(chosen, func(m Move) bool { return m.FileID == id }) {
			chosen = append(chosen, run.Report.Moved[i])
		}
	}
	if len(chosen) == 0 {
		return nil, fmt.Errorf("%w: no move selected", domain.ErrInvalidInput)
	}
	type obs struct {
		sha         string
		size, mtime int64
		dbSHA       string
	}
	seen := make([]obs, len(chosen))
	for i, m := range chosen {
		if s.Root.Exists(m.RelPath) {
			return nil, fmt.Errorf("%w: %s is back", ErrStale, m.RelPath)
		}
		sha, size, mtime, err := s.observe(row{Item: Item{RelPath: m.To}})
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrStale, m.To, err)
		}
		var dbSHA string
		switch m.By {
		case "hash":
			if m.SHA != "" && sha != m.SHA {
				return nil, fmt.Errorf("%w: %s content changed", ErrStale, m.To)
			}
			var dbObs string
			err := s.DB.Writer().QueryRowContext(ctx, `SELECT sha256, COALESCE(observed_sha256, '') FROM files WHERE id = ?`, m.FileID).Scan(&dbSHA, &dbObs)
			if err != nil {
				return nil, fmt.Errorf("%w: file %d: %v", ErrStale, m.FileID, err)
			}
			if m.FileSHA != "" && dbSHA != m.FileSHA {
				return nil, fmt.Errorf("%w: file %d changed in the database", ErrStale, m.FileID)
			}
			if sha != dbSHA && (dbObs == "" || sha != dbObs) {
				return nil, fmt.Errorf("%w: %s content changed", ErrStale, m.To)
			}
		case "header":
			id, err := s.headerFile(ctx, m.To)
			if err != nil {
				return nil, fmt.Errorf("%w: %s: %v", ErrStale, m.To, err)
			}
			if id != m.FileID {
				return nil, fmt.Errorf("%w: %s header changed", ErrStale, m.To)
			}
			if err := s.DB.Writer().QueryRowContext(ctx, `SELECT sha256 FROM files WHERE id = ?`, m.FileID).Scan(&dbSHA); err != nil {
				return nil, fmt.Errorf("%w: file %d: %v", ErrStale, m.FileID, err)
			}
			if m.FileSHA != "" && dbSHA != m.FileSHA {
				return nil, fmt.Errorf("%w: file %d changed in the database", ErrStale, m.FileID)
			}
		default:
			return nil, fmt.Errorf("%w: unknown move kind %s", domain.ErrInvalidState, m.By)
		}
		seen[i] = obs{sha: sha, size: size, mtime: mtime, dbSHA: dbSHA}
	}
	applied, err := json.Marshal(chosen)
	if err != nil {
		return nil, err
	}
	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		for i, m := range chosen {
			expectedSHA := seen[i].dbSHA
			if m.FileSHA != "" {
				expectedSHA = m.FileSHA
			}
			res, err := tx.ExecContext(ctx, `UPDATE files SET rel_path = ?, presence = 'present', observed_sha256 = ?, observed_size = ?, observed_mtime = ?,
				observed_at = `+nowSQL+` WHERE id = ? AND rel_path = ? AND sha256 = ? AND NOT EXISTS (SELECT 1 FROM files WHERE rel_path = ?)`,
				m.To, seen[i].sha, seen[i].size, seen[i].mtime, m.FileID, m.RelPath, expectedSHA, m.To)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return fmt.Errorf("%w: file %d changed in the database", ErrStale, m.FileID)
			}
			detail, _ := json.Marshal(map[string]any{"sync_run": runID, "from": m.RelPath, "to": m.To, "by": m.By})
			if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log (action, target_type, target_id, detail) VALUES ('sync_move', 'file', ?, ?)`,
				m.FileID, string(detail)); err != nil {
				return err
			}
		}
		if _, err := status.Recompute(ctx, tx); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE sync_runs SET applied = ? WHERE id = ? AND applied IS NULL`, string(applied), runID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: sync run %d was already applied", domain.ErrInvalidState, runID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return chosen, nil
}
