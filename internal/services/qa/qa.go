// Package qa is the lesson question job handler (T10.21): the learner asks about the current lesson,
// the AI returns {answer_markdown} and this package stores it in lesson_qa. No file is written.
package qa

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"learninghub/internal/ai"
	"learninghub/internal/ai/prompt"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/schema"
	"learninghub/internal/store"
	"learninghub/skills"
)

const skillName = "lesson-qa"

// MaxQuestion is the longest question accepted, in characters.
const MaxQuestion = 2000

// Params is jobs.params for a qa job.
type Params struct {
	Question string `json:"question"`
}

// CheckQuestion trims q and rejects an empty or over-long question.
func CheckQuestion(q string) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" || utf8.RuneCountInString(q) > MaxQuestion {
		return q, fmt.Errorf("%w: question must be 1 to %d characters", domain.ErrInvalidInput, MaxQuestion)
	}
	return q, nil
}

type Service struct {
	DB        *store.DB
	Root      *fsx.Root
	Adapter   ai.Adapter
	Model     string
	Language  string
	Timeout   time.Duration // config jobs.timeout_lesson
	Stall     time.Duration // config jobs.stall_timeout
	MaxSource int64         // config limits.max_source_bytes; also caps lesson.md
}

// Handle is the jobs.Handler for domain.JobKindQA.
func (s *Service) Handle(ctx context.Context, j jobs.Job) error {
	var p Params
	if err := json.Unmarshal(j.Params, &p); err != nil {
		return fmt.Errorf("%w: params: %v", domain.ErrInvalidInput, err)
	}
	q, err := CheckQuestion(p.Question)
	if err != nil {
		return err
	}
	db := s.DB.Writer()
	_, _ = db.ExecContext(ctx, `UPDATE jobs SET phase = 'prepare' WHERE id = ?`, j.ID)
	var title, lessonRel, sourceRel string
	var versionID int64
	err = db.QueryRowContext(ctx, `SELECT t.title, lv.id, lf.rel_path, COALESCE(sf.rel_path, '')
		FROM topics t JOIN lesson_versions lv ON lv.topic_id = t.id AND lv.is_current = 1 JOIN files lf ON lf.id = lv.file_id
		LEFT JOIN files sf ON sf.id = t.source_file_id WHERE t.id = ?`, j.TopicID).Scan(&title, &versionID, &lessonRel, &sourceRel)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: topic %d has no lesson", domain.ErrInvalidState, j.TopicID)
	}
	if err != nil {
		return err
	}

	in := prompt.Input{JobID: j.ID, Task: ai.TaskQA, Files: map[string][]byte{"question.md": []byte(q)}}
	for _, f := range []struct {
		dst *[]byte
		rel string
	}{{&in.Agents, "AGENTS.md"}, {&in.Profile, "profile.md"}, {&in.Skill, skills.SkillRel(skillName)}} {
		if *f.dst, err = s.Root.ReadFile(f.rel); err != nil {
			return fmt.Errorf("reading %s: %w", f.rel, err)
		}
	}
	if in.Schema, err = schema.GetSchemaBytes("ai-qa-output.schema.json"); err != nil {
		return err
	}
	// The current bytes on disk: the user may have edited lesson.md or source.md.
	if in.Files["lesson.md"], err = prompt.ReadSource(s.Root, lessonRel, s.MaxSource); err != nil {
		return err
	}
	if sourceRel != "" {
		if in.Files["source.md"], err = prompt.ReadSource(s.Root, sourceRel, s.MaxSource); err != nil {
			return err
		}
	}
	if in.Files["task.json"], err = json.MarshalIndent(map[string]string{"topic": title, "language": s.Language}, "", "  "); err != nil {
		return err
	}
	req, err := (&prompt.Composer{Root: s.Root}).Compose(in)
	if err != nil {
		return err
	}
	model := ai.ModelFor(s.Adapter, s.Model)
	req.Model, req.Timeout, req.StallTimeout = model, s.Timeout, s.Stall
	inputs, _ := json.Marshal(in.Files) // sorted keys: a stable fingerprint
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET phase = 'ai', inputs_fingerprint = ?, adapter = ?, model = ?, skill = ?, skill_version = ? WHERE id = ?`,
		fsx.HashBytes(inputs), s.Adapter.Name(), model, req.Skill.Name, req.Skill.Version, j.ID); err != nil {
		return err
	}

	var answer string
	resp, err := ai.WatchValid(ctx, s.Adapter, req, nil, func(raw json.RawMessage) (err error) {
		answer, err = Validate(raw)
		return err
	})
	if resp != nil {
		usage, _ := json.Marshal(resp.Usage)
		if _, uerr := db.ExecContext(context.WithoutCancel(ctx), `UPDATE jobs SET usage = ?, log_dir = ? WHERE id = ?`,
			string(usage), resp.RawLogDir, j.ID); uerr != nil {
			return errors.Join(err, uerr)
		}
	}
	if err != nil {
		return err
	}

	return j.Commit(ctx, func(ctx context.Context) error {
		return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `INSERT INTO lesson_qa (topic_id, lesson_version_id, question, answer_md, job_id) VALUES (?, ?, ?, ?, ?)`,
				j.TopicID, versionID, q, answer, j.ID)
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			ref, _ := json.Marshal(map[string]int64{"lesson_qa_id": id})
			_, err = tx.ExecContext(ctx, `UPDATE jobs SET result_ref = ? WHERE id = ?`, string(ref), j.ID)
			return err
		})
	})
}

// Validate checks the AI output against its schema and returns the trimmed, non-empty answer.
func Validate(raw json.RawMessage) (string, error) {
	if err := schema.ValidateAIQAOutput(raw); err != nil {
		return "", fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	var out struct {
		Answer string `json:"answer_markdown"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	if out.Answer = strings.TrimSpace(out.Answer); out.Answer == "" {
		return "", fmt.Errorf("%w: empty answer_markdown", ai.ErrInvalidOutput)
	}
	return out.Answer, nil
}
