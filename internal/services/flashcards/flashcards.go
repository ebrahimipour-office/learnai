// Package flashcards is the review card job handler (T11.3): the AI turns the current lesson's keywords into
// {cards:[{front, back}]} and this package stores them in the flashcards table. No file is written.
package flashcards

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/prompt"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/schema"
	"learninghub/internal/store"
	"learninghub/skills"
)

const skillName = "flashcard-generator"

// Card is one review card; both sides are Markdown.
type Card struct {
	Front string `json:"front"`
	Back  string `json:"back"`
}

type Service struct {
	DB        *store.DB
	Root      *fsx.Root
	Adapter   ai.Adapter
	Model     string
	Language  string
	Timeout   time.Duration // config jobs.timeout_lesson
	Stall     time.Duration // config jobs.stall_timeout
	MaxSource int64         // config limits.max_source_bytes; caps lesson.md
}

// Handle is the jobs.Handler for domain.JobKindFlashcards.
func (s *Service) Handle(ctx context.Context, j jobs.Job) error {
	db := s.DB.Writer()
	_, _ = db.ExecContext(ctx, `UPDATE jobs SET phase = 'prepare' WHERE id = ?`, j.ID)
	var title, lessonRel, keywords string
	var versionID int64
	err := db.QueryRowContext(ctx, `SELECT t.title, lv.id, lf.rel_path, lv.keywords
		FROM topics t JOIN lesson_versions lv ON lv.topic_id = t.id AND lv.is_current = 1 JOIN files lf ON lf.id = lv.file_id
		WHERE t.id = ?`, j.TopicID).Scan(&title, &versionID, &lessonRel, &keywords)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: topic %d has no lesson", domain.ErrInvalidState, j.TopicID)
	}
	if err != nil {
		return err
	}
	var kw []json.RawMessage
	if json.Unmarshal([]byte(keywords), &kw) != nil || len(kw) == 0 {
		return fmt.Errorf("%w: the current lesson has no keywords", domain.ErrInvalidState)
	}

	in := prompt.Input{JobID: j.ID, Task: ai.TaskFlashcards, Files: map[string][]byte{"keywords.json": []byte(keywords)}}
	for _, f := range []struct {
		dst *[]byte
		rel string
	}{{&in.Agents, "AGENTS.md"}, {&in.Profile, "profile.md"}, {&in.Skill, skills.SkillRel(skillName)}} {
		if *f.dst, err = s.Root.ReadFile(f.rel); err != nil {
			return fmt.Errorf("reading %s: %w", f.rel, err)
		}
	}
	if in.Schema, err = schema.GetSchemaBytes("ai-flashcards-output.schema.json"); err != nil {
		return err
	}
	if in.Files["lesson.md"], err = prompt.ReadSource(s.Root, lessonRel, s.MaxSource); err != nil {
		return err
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
	inputs, _ := json.Marshal(in.Files)
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET phase = 'ai', inputs_fingerprint = ?, adapter = ?, model = ?, skill = ?, skill_version = ? WHERE id = ?`,
		fsx.HashBytes(inputs), s.Adapter.Name(), model, req.Skill.Name, req.Skill.Version, j.ID); err != nil {
		return err
	}

	var cards []Card
	resp, err := ai.WatchValid(ctx, s.Adapter, req, nil, func(raw json.RawMessage) (err error) {
		cards, err = Validate(raw)
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
			for _, c := range cards {
				if _, err := tx.ExecContext(ctx, `INSERT INTO flashcards (topic_id, lesson_version_id, front, back, job_id) VALUES (?, ?, ?, ?, ?)`,
					j.TopicID, versionID, c.Front, c.Back, j.ID); err != nil {
					return err
				}
			}
			ref, _ := json.Marshal(map[string]int64{"lesson_version_id": versionID, "cards": int64(len(cards))})
			_, err := tx.ExecContext(ctx, `UPDATE jobs SET result_ref = ? WHERE id = ?`, string(ref), j.ID)
			return err
		})
	})
}

// Validate checks the AI output against its schema, trims both sides and drops cards with a blank side;
// no card left is invalid output.
func Validate(raw json.RawMessage) ([]Card, error) {
	if err := schema.ValidateAIFlashcardsOutput(raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	var out struct {
		Cards []Card `json:"cards"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	var cards []Card
	for _, c := range out.Cards {
		c.Front, c.Back = strings.TrimSpace(c.Front), strings.TrimSpace(c.Back)
		if c.Front != "" && c.Back != "" {
			cards = append(cards, c)
		}
	}
	if len(cards) == 0 {
		return nil, fmt.Errorf("%w: no card with both sides", ai.ErrInvalidOutput)
	}
	return cards, nil
}
