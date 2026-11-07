// Package evaluation is the evaluate job handler (R3.4, D10): one AI call per exercise, each a
// job_steps checkpoint whose output is kept in staging, so a retry only runs what is unfinished.
// When every step is done, the commit writes reports/vN/attempt-K and its rows (T5.5).
package evaluation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/prompt"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/schema"
	"learninghub/internal/services/exercises"
	"learninghub/internal/services/topics"
	"learninghub/internal/store"
	"learninghub/skills"
)

const (
	skillName  = "answer-evaluator"
	JobTimeout = 30 * time.Minute // whole evaluation job (TDD §6.5)
	nowSQL     = `strftime('%Y-%m-%dT%H:%M:%fZ','now')`
)

// Params is jobs.params for an evaluate job.
type Params struct {
	Mode string `json:"mode"` // topics.ModeIncremental (default) | topics.ModeFull
}

type Service struct {
	DB             *store.DB
	Root           *fsx.Root
	Adapter        ai.Adapter
	Model          string
	Language       string
	Timeout        time.Duration // per exercise: config jobs.timeout_evaluate_item
	Stall          time.Duration // config jobs.stall_timeout
	Parallel       int           // config jobs.evaluate_parallelism
	MaxAnswerBytes int64         // config limits.max_answer_bytes
}

// Output is what a done step leaves in staging for the commit (T5.5).
type Output struct {
	ExerciseID   string          `json:"exercise_id"`
	AnswerSHA256 string          `json:"answer_sha256"` // hash of the exact bytes the AI saw
	Evaluation   json.RawMessage `json:"evaluation"`    // validated ai-evaluation-output
	SkillVersion string          `json:"skill_version"`
}

func StepKey(exID string) string { return "exercise:" + exID }

// OutputRel is a step's staging file.
func OutputRel(jobID int64, exID string) string {
	return path.Join(jobs.StagingRel(jobID), exID+".json")
}

// Handle is the jobs.Handler for domain.JobKindEvaluate.
func (s *Service) Handle(ctx context.Context, j jobs.Job) error {
	p := Params{Mode: topics.ModeIncremental}
	if err := json.Unmarshal(j.Params, &p); err != nil {
		return fmt.Errorf("%w: params: %v", domain.ErrInvalidInput, err)
	}
	ctx, cancel := context.WithTimeout(ctx, JobTimeout)
	defer cancel()
	err := s.run(ctx, j, p.Mode)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%w: evaluation job exceeded %s", ai.ErrTimeout, JobTimeout)
	}
	return err
}

type shared struct {
	exercisesMD []byte
	rubric      map[string]schema.RubricExercise
	in          prompt.Input // Agents, Profile, Skill, Schema; Files/Step set per exercise
}

