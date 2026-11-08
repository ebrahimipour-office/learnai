// Package exercises is the exercise-set job handler (R3.2, TDD §6.6): prepare → ai → validate → commit.
// The AI returns JSON only; this package writes exercises.md, manifest.json, rubric-vN.json,
// the answer templates and every DB row.
package exercises

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/prompt"
	"learninghub/internal/config"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/roadmap"
	"learninghub/internal/schema"
	"learninghub/internal/store"
	"learninghub/skills"
)

const skillName = "exercise-generator"

// Params is jobs.params for an exercises job.
type Params struct {
	ConfirmNewVersion bool `json:"confirm_new_version"`
	Count             int  `json:"count"` // 0 = config count_default
}

type Service struct {
	DB        *store.DB
	Root      *fsx.Root
	Adapter   ai.Adapter
	Model     string
	Language  string
	Timeout   time.Duration // config jobs.timeout_exercises
	Stall     time.Duration // config jobs.stall_timeout
	Config    config.ExercisesConfig
	MaxSource int64 // config limits.max_source_bytes
}

type topic struct {
	ID                                    int64
	Title, Roadmap, Dir, StableKey, Group string
	Source                                string // rel path of the chapter's source.md; "" = no source text
}

func SetRel(dir string, v int) string {
	return path.Join("topics", dir, "exercises", fmt.Sprintf("v%d", v))
}
func AnswersRel(dir string, v int) string {
	return path.Join("topics", dir, "answers", fmt.Sprintf("v%d", v))
}
func RubricRel(dir string, v int) string {
	return path.Join("topics", dir, ".internal", fmt.Sprintf("rubric-v%d.json", v))
}

// Handle is the jobs.Handler for domain.JobKindExercises.
func (s *Service) Handle(ctx context.Context, j jobs.Job) error {
	var p Params
	if err := json.Unmarshal(j.Params, &p); err != nil {
		return fmt.Errorf("%w: params: %v", domain.ErrInvalidInput, err)
	}
	count := p.Count
	if count == 0 {
		count = s.Config.CountDefault
	}
	if count < s.Config.CountMin || count > s.Config.CountMax {
		return fmt.Errorf("%w: count %d outside [%d, %d]", domain.ErrInvalidInput, count, s.Config.CountMin, s.Config.CountMax)
	}

	// 1) prepare
	s.phase(ctx, j.ID, "prepare")
	t, err := s.loadTopic(ctx, j.TopicID)
	if err != nil {
		return err
	}
	lessonMD, err := s.currentLesson(ctx, t)
	if err != nil {
		return err
	}
	var curID int64
	var maxVer int
	err = s.DB.Writer().QueryRowContext(ctx, `SELECT COALESCE((SELECT id FROM exercise_sets WHERE topic_id = ? AND is_current = 1), 0),
		COALESCE((SELECT MAX(version) FROM exercise_sets WHERE topic_id = ?), 0)`, t.ID, t.ID).Scan(&curID, &maxVer)
	if err != nil {
		return err
	}
	if curID != 0 && !p.ConfirmNewVersion {
		return fmt.Errorf("%w: topic %d already has an exercise set; a new one needs confirm_new_version", domain.ErrInvalidState, t.ID)
	}
	in, err := s.input(j.ID, t, lessonMD, count)
	if err != nil {
		return err
	}
	req, err := (&prompt.Composer{Root: s.Root}).Compose(in)
	if err != nil {
		return err
	}
	req.Model, req.Timeout, req.StallTimeout = s.Model, s.Timeout, s.Stall
	if _, err := s.DB.Writer().ExecContext(ctx, `UPDATE jobs SET inputs_fingerprint = ?, adapter = ?, model = ?, skill = ?, skill_version = ? WHERE id = ?`,
		fsx.HashBytes(slices.Concat(in.Files["task.json"], lessonMD, in.Files["source.md"])), s.Adapter.Name(), s.Model, req.Skill.Name, req.Skill.Version, j.ID); err != nil {
		return err
	}
	meta := Meta{StableKey: t.StableKey, TopicTitle: t.Title, Version: maxVer + 1,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Generator:   schema.ManifestGenerator{Adapter: s.Adapter.Name(), Model: s.Model, Skill: req.Skill.Name, SkillVersion: req.Skill.Version, JobID: j.ID},
		AllowedExts: s.Config.AllowedAnswerExts, CountMin: s.Config.CountMin, CountMax: s.Config.CountMax}

	// 2) ai + validate (one automatic retry on invalid output)
	s.phase(ctx, j.ID, "ai")
	var set *Set
	resp, err := ai.WatchValid(ctx, s.Adapter, req, nil, func(raw json.RawMessage) (err error) {
		s.phase(ctx, j.ID, "validate")
		set, err = Build(raw, meta)
		return err
	})
	if resp != nil {
		usage, _ := json.Marshal(resp.Usage)
		if _, uerr := s.DB.Writer().ExecContext(context.WithoutCancel(ctx), `UPDATE jobs SET usage = ?, log_dir = ? WHERE id = ?`,
			string(usage), resp.RawLogDir, j.ID); uerr != nil {
			return errors.Join(err, uerr)
		}
	}
	if err != nil {
		return err
	}

	// 3) commit
	return j.Commit(ctx, func(ctx context.Context) error { return s.commit(ctx, j, t, curID, set) })
}

