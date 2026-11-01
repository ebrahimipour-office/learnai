// Package lessons is the lesson job handler (R3.1, TDD §6.6): prepare → ai → validate → commit.
// The AI only returns JSON; this package writes lesson.md, the keywords section and every DB row.
package lessons

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/prompt"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/roadmap"
	"learninghub/internal/schema"
	"learninghub/internal/store"
	"learninghub/skills"
)

const skillName = "lesson-generator"

// Params is jobs.params for a lesson job.
type Params struct {
	ConfirmRegenerate bool `json:"confirm_regenerate"`
}

type Service struct {
	DB       *store.DB
	Root     *fsx.Root
	Adapter  ai.Adapter
	Model    string
	Language string
	Timeout  time.Duration // config jobs.timeout_lesson
	Stall    time.Duration // config jobs.stall_timeout
}

// Output is the AI's structured answer (ai-lesson-output.schema.json).
type Output struct {
	LessonMarkdown string `json:"lesson_markdown"`
	Keywords       []struct {
		Term  string `json:"term"`
		Gloss string `json:"gloss"`
	} `json:"keywords"`
	Suggestions []struct {
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		Reason string `json:"reason"`
	} `json:"suggestions"`
}

type topic struct {
	ID                             int64
	Title, Outline, Group, Roadmap string
	Prev, Next                     string
	Dir                            string
}

type current struct { // the lesson version being replaced; ID 0 = first generation
	ID, Version, FileID int64
}

func lessonRel(dir string) string { return path.Join("topics", dir, "lesson", "lesson.md") }

func historyRel(dir string, version int64) string {
	return path.Join("topics", dir, "lesson", "history", fmt.Sprintf("lesson.%04d.md", version))
}