func (s *Service) run(ctx context.Context, j jobs.Job, mode string) error {
	s.phase(ctx, j.ID, "prepare")
	var set setInfo
	var exRel, rubricRel string
	err := s.DB.Writer().QueryRowContext(ctx, `SELECT x.id, x.version, x.rubric_version, fe.rel_path, fr.rel_path, fm.rel_path,
		t.dir_name, t.stable_key, t.title FROM exercise_sets x
		JOIN files fe ON fe.id = x.exercises_file JOIN files fr ON fr.id = x.rubric_file JOIN files fm ON fm.id = x.manifest_file
		JOIN topics t ON t.id = x.topic_id
		WHERE x.topic_id = ? AND x.is_current = 1`, j.TopicID).Scan(&set.ID, &set.Version, &set.RubricVersion, &exRel, &rubricRel,
		&set.ManifestRel, &set.TopicDir, &set.StableKey, &set.TopicTitle)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: topic %d has no exercise set", domain.ErrInvalidState, j.TopicID)
	}
	if err != nil {
		return err
	}
	list, err := topics.NewService(s.DB).Exercises(ctx, set.ID)
	if err != nil {
		return err
	}
	// Selected again at run time: answers may have changed while queued; unanswered never go out (R3.4).
	sel, err := topics.SelectForEvaluation(s.Root, list, mode, s.MaxAnswerBytes)
	if err != nil {
		return err
	}

	sh := shared{rubric: map[string]schema.RubricExercise{}, in: prompt.Input{JobID: j.ID, Task: ai.TaskEvaluate}}
	read := func(rel string) ([]byte, error) {
		b, err := s.Root.ReadFile(rel)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", rel, err)
		}
		return b, nil
	}
	if sh.exercisesMD, err = read(exRel); err != nil {
		return err
	}
	rubricB, err := read(rubricRel)
	if err != nil {
		return err
	}
	var rub schema.Rubric
	if err := json.Unmarshal(rubricB, &rub); err != nil {
		return fmt.Errorf("parsing %s: %w", rubricRel, err)
	}
	for _, r := range rub.Exercises {
		sh.rubric[r.ID] = r
	}
	if sh.in.Agents, err = read("AGENTS.md"); err != nil {
		return err
	}
	if sh.in.Profile, err = read("profile.md"); err != nil {
		return err
	}
	if sh.in.Skill, err = read(skills.SkillRel(skillName)); err != nil {
		return err
	}
	if sh.in.Schema, err = schema.GetSchemaBytes("ai-evaluation-output.schema.json"); err != nil {
		return err
	}

	// Same fingerprint ⇒ a retry may reuse the failed job's done steps (TDD §6.4).
	var fp bytes.Buffer
	fp.WriteString(s.Model + "\x00")
	for _, b := range [][]byte{sh.in.Skill, sh.in.Agents, sh.in.Profile, sh.exercisesMD, rubricB} {
		fp.WriteString(fsx.HashBytes(b) + "\x00")
	}
	for _, pk := range sel.Send {
		fp.WriteString(pk.ExID + "=" + pk.SHA + "\x00")
	}
	fingerprint := fsx.HashBytes(fp.Bytes())
	if _, err := s.DB.Writer().ExecContext(ctx, `UPDATE jobs SET inputs_fingerprint = ?, adapter = ?, model = ?, skill = ? WHERE id = ?`,
		fingerprint, s.Adapter.Name(), s.Model, skillName, j.ID); err != nil {
		return err
	}

	done, err := s.inherit(ctx, j, fingerprint)
	if err != nil {
		return err
	}
	for _, pk := range sel.Send {
		if _, err := s.DB.Writer().ExecContext(ctx, `INSERT INTO job_steps (job_id, step_key, status) VALUES (?, ?, 'pending')
			ON CONFLICT DO NOTHING`, j.ID, StepKey(pk.ExID)); err != nil {
			return err
		}
	}
	if _, err := s.DB.Writer().ExecContext(ctx, `UPDATE jobs SET progress_total = ?, progress_done = ? WHERE id = ?`,
		len(sel.Send), len(done), j.ID); err != nil {
		return err
	}

	s.phase(ctx, j.ID, "ai")
	// A failed exercise fails the job but does not stop the others: each finished one is a
	// checkpoint the retry keeps (D10). Cancel and the job timeout still stop everything via ctx.
	sem := make(chan struct{}, max(s.Parallel, 1))
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for _, pk := range sel.Send {
		if done[pk.ExID] {
			continue
		}
		select { // acquired here so exercises start in order
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := s.step(ctx, j, sh, pk); err != nil {
				once.Do(func() { first = err })
			}
		})
	}
	wg.Wait()
	if first == nil && ctx.Err() != nil {
		first = fmt.Errorf("%w: %v", ai.ErrCanceled, ctx.Err())
	}
	if first != nil {
		return first
	}
	return j.Commit(ctx, func(ctx context.Context) error { return s.commit(ctx, j, mode, set, list, sel, sh) })
}

// inherit copies the done steps of the job this one retries, if its inputs are unchanged.
func (s *Service) inherit(ctx context.Context, j jobs.Job, fingerprint string) (map[string]bool, error) {
	done := map[string]bool{}
	if j.RetryOf == 0 {
		return done, nil
	}
	var old sql.NullString
	if err := s.DB.Writer().QueryRowContext(ctx, `SELECT inputs_fingerprint FROM jobs WHERE id = ?`, j.RetryOf).Scan(&old); err != nil {
		return nil, err
	}
	if old.String != fingerprint {
		return done, nil // inputs changed: old checkpoints are discarded
	}
	rows, err := s.DB.Writer().QueryContext(ctx, `SELECT step_key FROM job_steps WHERE job_id = ? AND status = 'done'`, j.RetryOf)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	rows.Close() // single writer connection: finish reading before writing
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, k := range keys {
		exID := strings.TrimPrefix(k, "exercise:")
		b, err := s.Root.ReadFile(OutputRel(j.RetryOf, exID))
		if err != nil {
			continue // staging gone: run it again
		}
		if err := s.writeOutput(j.ID, exID, b); err != nil {
			return nil, err
		}
		if _, err := s.DB.Writer().ExecContext(ctx, `INSERT INTO job_steps (job_id, step_key, status, finished_at)
			VALUES (?, ?, 'done', `+nowSQL+`)`, j.ID, k); err != nil {
			return nil, err
		}
		done[exID] = true
	}
	return done, nil
}

