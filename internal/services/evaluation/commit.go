package evaluation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/schema"
	"learninghub/internal/services/exercises"
	"learninghub/internal/services/topics"
)

type setInfo struct {
	ID                     int64
	Version, RubricVersion int
	ManifestRel            string
	TopicDir, StableKey    string
	TopicTitle             string
}

// AttemptRel is the folder of one attempt (TDD §3): reports/v<N>/attempt-<K>.
func AttemptRel(topicDir string, setVersion, attemptNo int) string {
	return path.Join("topics", topicDir, "reports", fmt.Sprintf("v%d", setVersion), fmt.Sprintf("attempt-%d", attemptNo))
}

type itemRow struct {
	item      schema.ResultItem
	aiScore   sql.NullInt64
	fromID    sql.NullInt64 // attempts.id the score was carried from
	errTypes  string        // JSON
	flags     []string
	reportIdx int // index in files, -1 = none
}

type outFile struct {
	rel, kind, owner string
	b                []byte
}

// commit builds attempt K+1 from the staged evaluations and records it (R3.4, R11.2-6). Every
// manifest exercise gets an item: evaluated now, carried over (unchanged since its last
// evaluation) or unanswered, so the topic score is always over all of them. Files are written
// with WriteNew: an existing name fails the commit and earlier attempts are never touched.
func (s *Service) commit(ctx context.Context, j jobs.Job, mode string, set setInfo, list []topics.Exercise,
	sel *topics.Selection, sh shared) error {
	mb, err := s.Root.ReadFile(set.ManifestRel)
	if err != nil {
		return fmt.Errorf("reading %s: %w", set.ManifestRel, err)
	}
	var m schema.Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return fmt.Errorf("parsing %s: %w", set.ManifestRel, err)
	}
	var k int
	var started string
	if err := s.DB.Writer().QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_no), 0) + 1 FROM attempts WHERE set_id = ?`,
		set.ID).Scan(&k); err != nil {
		return err
	}
	if err := s.DB.Writer().QueryRowContext(ctx, `SELECT COALESCE(started_at, '') FROM jobs WHERE id = ?`, j.ID).Scan(&started); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if started == "" {
		started = now
	}
	dir := AttemptRel(set.TopicDir, set.Version, k)

	byID := map[string]topics.Exercise{}
	titles := map[string]string{}
	for _, x := range list {
		byID[x.ExID], titles[x.ExID] = x, x.Title
	}
	sent := map[string]topics.Pick{}
	for _, pk := range sel.Send {
		sent[pk.ExID] = pk
	}

	var files []outFile
	var rows []itemRow
	skillVersion := ""
	for _, me := range m.Exercises {
		x, ok := byID[me.ID]
		if !ok {
			return fmt.Errorf("%w: manifest exercise %s not in the database", domain.ErrInvalidState, me.ID)
		}
		if pk, ok := sent[me.ID]; ok {
			b, err := s.Root.ReadFile(OutputRel(j.ID, me.ID))
			if err != nil {
				return fmt.Errorf("staged evaluation of %s: %w", me.ID, err)
			}
			var out Output
			var ev Evaluation
			if err := json.Unmarshal(b, &out); err != nil {
				return err
			}
			if err := json.Unmarshal(out.Evaluation, &ev); err != nil {
				return err
			}
			skillVersion = out.SkillVersion
			md, item, flags := Report(ReportInput{Weight: me.Weight, Question: exercises.Section(sh.exercisesMD, me.ID), Answer: pk.Body,
				TemplateNormSHA: x.TemplateNormSHA,
				Rubric:          sh.rubric[me.ID], Eval: ev, AttemptNo: k, RubricVersion: set.RubricVersion, SkillVersion: out.SkillVersion, Date: now})
			et, _ := json.Marshal(nonNil(item.ErrorTypes))
			files = append(files, outFile{path.Join(dir, *item.ReportFile), "report", "attempt_item", md})
			rows = append(rows, itemRow{item: item, aiScore: sql.NullInt64{Int64: int64(ev.Score), Valid: true},
				errTypes: string(et), flags: flags, reportIdx: len(files) - 1})
			continue
		}
		row, err := s.carried(ctx, x, me.Weight)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}

	res := &schema.Result{Topic: schema.ManifestTopic{StableKey: set.StableKey, Title: set.TopicTitle}, SetVersion: set.Version,
		AttemptNo: k, Mode: mode, RubricVersion: set.RubricVersion, StartedAt: started, FinishedAt: now, JobID: j.ID,
		Evaluator: schema.ManifestGenerator{Adapter: s.Adapter.Name(), Model: ai.ModelFor(s.Adapter, s.Model), Skill: skillName, SkillVersion: skillVersion, JobID: j.ID}}
	flagged := map[string]bool{}
	for _, r := range rows {
		res.Items = append(res.Items, r.item)
		flagged[r.item.ExerciseID] = len(r.flags) > 0
	}
	resultJSON, err := ResultJSON(res, &m)
	if err != nil {
		return fmt.Errorf("%w: result.json: %v", domain.ErrInvalidState, err)
	}
	files = append(files,
		outFile{path.Join(dir, "summary.md"), "summary", "attempt", Summary(res, titles, flagged)},
		outFile{path.Join(dir, "result.json"), "result", "attempt", resultJSON})

	journal := make([]map[string]string, len(files))
	for i, f := range files {
		journal[i] = map[string]string{"rel_path": f.rel, "sha256": fsx.HashBytes(f.b)}
	}
	jb, _ := json.Marshal(journal)
	if _, err := s.DB.Writer().ExecContext(ctx, `UPDATE jobs SET commit_journal = ? WHERE id = ?`, string(jb), j.ID); err != nil {
		return err
	}
	var done []string
	undo := func() {
		for _, rel := range done {
			_ = s.Root.Remove(rel)
		}
	}
	if err := s.Root.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		if err := s.Root.WriteNew(f.rel, f.b, 0o644); err != nil {
			undo()
			return fmt.Errorf("writing %s: %w", f.rel, err)
		}
		done = append(done, f.rel)
	}

	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		ids := make([]int64, len(files))
		for i, f := range files {
			sha := fsx.HashBytes(f.b)
			r, err := tx.ExecContext(ctx, `INSERT INTO files (topic_id, kind, owner_type, rel_path, sha256, size_bytes, immutable, observed_sha256)
				VALUES (?, ?, ?, ?, ?, ?, 1, ?)`, j.TopicID, f.kind, f.owner, f.rel, sha, len(f.b), sha)
			if err != nil {
				return err
			}
			if ids[i], err = r.LastInsertId(); err != nil {
				return err
			}
		}
		a := res.Aggregate
		r, err := tx.ExecContext(ctx, `INSERT INTO attempts (set_id, attempt_no, mode, job_id, rubric_version, topic_score, answered_count,
			total_count, is_partial, result_file, summary_file, skill, skill_version, model, started_at, finished_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			set.ID, k, mode, j.ID, set.RubricVersion, a.WeightedScore, a.AnsweredCount, a.TotalCount, a.Partial,
			ids[len(ids)-1], ids[len(ids)-2], skillName, skillVersion, ai.ModelFor(s.Adapter, s.Model), started, now)
		if err != nil {
			return err
		}
		attemptID, err := r.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET owner_id = ? WHERE id IN (?, ?)`, attemptID, ids[len(ids)-1], ids[len(ids)-2]); err != nil {
			return err
		}
		for _, row := range rows {
			var reportID sql.NullInt64
			if row.reportIdx >= 0 {
				reportID = sql.NullInt64{Int64: ids[row.reportIdx], Valid: true}
			}
			r, err := tx.ExecContext(ctx, `INSERT INTO attempt_items (attempt_id, exercise_id, status, answer_sha256, score, ai_reported_score,
				carried_from_attempt, report_file, error_types, flags)
				SELECT ?, id, ?, ?, ?, ?, ?, ?, ?, ? FROM exercises WHERE set_id = ? AND ex_id = ?`,
				attemptID, row.item.Status, row.item.AnswerSHA256, row.item.Score, row.aiScore, row.fromID, reportID, row.errTypes,
				jsonList(row.flags),
				set.ID, row.item.ExerciseID)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n != 1 {
				return fmt.Errorf("%w: exercise %s not in set %d", domain.ErrInvalidState, row.item.ExerciseID, set.ID)
			}
			if reportID.Valid {
				itemID, err := r.LastInsertId()
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE files SET owner_id = ? WHERE id = ?`, itemID, reportID.Int64); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE topics SET status = 'evaluated', updated_at = `+nowSQL+` WHERE id = ?`, j.TopicID); err != nil {
			return err
		}
		ref, _ := json.Marshal(map[string]int64{"attempt_id": attemptID})
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET result_ref = ? WHERE id = ?`, string(ref), j.ID)
		return err
	})
	if err != nil {
		undo()
	}
	return err
}