func (s *Service) phase(ctx context.Context, id int64, phase string) {
	_, _ = s.DB.Writer().ExecContext(ctx, `UPDATE jobs SET phase = ? WHERE id = ?`, phase, id) // display only
}

func (s *Service) loadTopic(ctx context.Context, id int64) (topic, error) {
	t := topic{ID: id}
	err := s.DB.Writer().QueryRowContext(ctx, `SELECT t.title, r.title, t.dir_name, t.stable_key, COALESCE(g.title, ''), COALESCE(f.rel_path, '')
		FROM topics t JOIN roadmaps r ON r.id = t.roadmap_id LEFT JOIN groups g ON g.id = t.group_id
		LEFT JOIN files f ON f.id = t.source_file_id WHERE t.id = ?`, id).
		Scan(&t.Title, &t.Roadmap, &t.Dir, &t.StableKey, &t.Group, &t.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return t, fmt.Errorf("%w: topic %d", domain.ErrNotFound, id)
	}
	return t, err
}

// currentLesson reads the lesson the exercises are built on; exercises need one (TDD §6.6 inputs).
func (s *Service) currentLesson(ctx context.Context, t topic) ([]byte, error) {
	var rel string
	err := s.DB.Writer().QueryRowContext(ctx, `SELECT f.rel_path FROM lesson_versions v JOIN files f ON f.id = v.file_id
		WHERE v.topic_id = ? AND v.is_current = 1`, t.ID).Scan(&rel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: topic %d has no lesson yet", domain.ErrInvalidState, t.ID)
	}
	if err != nil {
		return nil, err
	}
	b, err := s.Root.ReadFile(rel)
	if err != nil {
		return nil, fmt.Errorf("reading lesson %s: %w", rel, err)
	}
	return b, nil
}

func (s *Service) input(jobID int64, t topic, lessonMD []byte, count int) (prompt.Input, error) {
	read := func(rel string) ([]byte, error) {
		b, err := s.Root.ReadFile(rel)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", rel, err)
		}
		return b, nil
	}
	in := prompt.Input{JobID: jobID, Task: ai.TaskExercises}
	var err error
	if in.Agents, err = read("AGENTS.md"); err != nil {
		return in, err
	}
	if in.Profile, err = read("profile.md"); err != nil {
		return in, err
	}
	if in.Skill, err = read(skills.SkillRel(skillName)); err != nil {
		return in, err
	}
	if in.Schema, err = schema.GetSchemaBytes("ai-exercises-output.schema.json"); err != nil {
		return in, err
	}
	task, err := json.MarshalIndent(map[string]any{
		"topic": t.Title, "group": t.Group, "roadmap": t.Roadmap, "language": s.Language,
		"exercise_count": count, "allowed_answer_exts": s.Config.AllowedAnswerExts,
		"criteria_points_total": 100,
	}, "", "  ")
	if err != nil {
		return in, err
	}
	in.Files = map[string][]byte{"task.json": task, "lesson.md": lessonMD}
	if t.Source != "" { // TDD §12.8: exercises stay inside the chapter text
		if in.Files["source.md"], err = prompt.ReadSource(s.Root, t.Source, s.MaxSource); err != nil {
			return in, err
		}
	}
	return in, nil
}

type written struct{ rel, sha string }