func (s *Service) step(ctx context.Context, j jobs.Job, sh shared, pk topics.Pick) (err error) {
	key := StepKey(pk.ExID)
	if _, err := s.DB.Writer().ExecContext(ctx, `UPDATE job_steps SET status = 'running', started_at = `+nowSQL+`,
		error_code = NULL, error_message = NULL WHERE job_id = ? AND step_key = ?`, j.ID, key); err != nil {
		return err
	}
	defer func() { // the step's outcome is recorded even when ctx was cancelled
		bg := context.WithoutCancel(ctx)
		var werr error
		if err != nil {
			_, werr = s.DB.Writer().ExecContext(bg, `UPDATE job_steps SET status = 'failed', error_code = ?, error_message = ?,
				finished_at = `+nowSQL+` WHERE job_id = ? AND step_key = ?`, ai.Code(err), err.Error(), j.ID, key)
		} else {
			_, werr = s.DB.Writer().ExecContext(bg, `UPDATE job_steps SET status = 'done', finished_at = `+nowSQL+`
				WHERE job_id = ? AND step_key = ?`, j.ID, key)
			if werr == nil {
				_, werr = s.DB.Writer().ExecContext(bg, `UPDATE jobs SET progress_done = progress_done + 1 WHERE id = ?`, j.ID)
			}
		}
		err = errors.Join(err, werr)
	}()

	rub, ok := sh.rubric[pk.ExID]
	question := exercises.Section(sh.exercisesMD, pk.ExID)
	if !ok || question == nil {
		return fmt.Errorf("%w: %s missing from rubric or exercises.md", domain.ErrInvalidState, pk.ExID)
	}
	rubricJSON, err := json.MarshalIndent(rub, "", "  ")
	if err != nil {
		return err
	}
	oos := rub.OutOfScopeKnowledge
	if oos == nil {
		oos = []schema.OutOfScopeKnowledge{}
	}
	answerName := "answer" + path.Ext(pk.AnswerRel)
	task, err := json.MarshalIndent(map[string]any{
		"exercise_id": pk.ExID, "title": pk.Title, "type": pk.Type, "language": s.Language,
		"answer_file": "inputs/" + answerName,
		// Repeated here so the evaluator cannot miss it (R3.2, T4.6): never deduct for these.
		"out_of_scope_knowledge": oos,
	}, "", "  ")
	if err != nil {
		return err
	}
	in := sh.in
	in.Step = pk.ExID
	in.Files = map[string][]byte{"task.json": task, "question.md": question, "rubric.json": rubricJSON, answerName: pk.Body}
	req, err := (&prompt.Composer{Root: s.Root}).Compose(in)
	if err != nil {
		return err
	}
	req.Model, req.Timeout, req.StallTimeout = s.Model, s.Timeout, s.Stall

	var out json.RawMessage
	_, err = ai.WatchValid(ctx, s.Adapter, req, nil, func(raw json.RawMessage) error {
		if err := Check(raw, pk.ExID, rub); err != nil {
			return fmt.Errorf("%w: %s: %v", ai.ErrInvalidOutput, pk.ExID, err)
		}
		out = raw
		return nil
	})
	if err != nil {
		return err
	}
	b, err := json.Marshal(Output{ExerciseID: pk.ExID, AnswerSHA256: pk.SHA, Evaluation: out, SkillVersion: req.Skill.Version})
	if err != nil {
		return err
	}
	if err := s.writeOutput(j.ID, pk.ExID, b); err != nil {
		return err
	}
	_, err = s.DB.Writer().ExecContext(ctx, `UPDATE jobs SET skill_version = ? WHERE id = ?`, req.Skill.Version, j.ID)
	return err
}

func (s *Service) writeOutput(jobID int64, exID string, b []byte) error {
	rel := OutputRel(jobID, exID)
	if err := s.Root.MkdirAll(path.Dir(rel), 0o755); err != nil {
		return err
	}
	return s.Root.WriteNew(rel, b, 0o644)
}

// Check validates one AI evaluation against the schema and the exercise's rubric: right exercise,
// every criterion scored exactly once, none above its max_points.
func Check(raw json.RawMessage, exID string, rub schema.RubricExercise) error {
	if err := schema.ValidateAIEvaluationOutput(raw); err != nil {
		return err
	}
	var o struct {
		ExerciseID     string `json:"exercise_id"`
		CriteriaScores []struct {
			CriterionID string `json:"criterion_id"`
			Awarded     int    `json:"awarded"`
		} `json:"criteria_scores"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return err
	}
	if o.ExerciseID != exID {
		return fmt.Errorf("exercise_id is %q, want %q", o.ExerciseID, exID)
	}
	maxPts := map[string]int{}
	for _, c := range rub.Criteria {
		maxPts[c.ID] = c.MaxPoints
	}
	seen := map[string]bool{}
	for _, c := range o.CriteriaScores {
		m, ok := maxPts[c.CriterionID]
		switch {
		case !ok:
			return fmt.Errorf("unknown criterion %q", c.CriterionID)
		case seen[c.CriterionID]:
			return fmt.Errorf("criterion %q scored twice", c.CriterionID)
		case c.Awarded > m:
			return fmt.Errorf("criterion %q awarded %d > max %d", c.CriterionID, c.Awarded, m)
		}
		seen[c.CriterionID] = true
	}
	if len(seen) != len(maxPts) {
		return fmt.Errorf("%d of %d criteria scored", len(seen), len(maxPts))
	}
	return nil
}

func (s *Service) phase(ctx context.Context, id int64, phase string) {
	_, _ = s.DB.Writer().ExecContext(ctx, `UPDATE jobs SET phase = ? WHERE id = ?`, phase, id) // display only
}
