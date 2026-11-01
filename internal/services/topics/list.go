// Package topics serves the topic list (R2.4, T2.6).
package topics

import (
	"context"
	"database/sql"
	"fmt"

	"learninghub/internal/roadmap"
	"learninghub/internal/store"
)

// Filter narrows the list. Empty fields match everything.
type Filter struct {
	Query  string // matched against title_norm after roadmap.Normalize
	Status string // topics.status
}

// Node is a roadmap, a group or a topic. Only roadmaps and groups have Items.
type Node struct {
	Kind  string // "roadmap" | "group" | "topic"
	ID    int64
	Title string
	Items []*Node

	// Topic fields
	Status, State, Origin string
	Score                 sql.NullFloat64 // latest attempt of the current exercise set
	Partial               bool
	Busy                  bool // has a queued or running job
}

type Service struct{ db *store.DB }

func NewService(db *store.DB) *Service { return &Service{db: db} }

// List returns roadmaps → groups → topics in import order, keeping only branches with a matching
// topic. total is the number of topics before filtering, so the page can tell "no match" from "empty".
func (s *Service) List(ctx context.Context, f Filter) (tree []*Node, matched, total int, err error) {
	q := roadmap.Normalize(f.Query)
	if err = s.db.Writer().QueryRowContext(ctx, `SELECT COUNT(*) FROM topics`).Scan(&total); err != nil {
		return nil, 0, 0, err
	}

	type group struct {
		parent sql.NullInt64
		title  string
		node   *Node
	}
	groups := map[int64]*group{}
	grows, err := s.db.Writer().QueryContext(ctx, `SELECT id, parent_id, title FROM groups`)
	if err != nil {
		return nil, 0, 0, err
	}
	for grows.Next() {
		var id int64
		g := &group{}
		if err := grows.Scan(&id, &g.parent, &g.title); err != nil {
			grows.Close()
			return nil, 0, 0, err
		}
		groups[id] = g
	}
	grows.Close()
	if err := grows.Err(); err != nil {
		return nil, 0, 0, err
	}

	rows, err := s.db.Writer().QueryContext(ctx, `
		WITH latest AS (
		  SELECT s.topic_id, a.topic_score, a.is_partial,
		         ROW_NUMBER() OVER (PARTITION BY s.topic_id ORDER BY a.attempt_no DESC) AS rn
		  FROM attempts a JOIN exercise_sets s ON s.id = a.set_id AND s.is_current = 1)
		SELECT r.id, r.title, t.id, t.group_id, t.title, t.status, t.state, t.origin, l.topic_score, COALESCE(l.is_partial, 0),
		       EXISTS (SELECT 1 FROM jobs j WHERE j.topic_id = t.id AND j.status IN ('queued','running'))
		FROM topics t
		JOIN roadmaps r ON r.id = t.roadmap_id
		LEFT JOIN latest l ON l.topic_id = t.id AND l.rn = 1
		WHERE (? = '' OR instr(t.title_norm, ?) > 0) AND (? = '' OR t.status = ?)
		ORDER BY r.id, t.ord`, q, q, f.Status, f.Status)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("listing topics: %w", err)
	}
	defer rows.Close()

	roadmaps := map[int64]*Node{}
	// itemsOf creates a group's node (and its ancestors) at its first matching topic, so sibling
	// order follows the topics' document order, as in the import preview.
	var itemsOf func(rm *Node, gid sql.NullInt64) *[]*Node
	itemsOf = func(rm *Node, gid sql.NullInt64) *[]*Node {
		g, ok := groups[gid.Int64]
		if !gid.Valid || !ok {
			return &rm.Items
		}
		if g.node == nil {
			g.node = &Node{Kind: "group", ID: gid.Int64, Title: g.title}
			parent := itemsOf(rm, g.parent)
			*parent = append(*parent, g.node)
		}
		return &g.node.Items
	}
	for rows.Next() {
		var rid int64
		var rtitle string
		var gid sql.NullInt64
		t := &Node{Kind: "topic"}
		if err := rows.Scan(&rid, &rtitle, &t.ID, &gid, &t.Title, &t.Status, &t.State, &t.Origin, &t.Score, &t.Partial, &t.Busy); err != nil {
			return nil, 0, 0, err
		}
		rm := roadmaps[rid]
		if rm == nil {
			rm = &Node{Kind: "roadmap", ID: rid, Title: rtitle}
			roadmaps[rid] = rm
			tree = append(tree, rm)
		}
		items := itemsOf(rm, gid)
		*items = append(*items, t)
		matched++
	}
	return tree, matched, total, rows.Err()
}
