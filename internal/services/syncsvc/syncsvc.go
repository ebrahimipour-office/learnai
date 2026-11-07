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
	"time"

	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/services/status"
	"learninghub/internal/store"
)

const (
	nowSQL    = `strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	topicsDir = "topics"
)

// hubRe reads the answer-template header written by fsx.GenerateAnswerTemplate.
var hubRe = regexp.MustCompile(`@hub:topic=([^,\s]+),set=v(\d+),ex=([A-Za-z0-9_.-]+)`)

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
	To string `json:"to"`
	By string `json:"by"` // "hash" | "header"
}

// Report is sync_runs.report.
type Report struct {
	Missing  []Item   `json:"missing"`
	Moved    []Move   `json:"moved"`
	New      []string `json:"new"`      // unregistered files under topics/
	Modified []Item   `json:"modified"` // content differs from what the app wrote; answer files are expected to change and are left out
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
}

// Scan checks every registered file and every file under topics/, records observations and the report.
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
		sha   string
		mtime int64
	}
	seen := map[int64]obs{}
	rep := Report{Missing: []Item{}, Moved: []Move{}, New: []string{}, Modified: []Item{}}
	var missing []row
	for _, r := range rows {
		sha, mtime, err := s.observe(r.RelPath)
		if errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, r)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", r.RelPath, err)
		}
		seen[r.FileID] = obs{sha, mtime}
		if sha != r.SHA && r.Kind != "answer" {
			rep.Modified = append(rep.Modified, r.Item)
		}
	}

	unregistered, err := s.unregistered(registered)
	if err != nil {
		return nil, err
	}
	rep.Moved, rep.New, err = s.match(ctx, missing, unregistered)
	if err != nil {
		return nil, err
	}
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
	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		for id, o := range seen {
			if _, err := tx.ExecContext(ctx, `UPDATE files SET observed_sha256 = ?, observed_mtime = ?, observed_at = `+nowSQL+`,
				presence = 'present' WHERE id = ?`, o.sha, o.mtime, id); err != nil {
				return err
			}
		}
		for _, r := range missing {
			if _, err := tx.ExecContext(ctx, `UPDATE files SET presence = 'missing', observed_at = `+nowSQL+` WHERE id = ?`, r.FileID); err != nil {
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
	q, err := s.DB.Writer().QueryContext(ctx, `SELECT id, topic_id, kind, rel_path, sha256, COALESCE(observed_sha256, ''), presence = 'present'
		FROM files ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	var out []row
	for q.Next() {
		var r row
		if err := q.Scan(&r.FileID, &r.TopicID, &r.Kind, &r.RelPath, &r.SHA, &r.Observed, &r.Present); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, q.Err()
}

func (s *Service) observe(rel string) (sha string, mtime int64, err error) {
	fi, err := s.Root.Stat(rel)
	if err != nil {
		return "", 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, fs.ErrNotExist // a directory where the file was is as good as missing
	}
	sha, _, err = s.Root.HashFile(rel)
	return sha, fi.ModTime().UnixNano(), err
}

// unregistered lists regular files under topics/ that no files row points at, in path order.
func (s *Service) unregistered(registered map[string]bool) ([]string, error) {
	var out []string
	err := fs.WalkDir(s.Root.FS(), topicsDir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == topicsDir {
			return fs.SkipAll // no topic folder yet
		}
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && !registered[p] {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// match pairs missing files with unregistered ones: first by content hash (the app's or the last
// observed one), then answer files by their @hub header, which survives editing. A pairing must be
// unambiguous on both sides; anything else stays missing or new.
func (s *Service) match(ctx context.Context, missing []row, candidates []string) ([]Move, []string, error) {
	moves := []Move{}
	if len(missing) == 0 {
		return moves, append([]string{}, candidates...), nil
	}
	byHash := map[string][]int{} // sha → indexes into missing
	for i, r := range missing {
		byHash[r.SHA] = append(byHash[r.SHA], i)
		if r.Observed != "" && r.Observed != r.SHA {
			byHash[r.Observed] = append(byHash[r.Observed], i)
		}
	}
	type cand struct {
		path string
		sha  string
	}
	cands := make([]cand, len(candidates))
	hashHits := map[int][]int{} // missing index → candidate indexes
	for ci, p := range candidates {
		sha, _, err := s.Root.HashFile(p)
		if err != nil {
			return nil, nil, fmt.Errorf("hashing %s: %w", p, err)
		}
		cands[ci] = cand{p, sha}
		if idx := byHash[sha]; len(idx) == 1 {
			hashHits[idx[0]] = append(hashHits[idx[0]], ci)
		}
	}
	taken := map[int]bool{}   // candidate index
	matched := map[int]bool{} // missing index
	for mi, cis := range hashHits {
		if len(cis) == 1 {
			moves = append(moves, Move{Item: missing[mi].Item, To: cands[cis[0]].path, By: "hash"})
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
			return nil, nil, err
		}
		for mi, r := range missing {
			if !matched[mi] && r.FileID == id {
				headerHits[mi] = append(headerHits[mi], ci)
			}
		}
	}
	for mi, cis := range headerHits {
		if len(cis) == 1 {
			moves = append(moves, Move{Item: missing[mi].Item, To: cands[cis[0]].path, By: "header"})
			taken[cis[0]] = true
		}
	}
	slices.SortFunc(moves, func(a, b Move) int { return int(a.FileID - b.FileID) })
	newFiles := []string{}
	for ci, c := range cands {
		if !taken[ci] {
			newFiles = append(newFiles, c.path)
		}
	}
	return moves, newFiles, nil
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
		sha   string
		mtime int64
	}
	seen := make([]obs, len(chosen))
	for i, m := range chosen {
		if s.Root.Exists(m.RelPath) {
			return nil, fmt.Errorf("%w: %s is back", ErrStale, m.RelPath)
		}
		sha, mtime, err := s.observe(m.To)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrStale, m.To, err)
		}
		seen[i] = obs{sha, mtime}
	}
	applied, err := json.Marshal(chosen)
	if err != nil {
		return nil, err
	}
	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		for i, m := range chosen {
			res, err := tx.ExecContext(ctx, `UPDATE files SET rel_path = ?, presence = 'present', observed_sha256 = ?, observed_mtime = ?,
				observed_at = `+nowSQL+` WHERE id = ? AND rel_path = ? AND NOT EXISTS (SELECT 1 FROM files WHERE rel_path = ?)`,
				m.To, seen[i].sha, seen[i].mtime, m.FileID, m.RelPath, m.To)
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