// Handle is the jobs.Handler for domain.JobKindLesson.
func (s *Service) Handle(ctx context.Context, j jobs.Job) error {
	var p Params
	if err := json.Unmarshal(j.Params, &p); err != nil {
		return fmt.Errorf("%w: params: %v", domain.ErrInvalidInput, err)
	}

	// 1) prepare
	s.phase(ctx, j.ID, "prepare")
	t, err := s.loadTopic(ctx, j.TopicID)
	if err != nil {
		return err
	}
	cur, err := s.current(ctx, t.ID)
	if err != nil {
		return err
	}
	if cur.ID != 0 && !p.ConfirmRegenerate {
		return fmt.Errorf("%w: topic %d already has a lesson; regenerating needs confirm_regenerate", domain.ErrInvalidState, t.ID)
	}
	in, err := s.input(j.ID, t)
	if err != nil {
		return err
	}
	req, err := (&prompt.Composer{Root: s.Root}).Compose(in)
	if err != nil {
		return err
	}
	req.Model, req.Timeout, req.StallTimeout = s.Model, s.Timeout, s.Stall
	if _, err := s.DB.Writer().ExecContext(ctx, `UPDATE jobs SET inputs_fingerprint = ?, adapter = ?, model = ?, skill = ?, skill_version = ? WHERE id = ?`,
		fsx.HashBytes(in.Files["task.json"]), s.Adapter.Name(), s.Model, req.Skill.Name, req.Skill.Version, j.ID); err != nil {
		return err
	}

	// 2) ai + validate
	s.phase(ctx, j.ID, "ai")
	var out Output
	resp, err := ai.WatchValid(ctx, s.Adapter, req, nil, func(raw json.RawMessage) (err error) {
		s.phase(ctx, j.ID, "validate")
		out, err = Validate(raw)
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

	// 3) validate: done inside WatchValid, which retries once on invalid output.
	md := []byte(Render(out))

	// 4) commit
	return j.Commit(ctx, func(ctx context.Context) error {
		return s.commit(ctx, j, req.Skill, t, cur, md, out)
	})
}

func (s *Service) phase(ctx context.Context, id int64, phase string) {
	_, _ = s.DB.Writer().ExecContext(ctx, `UPDATE jobs SET phase = ? WHERE id = ?`, phase, id) // display only
}

func (s *Service) loadTopic(ctx context.Context, id int64) (topic, error) {
	t := topic{ID: id}
	var rm int64
	var ord float64
	err := s.DB.Writer().QueryRowContext(ctx, `SELECT t.title, COALESCE(t.outline, ''), COALESCE(g.title, ''), r.title, r.id, t.ord, t.dir_name
		FROM topics t JOIN roadmaps r ON r.id = t.roadmap_id LEFT JOIN groups g ON g.id = t.group_id WHERE t.id = ?`, id).
		Scan(&t.Title, &t.Outline, &t.Group, &t.Roadmap, &rm, &ord, &t.Dir)
	if errors.Is(err, sql.ErrNoRows) {
		return t, fmt.Errorf("%w: topic %d", domain.ErrNotFound, id)
	}
	if err != nil {
		return t, err
	}
	neighbour := func(q string) (string, error) {
		var title string
		err := s.DB.Writer().QueryRowContext(ctx, q, rm, ord).Scan(&title)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return title, err
	}
	if t.Prev, err = neighbour(`SELECT title FROM topics WHERE roadmap_id = ? AND state = 'active' AND ord < ? ORDER BY ord DESC LIMIT 1`); err != nil {
		return t, err
	}
	t.Next, err = neighbour(`SELECT title FROM topics WHERE roadmap_id = ? AND state = 'active' AND ord > ? ORDER BY ord LIMIT 1`)
	return t, err
}

func (s *Service) current(ctx context.Context, topicID int64) (current, error) {
	var c current
	err := s.DB.Writer().QueryRowContext(ctx, `SELECT id, version, file_id FROM lesson_versions WHERE topic_id = ? AND is_current = 1`, topicID).
		Scan(&c.ID, &c.Version, &c.FileID)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	return c, err
}

// input reads AGENTS.md, profile.md and the skill from the base dir (installed by skills.Install).
func (s *Service) input(jobID int64, t topic) (prompt.Input, error) {
	read := func(rel string) ([]byte, error) {
		b, err := s.Root.ReadFile(rel)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", rel, err)
		}
		return b, nil
	}
	in := prompt.Input{JobID: jobID, Task: ai.TaskLesson}
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
	if in.Schema, err = schema.GetSchemaBytes("ai-lesson-output.schema.json"); err != nil {
		return in, err
	}
	task, err := json.MarshalIndent(map[string]string{
		"topic": t.Title, "group": t.Group, "roadmap": t.Roadmap,
		"previous_topic": t.Prev, "next_topic": t.Next, "language": s.Language,
	}, "", "  ")
	if err != nil {
		return in, err
	}
	excerpt := "# " + t.Title + "\n"
	if t.Outline != "" {
		excerpt += "\n" + t.Outline + "\n"
	}
	in.Files = map[string][]byte{"task.json": task, "roadmap_excerpt.md": []byte(excerpt)}
	return in, nil
}

// Validate checks the AI output against its schema and drops blank keywords/suggestions.
func Validate(raw json.RawMessage) (Output, error) {
	var out Output
	if err := schema.ValidateAILessonOutput(raw); err != nil {
		return out, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	out.LessonMarkdown = strings.TrimSpace(out.LessonMarkdown)
	if out.LessonMarkdown == "" {
		return out, fmt.Errorf("%w: empty lesson_markdown", ai.ErrInvalidOutput)
	}
	kw := out.Keywords[:0]
	for _, k := range out.Keywords {
		if k.Term, k.Gloss = oneLine(k.Term), oneLine(k.Gloss); k.Term != "" {
			kw = append(kw, k)
		}
	}
	out.Keywords = kw
	sg := out.Suggestions[:0]
	for _, x := range out.Suggestions {
		if x.Title, x.Reason = oneLine(x.Title), strings.TrimSpace(x.Reason); x.Title != "" {
			sg = append(sg, x)
		}
	}
	out.Suggestions = sg
	return out, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Render is the lesson file: the AI's markdown, then the keywords section built by the app at the end (R11.2).
func Render(out Output) string {
	var b strings.Builder
	b.WriteString(out.LessonMarkdown)
	b.WriteString("\n")
	if len(out.Keywords) > 0 {
		b.WriteString("\n## کلیدواژه‌ها\n\n")
		for _, k := range out.Keywords {
			b.WriteString("- **" + k.Term + "**")
			if k.Gloss != "" {
				b.WriteString(": " + k.Gloss)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// commit writes the files, then records everything in one transaction. On failure it puts the
// disk back the way it was: the previous lesson.md content is restored and new files are removed.
func (s *Service) commit(ctx context.Context, j jobs.Job, skill ai.SkillRef, t topic, cur current, md []byte, out Output) error {
	rel := lessonRel(t.Dir)
	var prev []byte
	var hist string
	if cur.ID != 0 {
		var err error
		if prev, err = s.Root.ReadFile(rel); err != nil {
			return fmt.Errorf("reading current lesson %s: %w", rel, err)
		}
		hist = historyRel(t.Dir, cur.Version)
	}

	journal := []map[string]string{{"rel_path": rel, "sha256": fsx.HashBytes(md)}}
	if hist != "" {
		journal = append(journal, map[string]string{"rel_path": hist, "sha256": fsx.HashBytes(prev)})
	}
	jb, _ := json.Marshal(journal)
	if _, err := s.DB.Writer().ExecContext(ctx, `UPDATE jobs SET commit_journal = ? WHERE id = ?`, string(jb), j.ID); err != nil {
		return err
	}

	// Keep the old version first; only then replace lesson.md (TDD I1).
	var undo func()
	if hist == "" {
		if err := s.Root.WriteNew(rel, md, 0o644); err != nil {
			return err
		}
		undo = func() { _ = s.Root.Remove(rel) }
	} else {
		removeHist := func() {}
		err := s.Root.WriteNew(hist, prev, 0o644)
		switch {
		case err == nil:
			removeHist = func() { _ = s.Root.Remove(hist) }
		case errors.Is(err, fsx.ErrFileExists):
			// Left by a crashed earlier attempt: fine if it is the same version, never overwrite otherwise.
			if b, rerr := s.Root.ReadFile(hist); rerr != nil || fsx.HashBytes(b) != fsx.HashBytes(prev) {
				return err
			}
		default:
			return err
		}
		if err := s.Root.WriteAtomic(rel, md, 0o644); err != nil {
			removeHist()
			return err
		}
		undo = func() {
			if s.Root.WriteAtomic(rel, prev, 0o644) == nil { // never drop the only copy of the old lesson
				removeHist()
			}
		}
	}

	if err := s.record(ctx, j, skill, t, cur, hist, prev, md, out); err != nil {
		undo()
		return err
	}
	return nil
}

func (s *Service) record(ctx context.Context, j jobs.Job, skill ai.SkillRef, t topic, cur current, hist string, prev, md []byte, out Output) error {
	return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		sha, size := fsx.HashBytes(md), len(md)
		fileID := cur.FileID
		if cur.ID == 0 {
			res, err := tx.ExecContext(ctx, `INSERT INTO files (topic_id, kind, owner_type, rel_path, sha256, size_bytes, observed_sha256)
				VALUES (?, 'lesson', 'lesson_version', ?, ?, ?, ?)`, t.ID, lessonRel(t.Dir), sha, size, sha)
			if err != nil {
				return err
			}
			if fileID, err = res.LastInsertId(); err != nil {
				return err
			}
		} else {
			// The old version moves to its history file; the lesson.md row is reused for the new one.
			ph := fsx.HashBytes(prev)
			res, err := tx.ExecContext(ctx, `INSERT INTO files (topic_id, kind, owner_type, owner_id, rel_path, sha256, size_bytes, observed_sha256)
				VALUES (?, 'lesson', 'lesson_version', ?, ?, ?, ?, ?)`, t.ID, cur.ID, hist, ph, len(prev), ph)
			if err != nil {
				return err
			}
			histID, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE lesson_versions SET is_current = 0, file_id = ? WHERE id = ?`, histID, cur.ID); err != nil {
				return err
			}
		}

		kw, _ := json.Marshal(out.Keywords)
		res, err := tx.ExecContext(ctx, `INSERT INTO lesson_versions (topic_id, version, file_id, keywords, is_current, job_id, skill, skill_version, model)
			VALUES (?, (SELECT COALESCE(MAX(version), 0) + 1 FROM lesson_versions WHERE topic_id = ?), ?, ?, 1, ?, ?, ?, ?)`,
			t.ID, t.ID, fileID, string(kw), j.ID, skill.Name, skill.Version, s.Model)
		if err != nil {
			return err
		}
		verID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET owner_id = ?, sha256 = ?, size_bytes = ?, observed_sha256 = ?,
			observed_mtime = NULL, observed_at = NULL, presence = 'present' WHERE id = ?`, verID, sha, size, sha, fileID); err != nil {
			return err
		}
		for _, x := range out.Suggestions {
			// Same title again for this topic is a duplicate (T6.1 dedupe by title_norm).
			if _, err := tx.ExecContext(ctx, `INSERT INTO suggestions (topic_id, job_id, kind, title, title_norm, reason)
				VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (topic_id, kind, title_norm) DO NOTHING`,
				t.ID, j.ID, x.Kind, x.Title, roadmap.Normalize(x.Title), x.Reason); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE topics SET status = 'content_ready', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE id = ? AND status = 'created'`, t.ID); err != nil {
			return err
		}
		ref, _ := json.Marshal(map[string]int64{"lesson_version_id": verID})
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET result_ref = ? WHERE id = ?`, string(ref), j.ID)
		return err
	})
}
