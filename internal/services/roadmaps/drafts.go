// Package roadmaps holds roadmap import: drafts and preview (T2.4), later confirm and diff.
package roadmaps

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"learninghub/internal/fsx"
	"learninghub/internal/roadmap"
	"learninghub/internal/store"
)

const (
	MaxSourceBytes = 1 << 20 // R2.2: uploads up to 1 MB
	DraftTTL       = 24 * time.Hour
	tsFormat       = "2006-01-02T15:04:05.000Z" // same shape as SQLite strftime('%Y-%m-%dT%H:%M:%fZ')
)

var (
	ErrDraftNotFound = errors.New("draft not found or expired")
	ErrTooLarge      = errors.New("roadmap file is larger than 1 MB")
	ErrInvalidSource = errors.New("invalid roadmap file")
)

// Choice is the user's selection in the preview, stored as import_drafts.selection.
type Choice struct {
	Rule     roadmap.Rule `json:"rule"`
	Excluded []string     `json:"excluded,omitempty"` // stable keys of unticked topics
}

// Draft is an import preview. It never touches roadmaps, groups, topics or the file system.
type Draft struct {
	ID         string
	SourceName string
	Content    []byte
	Choice     Choice
	ExpiresAt  time.Time
}

// Preview applies c to the draft content. Parsing is deterministic, so the content is the source of truth.
func (d *Draft) Preview(c Choice) (*roadmap.Selection, error) {
	return roadmap.Select(roadmap.Parse(d.Content), c.Rule)
}

type Service struct {
	db  *store.DB
	Now func() time.Time // replaceable in tests
}

func NewService(db *store.DB) *Service {
	return &Service{db: db, Now: time.Now}
}

func (s *Service) ts(t time.Time) string { return t.UTC().Format(tsFormat) }

// CreateDraft validates an uploaded roadmap and stores it as a draft with the default rule.
func (s *Service) CreateDraft(ctx context.Context, name string, content []byte) (string, error) {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case len(content) > MaxSourceBytes:
		return "", ErrTooLarge
	case ext != ".md" && ext != ".markdown":
		return "", fmt.Errorf("%w: only .md files are accepted", ErrInvalidSource)
	case len(name) > 255:
		return "", fmt.Errorf("%w: file name too long", ErrInvalidSource)
	case len(strings.TrimSpace(string(content))) == 0:
		return "", fmt.Errorf("%w: file is empty", ErrInvalidSource)
	case !utf8.Valid(content):
		return "", fmt.Errorf("%w: file is not valid UTF-8", ErrInvalidSource)
	}

	tree, err := json.Marshal(roadmap.Parse(content))
	if err != nil {
		return "", err
	}
	choice, err := json.Marshal(Choice{Rule: roadmap.Rule{Kind: roadmap.RuleDeepest}})
	if err != nil {
		return "", err
	}
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(idBytes[:])

	if _, err := s.PurgeExpired(ctx); err != nil {
		return "", err
	}
	_, err = s.db.Writer().ExecContext(ctx,
		`INSERT INTO import_drafts (id, source_name, content, content_sha, parsed_tree, selection, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, name, string(content), fsx.HashBytes(content), string(tree), string(choice), s.ts(s.Now().Add(DraftTTL)))
	if err != nil {
		return "", fmt.Errorf("inserting draft: %w", err)
	}
	return id, nil
}

// Draft loads a draft that has not expired.
func (s *Service) Draft(ctx context.Context, id string) (*Draft, error) {
	var content, choice, expires string
	d := &Draft{ID: id}
	err := s.db.Writer().QueryRowContext(ctx,
		`SELECT source_name, content, selection, expires_at FROM import_drafts WHERE id = ? AND expires_at > ?`,
		id, s.ts(s.Now())).Scan(&d.SourceName, &content, &choice, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDraftNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(choice), &d.Choice); err != nil {
		return nil, fmt.Errorf("decoding draft selection: %w", err)
	}
	d.Content = []byte(content)
	d.ExpiresAt, _ = time.Parse(tsFormat, expires)
	return d, nil
}

// SetChoice validates c against the draft and stores it.
func (s *Service) SetChoice(ctx context.Context, d *Draft, c Choice) error {
	if _, err := d.Preview(c); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSource, err)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	res, err := s.db.Writer().ExecContext(ctx,
		`UPDATE import_drafts SET selection = ? WHERE id = ? AND expires_at > ?`, string(raw), d.ID, s.ts(s.Now()))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDraftNotFound
	}
	d.Choice = c
	return nil
}

// PurgeExpired deletes drafts older than DraftTTL.
func (s *Service) PurgeExpired(ctx context.Context) (int64, error) {
	res, err := s.db.Writer().ExecContext(ctx, `DELETE FROM import_drafts WHERE expires_at <= ?`, s.ts(s.Now()))
	if err != nil {
		return 0, fmt.Errorf("purging expired drafts: %w", err)
	}
	return res.RowsAffected()
}