// commit writes every file with WriteNew (never overwrite, R3.3), then records it all in one
// transaction. Any failure removes the files this job created; files that were already there
// make the whole commit fail and are left untouched.
func (s *Service) commit(ctx context.Context, j jobs.Job, t topic, curID int64, set *Set) error {
	v := set.Manifest.Set.Version
	files := []struct {
		rel, kind string
		b         []byte
	}{
		{path.Join(SetRel(t.Dir, v), "exercises.md"), "exercises_md", set.ExercisesMD},
		{path.Join(SetRel(t.Dir, v), "manifest.json"), "manifest", set.ManifestJSON},
		{RubricRel(t.Dir, v), "rubric", set.RubricJSON},
	}
	for _, a := range set.Answers {
		files = append(files, struct {
			rel, kind string
			b         []byte
		}{path.Join(AnswersRel(t.Dir, v), a.FileName), "answer", a.Content})
	}

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
	for _, f := range files {
		if err := s.Root.MkdirAll(path.Dir(f.rel), 0o755); err != nil {
			undo()
			return err
		}
		if err := s.Root.WriteNew(f.rel, f.b, 0o644); err != nil {
			undo()
			return fmt.Errorf("writing %s: %w", f.rel, err)
		}
		done = append(done, f.rel)
	}

	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		ids := make([]int64, len(files))
		for i, f := range files {
			immutable := 0
			if f.kind == "answer" {
				immutable = 1
			}
			sha := fsx.HashBytes(f.b)
			res, err := tx.ExecContext(ctx, `INSERT INTO files (topic_id, kind, owner_type, rel_path, sha256, size_bytes, immutable, observed_sha256)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, t.ID, f.kind, map[bool]string{true: "exercise", false: "exercise_set"}[f.kind == "answer"],
				f.rel, sha, len(f.b), immutable, sha)
			if err != nil {
				return err
			}
			if ids[i], err = res.LastInsertId(); err != nil {
				return err
			}
		}
		if curID != 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE exercise_sets SET is_current = 0 WHERE id = ?`, curID); err != nil {
				return err
			}
		}
		g := set.Manifest.Set.Generator
		res, err := tx.ExecContext(ctx, `INSERT INTO exercise_sets (topic_id, version, exercises_file, manifest_file, rubric_file, rubric_version,
			is_current, job_id, skill, skill_version, model) VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?, ?, ?)`,
			t.ID, v, ids[0], ids[1], ids[2], j.ID, g.Skill, g.SkillVersion, g.Model)
		if err != nil {
			return err
		}
		setID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET owner_id = ? WHERE id IN (?, ?, ?)`, setID, ids[0], ids[1], ids[2]); err != nil {
			return err
		}
		if curID != 0 { // a replaced set means the user confirmed confirm_new_version (R3.2)
			if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log (action, target_type, target_id, detail)
				SELECT 'new_exercise_set', 'exercise_set', ?, json_object('topic_id', ?, 'job_id', ?, 'version', ?,
					'previous_set_id', id, 'previous_version', version) FROM exercise_sets WHERE id = ?`,
				setID, t.ID, j.ID, v, curID); err != nil {
				return err
			}
		}
		for i, ex := range set.Manifest.Exercises {
			fileID, content := ids[3+i], set.Answers[i].Content
			deps, _ := json.Marshal(ex.ExternalKnowledge)
			if ex.ExternalKnowledge == nil {
				deps = []byte("[]")
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO exercises (set_id, ex_id, ord, title, type, difficulty, weight, answer_file_id, template_sha256,
				template_norm_sha256, external_deps) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, setID, ex.ID, ex.Order, ex.Title, ex.Type,
				ex.Difficulty, ex.Weight, fileID, fsx.HashBytes(content), fsx.NormalizedAnswerSHA(content), string(deps))
			if err != nil {
				return err
			}
			exID, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE files SET owner_id = ? WHERE id = ?`, exID, fileID); err != nil {
				return err
			}
		}
		for _, x := range set.Out.Suggestions {
			title := oneLine(x.Title)
			if title == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO suggestions (topic_id, job_id, kind, title, title_norm, reason)
				VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (topic_id, kind, title_norm) DO NOTHING`,
				t.ID, j.ID, x.Kind, title, roadmap.Normalize(title), x.Reason); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE topics SET status = 'exercises_ready', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE id = ? AND status IN ('created', 'content_ready')`, t.ID); err != nil {
			return err
		}
		ref, _ := json.Marshal(map[string]int64{"exercise_set_id": setID})
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET result_ref = ? WHERE id = ?`, string(ref), j.ID)
		return err
	})
	if err != nil {
		undo()
	}
	return err
}
