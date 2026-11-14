package roadmaps

import (
	"context"
	"database/sql"

	"learninghub/internal/roadmap"
)

// DiffItem is one row of the re-import report.
type DiffItem struct {
	Key, Title string
	OldTitle   string // Changed: the recorded title when it differs
	Restored   bool   // Changed: the topic was archived and comes back
	Guess      string // New: title of a similar disappeared topic (suggestion only, RK8)
}

// Diff compares the kept topics of a re-import draft with the roadmap's recorded topics (R2.5).
// Gone topics are archived on confirm, never deleted.
type Diff struct {
	New, Changed, Gone []DiffItem
	Same               int
	GuessSkipped       bool // too many new × gone pairs to guess within the time budget
}

// oldTopic is a recorded roadmap topic (origin 'roadmap'; suggested topics are never touched by re-import).
type oldTopic struct {
	ID                    int64
	Key, Title, TitleNorm string
	Outline, State        string
	Dir, SourceSHA        string        // dir_name; source_sha256 ("" = no source.md yet)
	SourceFile            sql.NullInt64 // source_file_id
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func recordedTopics(ctx context.Context, q querier, roadmapID int64) ([]oldTopic, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, stable_key, title, title_norm, COALESCE(outline, ''), state,
		dir_name, COALESCE(source_sha256, ''), source_file_id FROM topics WHERE roadmap_id = ? AND origin = 'roadmap' ORDER BY ord, id`, roadmapID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []oldTopic
	for rows.Next() {
		var o oldTopic
		if err := rows.Scan(&o.ID, &o.Key, &o.Title, &o.TitleNorm, &o.Outline, &o.State, &o.Dir, &o.SourceSHA, &o.SourceFile); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Diff reports what confirming d would change; nil for a draft that imports a new roadmap.
func (s *Service) Diff(ctx context.Context, d *Draft) (*Diff, error) {
	if d.RoadmapID == 0 {
		return nil, nil
	}
	sel, err := d.Preview(d.Choice)
	if err != nil {
		return nil, err
	}
	old, err := recordedTopics(ctx, s.db.Writer(), d.RoadmapID)
	if err != nil {
		return nil, err
	}
	return diffTopics(KeptTopics(sel, d.Choice.Excluded), old), nil
}

// diffTopics matches by stable key. Already-archived topics that stay absent are not reported.
func diffTopics(kept []roadmap.Topic, old []oldTopic) *Diff {
	d := &Diff{}
	byKey := make(map[string]oldTopic, len(old))
	for _, o := range old {
		byKey[o.Key] = o
	}
	seen := map[string]bool{}
	for _, t := range kept {
		seen[t.StableKey] = true
		o, ok := byKey[t.StableKey]
		switch {
		case !ok:
			d.New = append(d.New, DiffItem{Key: t.StableKey, Title: t.Title})
		case o.State == "archived" || o.Title != t.Title || o.Outline != t.Outline:
			it := DiffItem{Key: t.StableKey, Title: t.Title, Restored: o.State == "archived"}
			if o.Title != t.Title {
				it.OldTitle = o.Title
			}
			d.Changed = append(d.Changed, it)
		default:
			d.Same++
		}
	}
	var gone []oldTopic
	for _, o := range old {
		if !seen[o.Key] && o.State == "active" {
			gone = append(gone, o)
			d.Gone = append(d.Gone, DiffItem{Key: o.Key, Title: o.Title})
		}
	}
	if len(d.New)*len(gone) > guessMaxPairs {
		d.GuessSkipped = len(gone) > 0
		return d
	}
	goneRunes := make([][]rune, len(gone))
	for i, o := range gone {
		goneRunes[i] = capRunes(o.TitleNorm)
	}
	for i := range d.New {
		d.New[i].Guess = guess(capRunes(roadmap.Normalize(d.New[i].Title)), gone, goneRunes)
	}
	return d
}

// guessMin is the similarity a disappeared title needs to be offered as "probably the same topic".
const guessMin = 0.6

// Guess bounds (CR-17): beyond guessMaxPairs the preview shows a note instead of guesses.
const (
	guessMaxPairs = 250_000
	guessMaxRunes = 200
)

func capRunes(s string) []rune {
	r := []rune(s)
	return r[:min(len(r), guessMaxRunes)]
}

// guess returns the most similar gone title. A renamed group keeps title_norm equal (similarity 1).
func guess(norm []rune, gone []oldTopic, goneRunes [][]rune) string {
	best, bestSim := "", guessMin
	for i, o := range gone {
		// Distance >= length gap, so similarity <= shorter/longer: skip pairs that cannot win.
		lo, hi := min(len(norm), len(goneRunes[i])), max(len(norm), len(goneRunes[i]))
		if hi > 0 && float64(lo)/float64(hi) < bestSim {
			continue
		}
		if s := similarity(norm, goneRunes[i]); s >= bestSim {
			best, bestSim = o.Title, s
		}
	}
	return best
}

// similarity is 1 - Levenshtein distance / longer length, over runes.
func similarity(ra, rb []rune) float64 {
	n := max(len(ra), len(rb))
	if n == 0 {
		return 1
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return 1 - float64(prev[len(rb)])/float64(n)
}

// KeptTopics drops the topics the user unticked. Parent is re-pointed into the returned slice: an
// unticked parent is skipped like an unselected heading, so its child hangs from the next kept one up.
func KeptTopics(sel *roadmap.Selection, excludedKeys []string) []roadmap.Topic {
	excluded := map[string]bool{}
	for _, k := range excludedKeys {
		excluded[k] = true
	}
	var kept []roadmap.Topic
	at := make([]int, len(sel.Topics)) // sel index -> kept index, -1 when unticked
	for i, t := range sel.Topics {
		at[i] = -1
		if excluded[t.StableKey] {
			continue
		}
		p := t.Parent
		for p >= 0 && at[p] < 0 { // parents precede children, so at[p] is already set
			p = sel.Topics[p].Parent
		}
		if p >= 0 {
			p = at[p]
		}
		t.Parent = p
		at[i] = len(kept)
		kept = append(kept, t)
	}
	return kept
}