// carried returns x's item when it was not sent: carried over if the answer on disk is still the
// one last evaluated, otherwise unanswered (template, missing, or held for manual review).
func (s *Service) carried(ctx context.Context, x topics.Exercise, weight float64) (itemRow, error) {
	row := itemRow{item: schema.ResultItem{ExerciseID: x.ExID, Weight: weight, Status: "unanswered"}, errTypes: "[]", reportIdx: -1}
	if x.EvaluatedSHA == "" {
		return row, nil
	}
	sha, _, err := s.Root.HashFile(x.AnswerRel)
	if err != nil || sha != x.EvaluatedSHA {
		return row, nil // gone or changed (and not sent): not the answer that score belongs to
	}
	var attemptID int64
	var attemptNo, score int
	var et, flags string
	err = s.DB.Writer().QueryRowContext(ctx, `SELECT a.id, a.attempt_no, i.score, i.error_types, i.flags FROM attempt_items i
		JOIN attempts a ON a.id = i.attempt_id JOIN exercises e ON e.id = i.exercise_id
		WHERE e.answer_file_id = ? AND i.status = 'evaluated' ORDER BY a.attempt_no DESC LIMIT 1`, x.AnswerFileID).
		Scan(&attemptID, &attemptNo, &score, &et, &flags)
	if errors.Is(err, sql.ErrNoRows) {
		return row, nil
	}
	if err != nil {
		return row, err
	}
	row.item.Status, row.item.Score, row.item.AnswerSHA256, row.item.CarriedFromAttempt = "carried_over", score, &sha, &attemptNo
	row.fromID, row.errTypes = sql.NullInt64{Int64: attemptID, Valid: true}, et
	if err := json.Unmarshal([]byte(et), &row.item.ErrorTypes); err != nil {
		return row, err
	}
	if err := json.Unmarshal([]byte(flags), &row.flags); err != nil { // the carried score keeps its warning
		return row, err
	}
	return row, nil
}

func jsonList(s []string) string {
	b, _ := json.Marshal(nonNil(s))
	return string(b)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
