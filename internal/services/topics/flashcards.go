package topics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"learninghub/internal/domain"
)

// dbTime is the format of the DB's strftime('%Y-%m-%dT%H:%M:%fZ'), so due_at compares as text.
const dbTime = "2006-01-02T15:04:05.000Z"

// MaxBox is the last Leitner box.
const MaxBox = 5

// NextBox is the Leitner step (T11.3): known ⇒ one box up (capped at MaxBox), unknown ⇒ box 1.
// Box b is due again after 2^(b-1) days: 1, 2, 4, 8, 16.
func NextBox(box int, knew bool) (int, time.Duration) {
	if knew {
		box = min(box+1, MaxBox)
	} else {
		box = 1
	}
	return box, time.Duration(1<<(box-1)) * 24 * time.Hour
}

// Flashcard is one review card of the current lesson; Front and Back are raw Markdown.
type Flashcard struct {
	ID          int64
	Front, Back string
	Box         int
}

// Deck is the lesson tab's card section.
type Deck struct {
	Keywords int         // keywords of the current lesson; 0 = cards cannot be built
	Total    int         // cards of the current lesson version
	Due      []Flashcard // due at now, oldest due first
}

// Deck returns the cards of the topic's current lesson version. Cards of older versions stay in the
// table but are not shown.
func (s *Service) Deck(ctx context.Context, topicID int64, now time.Time) (*Deck, error) {
	d := &Deck{}
	var vid int64
	err := s.db.Reader().QueryRowContext(ctx, `SELECT id, COALESCE(json_array_length(keywords), 0) FROM lesson_versions
		WHERE topic_id = ? AND is_current = 1`, topicID).Scan(&vid, &d.Keywords)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT id, front, back, box, due_at <= ? FROM flashcards
		WHERE lesson_version_id = ? ORDER BY due_at, id`, now.UTC().Format(dbTime), vid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Flashcard
		var due bool
		if err := rows.Scan(&c.ID, &c.Front, &c.Back, &c.Box, &due); err != nil {
			return nil, err
		}
		d.Total++
		if due {
			d.Due = append(d.Due, c)
		}
	}
	return d, rows.Err()
}

// GradeCard moves a due-or-not card of the topic's current lesson to its next Leitner box.
// A card of another topic or an older lesson version is domain.ErrNotFound. seenBox is the box the
// page showed: a card no longer in it was already graded (a double-click), so this grade is a no-op.
func (s *Service) GradeCard(ctx context.Context, topicID, cardID int64, seenBox int, knew bool, now time.Time) error {
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var box int
		err := tx.QueryRowContext(ctx, `SELECT f.box FROM flashcards f JOIN lesson_versions lv ON lv.id = f.lesson_version_id AND lv.is_current = 1
			WHERE f.id = ? AND f.topic_id = ?`, cardID, topicID).Scan(&box)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: card %d", domain.ErrNotFound, cardID)
		}
		if err != nil {
			return err
		}
		if box != seenBox {
			return nil
		}
		box, wait := NextBox(box, knew)
		_, err = tx.ExecContext(ctx, `UPDATE flashcards SET box = ?, due_at = ? WHERE id = ?`, box, now.Add(wait).UTC().Format(dbTime), cardID)
		return err
	})
}

// DueTopic is an active topic with due cards of its current lesson, for «برای مرور» on /topics.
type DueTopic struct {
	ID    int64
	Title string
	N     int
}

// DueCards lists active topics with cards due at now, most due first.
func (s *Service) DueCards(ctx context.Context, now time.Time) ([]DueTopic, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT t.id, t.title, COUNT(*) FROM flashcards f
		JOIN lesson_versions lv ON lv.id = f.lesson_version_id AND lv.is_current = 1
		JOIN topics t ON t.id = f.topic_id AND t.state = 'active'
		WHERE f.due_at <= ? GROUP BY t.id ORDER BY COUNT(*) DESC, t.id`, now.UTC().Format(dbTime))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueTopic
	for rows.Next() {
		var d DueTopic
		if err := rows.Scan(&d.ID, &d.Title, &d.N); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
