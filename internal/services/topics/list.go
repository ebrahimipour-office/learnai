// Package topics serves the topic list (R2.4, T2.6).
package topics

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"learninghub/internal/fsx"
	"learninghub/internal/roadmap"
	"learninghub/internal/store"
)

// Filter narrows the list. Empty fields match everything.
type Filter struct {
	Query    string // matched against title_norm after roadmap.Normalize
	Status   string // topics.status
	Archived bool   // also list archived topics (T9.12); hidden by default
}

// Node is a roadmap, a group or a topic. A topic's Items are its tree-rule children (§13.6).
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
	Missing               int  // registered files last seen missing by sync

	// Roadmap fields (T9.8): over all active topics of the roadmap, whatever the filter
	Done, Total int             // evaluated / all active topics
	Avg         sql.NullFloat64 // mean latest score of the topics that have one
	NextID      int64           // first active topic in roadmap order not yet evaluated; 0 = all done
	NextTitle   string
}

// latestAttempt ranks each topic's attempts on its current exercise set, newest first (rn = 1).
const latestAttempt = `
		WITH latest AS (
		  SELECT s.topic_id, a.topic_score, a.is_partial,
		         ROW_NUMBER() OVER (PARTITION BY s.topic_id ORDER BY a.attempt_no DESC) AS rn
		  FROM attempts a JOIN exercise_sets s ON s.id = a.set_id AND s.is_current = 1)`

type Service struct {
	db   *store.DB
	root *fsx.Root // topics/ folders, for NextTopicSeq on suggestion accept
}

func NewService(db *store.DB, root *fsx.Root) *Service { return &Service{db: db, root: root} }

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

	var matchingGroups map[int64]bool
	if q != "" {
		matchingGroups = make(map[int64]bool)
		for id, g := range groups {
			if strings.Contains(roadmap.Normalize(g.title), q) {
				matchingGroups[id] = true
			}
		}
		changed := true
		for changed {
			changed = false
			for id, g := range groups {
				if !matchingGroups[id] && g.parent.Valid && matchingGroups[g.parent.Int64] {
					matchingGroups[id] = true
					changed = true
				}
			}
		}
	}

	baseQuery := latestAttempt + `
		SELECT r.id, r.title, t.id, t.group_id, t.parent_id, t.title, t.status, t.state, t.origin, l.topic_score, COALESCE(l.is_partial, 0),
		       EXISTS (SELECT 1 FROM jobs j WHERE j.topic_id = t.id AND j.status IN ('queued','running')),
		       (SELECT COUNT(*) FROM files f WHERE f.topic_id = t.id AND f.presence = 'missing')
		FROM topics t
		JOIN roadmaps r ON r.id = t.roadmap_id
		LEFT JOIN latest l ON l.topic_id = t.id AND l.rn = 1
		WHERE `

	var whereClauses []string
	var args []any

	if q != "" {
		if len(matchingGroups) > 0 {
			placeholders := make([]string, len(matchingGroups))
			matchGroupArgs := make([]any, len(matchingGroups))
			idx := 0
			for gid := range matchingGroups {
				placeholders[idx] = "?"
				matchGroupArgs[idx] = gid
				idx++
			}
			whereClauses = append(whereClauses, fmt.Sprintf("(instr(t.title_norm, ?) > 0 OR t.group_id IN (%s))", strings.Join(placeholders, ",")))
			args = append(args, q)
			args = append(args, matchGroupArgs...)
		} else {
			whereClauses = append(whereClauses, "instr(t.title_norm, ?) > 0")
			args = append(args, q)
		}
	}
	if !f.Archived {
		whereClauses = append(whereClauses, "t.state = 'active'")
	}
	if f.Status != "" {
		whereClauses = append(whereClauses, "t.status = ?")
		args = append(args, f.Status)
	}

	if len(whereClauses) == 0 {
		baseQuery += "1=1 "
	} else {
		baseQuery += strings.Join(whereClauses, " AND ") + " "
	}
	baseQuery += "ORDER BY r.id, t.ord"

	rows, err := s.db.Writer().QueryContext(ctx, baseQuery, args...)
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
	shown := map[int64]*Node{} // parents precede children in ord; a filtered-out parent leaves the child under its group
	for rows.Next() {
		var rid int64
		var rtitle string
		var gid, pid sql.NullInt64
		t := &Node{Kind: "topic"}
		if err := rows.Scan(&rid, &rtitle, &t.ID, &gid, &pid, &t.Title, &t.Status, &t.State, &t.Origin, &t.Score, &t.Partial, &t.Busy, &t.Missing); err != nil {
			return nil, 0, 0, err
		}
		rm := roadmaps[rid]
		if rm == nil {
			rm = &Node{Kind: "roadmap", ID: rid, Title: rtitle}
			roadmaps[rid] = rm
			tree = append(tree, rm)
		}
		items := itemsOf(rm, gid)
		if p := shown[pid.Int64]; pid.Valid && p != nil {
			items = &p.Items
		}
		*items = append(*items, t)
		shown[t.ID] = t
		matched++
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	return tree, matched, total, s.progress(ctx, roadmaps)
}

// progress fills the roadmap nodes' T9.8 fields. It ignores the filter on purpose: the bar shows how
// far the whole roadmap is, so a search does not make it look finished.
func (s *Service) progress(ctx context.Context, roadmaps map[int64]*Node) error {
	if len(roadmaps) == 0 {
		return nil
	}
	rows, err := s.db.Writer().QueryContext(ctx, latestAttempt+`,
		next AS (
		  SELECT roadmap_id, id, title, ROW_NUMBER() OVER (PARTITION BY roadmap_id ORDER BY ord) AS rn
		  FROM topics WHERE state = 'active' AND status <> 'evaluated')
		SELECT t.roadmap_id, COUNT(*), SUM(t.status = 'evaluated'), AVG(l.topic_score), COALESCE(n.id, 0), COALESCE(n.title, '')
		FROM topics t
		LEFT JOIN latest l ON l.topic_id = t.id AND l.rn = 1
		LEFT JOIN next n ON n.roadmap_id = t.roadmap_id AND n.rn = 1
		WHERE t.state = 'active'
		GROUP BY t.roadmap_id`)
	if err != nil {
		return fmt.Errorf("roadmap progress: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rid int64
		var p Node
		if err := rows.Scan(&rid, &p.Total, &p.Done, &p.Avg, &p.NextID, &p.NextTitle); err != nil {
			return err
		}
		if rm := roadmaps[rid]; rm != nil {
			rm.Total, rm.Done, rm.Avg, rm.NextID, rm.NextTitle = p.Total, p.Done, p.Avg, p.NextID, p.NextTitle
		}
	}
	return rows.Err()
}
