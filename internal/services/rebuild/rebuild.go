// Package rebuild recreates the database tables from the files under the base directory (T6.8,
// RK9): roadmaps/<slug>/rev-NNN.md, topics/<dir>/lesson, exercises/v<N>/manifest.json, the rubric,
// the answer files and reports/v<N>/attempt-<K>/result.json. Each revision's topics are re-selected
// with the choice in rev-NNN.choice.json (CR-19; deepest when missing). It only reads files and writes into
// an empty database in one transaction. What exists only in the database is not recoverable and
// gets a default: lesson keywords, suggestions, jobs and the audit log.
package rebuild

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"

	"learninghub/internal/fsx"
	"learninghub/internal/roadmap"
	"learninghub/internal/schema"
	"learninghub/internal/services/evaluation"
	"learninghub/internal/services/exercises"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/services/status"
	"learninghub/internal/store"
)

// ErrNotEmpty means the target database already has records; rebuild never merges.
var ErrNotEmpty = errors.New("rebuild: the database is not empty")

// Report counts what was recorded; Warnings lists what had to be guessed or skipped.
type Report struct {
	Roadmaps, Topics, Lessons, Sets, Attempts, Files int
	Warnings                                         []string
}

const defaultRule = `{"kind":"deepest"}`

var dirNum = regexp.MustCompile(`^(\d+)-(.+)$`)

// candidate is a roadmap topic from a revision file, waiting to be matched to a topics/<dir>.
type candidate struct {
	roadmapID, revID int64
	sel              *roadmap.Selection
	t                roadmap.Topic
	ord              int
	active           bool // in the latest revision
	used             bool
}

type run struct {
	ctx    context.Context
	tx     *sql.Tx
	root   *fsx.Root
	fsys   fs.FS
	rep    *Report
	groups map[string]int64 // roadmapID/stable_key -> groups.id
}

// Run rebuilds db (which must be empty) from the files in root.
func Run(ctx context.Context, db *store.DB, root *fsx.Root) (*Report, error) {
	var n int
	if err := db.Writer().QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM roadmaps) + (SELECT COUNT(*) FROM topics)
		+ (SELECT COUNT(*) FROM files)`).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrNotEmpty
	}
	rep := &Report{}
	err := db.WithTx(ctx, func(tx *sql.Tx) error {
		r := &run{ctx: ctx, tx: tx, root: root, fsys: root.FS(), rep: rep, groups: map[string]int64{}}
		cands, err := r.roadmaps()
		if err != nil {
			return err
		}
		if err := r.topics(cands); err != nil {
			return err
		}
		_, err = status.Recompute(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return rep, nil
}

func (r *run) warn(format string, a ...any) {
	r.rep.Warnings = append(r.rep.Warnings, fmt.Sprintf(format, a...))
}

func base(p string) string    { return path.Base(p) }
func dirBase(p string) string { return path.Base(path.Dir(p)) }

// numbered returns the files matching pattern ordered by the number that format reads from elem(path).
func (r *run) numbered(pattern string, elem func(string) string, format string) ([]string, map[string]int, error) {
	paths, err := fs.Glob(r.fsys, pattern)
	if err != nil {
		return nil, nil, err
	}
	num := map[string]int{}
	var out []string
	for _, p := range paths {
		var n int
		if _, err := fmt.Sscanf(elem(p), format, &n); err != nil || n < 1 {
			continue
		}
		num[p] = n
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return num[out[i]] < num[out[j]] })
	return out, num, nil
}

// roadmaps records every roadmap and revision and returns the topics of all revisions by stable key.
func (r *run) roadmaps() (map[string][]*candidate, error) {
	cands := map[string][]*candidate{}
	slugs, err := fs.Glob(r.fsys, "roadmaps/*")
	if err != nil {
		return nil, err
	}
	for _, dir := range slugs {
		revs, num, err := r.numbered(dir+"/rev-*.md", base, "rev-%d.md")
		if err != nil || len(revs) == 0 {
			continue
		}
		slug := path.Base(dir)
		var rid int64
		if err := r.tx.QueryRowContext(r.ctx, `INSERT INTO roadmaps (slug, title, select_rule) VALUES (?, ?, ?) RETURNING id`,
			slug, slug, defaultRule).Scan(&rid); err != nil {
			return nil, err
		}
		r.rep.Roadmaps++
		for i, rel := range revs {
			b, err := r.root.ReadFile(rel)
			if err != nil {
				return nil, err
			}
			node := roadmap.Parse(b)
			tree, err := json.Marshal(node)
			if err != nil {
				return nil, err
			}
			var revID int64
			if err := r.tx.QueryRowContext(r.ctx, `INSERT INTO roadmap_revisions (roadmap_id, rev_no, rel_path, sha256, parsed_tree)
				VALUES (?, ?, ?, ?, ?) RETURNING id`, rid, num[rel], rel, fsx.HashBytes(b), string(tree)).Scan(&revID); err != nil {
				return nil, err
			}
			choice := r.choice(rel)
			latest := i == len(revs)-1
			if latest {
				rule, _ := json.Marshal(choice.Rule)
				if _, err := r.tx.ExecContext(r.ctx, `UPDATE roadmaps SET select_rule = ? WHERE id = ?`, string(rule), rid); err != nil {
					return nil, err
				}
			}
			sel, err := roadmap.Select(node, choice.Rule)
			if err != nil {
				r.warn("%s: no topics with rule %s: %v", rel, choice.Rule.Kind, err)
				continue
			}
			excluded := map[string]bool{}
			for _, k := range choice.Excluded {
				excluded[k] = true
			}
			ord := 0
			for _, t := range sel.Topics {
				if excluded[t.StableKey] {
					continue
				}
				ord++
				c := &candidate{roadmapID: rid, revID: revID, sel: sel, t: t, ord: ord, active: latest}
				// A later revision replaces the same key's entry from an earlier one of this roadmap.
				list := cands[t.StableKey]
				if k := len(list) - 1; k >= 0 && list[k].roadmapID == rid {
					list[k] = c
				} else {
					list = append(list, c)
				}
				cands[t.StableKey] = list
			}
		}
	}
	return cands, nil
}

// choice reads the Choice confirmed with revision rel; without a readable one (an import older than
// CR-19) it is the default rule with nothing excluded, and a warning.
func (r *run) choice(rel string) roadmaps.Choice {
	c := roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleDeepest}}
	b, err := r.root.ReadFile(roadmaps.ChoiceRel(rel))
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if err != nil {
		r.warn("%s: no readable selection file, the default rule (deepest) is applied: %v", rel, err)
		return roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleDeepest}}
	}
	return c
}

// group records sel.Groups[i] and its parents once per roadmap; -1 is no group.
func (r *run) group(rid int64, sel *roadmap.Selection, i int) (sql.NullInt64, error) {
	if i < 0 {
		return sql.NullInt64{}, nil
	}
	g := sel.Groups[i]
	key := fmt.Sprintf("%d/%s", rid, g.StableKey)
	if id, ok := r.groups[key]; ok {
		return sql.NullInt64{Int64: id, Valid: true}, nil
	}
	parent, err := r.group(rid, sel, g.Parent)
	if err != nil {
		return parent, err
	}
	var id int64
	if err := r.tx.QueryRowContext(r.ctx, `INSERT INTO groups (roadmap_id, parent_id, stable_key, title, level, ord)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING id`, rid, parent, g.StableKey, g.Title, g.Level, float64(i+1)).Scan(&id); err != nil {
		return sql.NullInt64{}, err
	}
	r.groups[key] = id
	return sql.NullInt64{Int64: id, Valid: true}, nil
}

// identity reads the topic's stable key and title from its first readable manifest or result.json.
func (r *run) identity(dir string) (key, title string) {
	for _, pattern := range []string{"topics/" + dir + "/exercises/v*/manifest.json", "topics/" + dir + "/reports/v*/attempt-*/result.json"} {
		paths, _ := fs.Glob(r.fsys, pattern)
		for _, p := range paths {
			var v struct{ Topic schema.ManifestTopic }
			if b, err := r.root.ReadFile(p); err == nil && json.Unmarshal(b, &v) == nil && v.Topic.StableKey != "" {
				return v.Topic.StableKey, v.Topic.Title
			}
		}
	}
	return "", ""
}

// topics matches each topics/<dir> to a roadmap topic (by stable key, else by the slug in the
// folder name), records it with everything inside, then records the unmatched roadmap topics.
func (r *run) topics(cands map[string][]*candidate) error {
	entries, err := fs.ReadDir(r.fsys, "topics")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	slugOf := func(c *candidate) string { s, _ := roadmaps.TopicDir(1, c.t.Title); return s }
	maxSeq := 0
	var fallbackRoadmap int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := e.Name()
		m := dirNum.FindStringSubmatch(dir)
		if m == nil {
			r.warn("topics/%s: not a topic folder (NN-slug), skipped", dir)
			continue
		}
		seq, _ := strconv.Atoi(m[1])
		maxSeq = max(maxSeq, seq)
		key, title := r.identity(dir)

		var match *candidate
		pick := func(list []*candidate, need func(*candidate) bool) {
			for _, c := range list {
				if !c.used && need(c) && (match == nil || (slugOf(c) == m[2] && slugOf(match) != m[2])) {
					match = c
				}
			}
		}
		if key != "" {
			pick(cands[key], func(*candidate) bool { return true })
		} else {
			for _, list := range cands {
				pick(list, func(c *candidate) bool { return slugOf(c) == m[2] })
			}
		}

		var topicID int64
		if match != nil {
			match.used = true
			if topicID, err = r.insertTopic(match, m[2], dir); err != nil {
				return err
			}
		} else {
			if fallbackRoadmap == 0 {
				if fallbackRoadmap, err = r.recoveredRoadmap(); err != nil {
					return err
				}
			}
			if title == "" {
				title = m[2]
			}
			if key == "" {
				key = fsx.HashString("rebuild:" + dir)[:16]
			}
			r.warn("topics/%s: not in any roadmap revision, recorded as a suggested topic", dir)
			if err := r.tx.QueryRowContext(r.ctx, `INSERT INTO topics (roadmap_id, stable_key, title, title_norm, ord, slug, dir_name, origin)
				VALUES (?, ?, ?, ?, ?, ?, ?, 'suggested') RETURNING id`, fallbackRoadmap, key, title, roadmap.Normalize(title),
				float64(1000+seq), m[2], dir).Scan(&topicID); err != nil {
				return fmt.Errorf("topics/%s: %w", dir, err)
			}
			r.rep.Topics++
		}
		if err := r.contents(topicID, dir, key); err != nil {
			return err
		}
	}

	// Roadmap topics without a folder yet (no lesson generated); numbered after every existing folder.
	var rest []*candidate
	for _, list := range cands {
		for _, c := range list {
			if !c.used && c.active {
				rest = append(rest, c)
			}
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].roadmapID != rest[j].roadmapID {
			return rest[i].roadmapID < rest[j].roadmapID
		}
		return rest[i].ord < rest[j].ord
	})
	for _, c := range rest {
		maxSeq++
		slug, dir := roadmaps.TopicDir(maxSeq, c.t.Title)
		if _, err := r.insertTopic(c, slug, dir); err != nil {
			return err
		}
	}
	return nil
}

// recoveredRoadmap is the first roadmap, or a new empty one when no revision file exists.
func (r *run) recoveredRoadmap() (int64, error) {
	var id int64
	err := r.tx.QueryRowContext(r.ctx, `SELECT id FROM roadmaps ORDER BY id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		r.warn("no roadmap revision files: topics recorded under a new roadmap 'recovered'")
		err = r.tx.QueryRowContext(r.ctx, `INSERT INTO roadmaps (slug, title, select_rule) VALUES ('recovered', 'Recovered', ?) RETURNING id`,
			defaultRule).Scan(&id)
		r.rep.Roadmaps++
	}
	return id, err
}

func (r *run) insertTopic(c *candidate, slug, dir string) (int64, error) {
	group, err := r.group(c.roadmapID, c.sel, c.t.Group)
	if err != nil {
		return 0, err
	}
	state := "active"
	if !c.active {
		state = "archived"
	}
	outline := sql.NullString{String: c.t.Outline, Valid: c.t.Outline != ""}
	start := 0 // no source range (items rule) is NULL
	if c.t.EndLine > 0 {
		start = c.t.Line
	}
	var id int64
	if err := r.tx.QueryRowContext(r.ctx, `INSERT INTO topics (roadmap_id, group_id, stable_key, title, title_norm, outline, ord, slug,
		dir_name, state, source_rev_id, source_start_line, source_end_line) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, 0), NULLIF(?, 0))
		RETURNING id`, c.roadmapID, group, c.t.StableKey, c.t.Title, roadmap.Normalize(c.t.Title), outline, float64(c.ord), slug, dir,
		state, c.revID, start, c.t.EndLine).Scan(&id); err != nil {
		return 0, fmt.Errorf("topic %q (%s): %w", c.t.Title, dir, err)
	}
	r.rep.Topics++
	return id, nil
}

// file records rel; a file that is gone is recorded as missing so its owner row can still exist.
func (r *run) file(topicID int64, kind, ownerType, rel string, immutable bool) (int64, string, error) {
	sha, size, err := r.root.HashFile(rel)
	presence := "present"
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return 0, "", fmt.Errorf("%s: %w", rel, err)
		}
		r.warn("%s: missing, recorded as missing", rel)
		presence, sha, size = "missing", "", 0
	}
	var id int64
	err = r.tx.QueryRowContext(r.ctx, `INSERT INTO files (topic_id, kind, owner_type, rel_path, sha256, size_bytes, immutable,
		observed_sha256, presence) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?) RETURNING id`,
		topicID, kind, ownerType, rel, sha, size, immutable, sha, presence).Scan(&id)
	if err != nil {
		return 0, "", fmt.Errorf("%s: %w", rel, err)
	}
	r.rep.Files++
	return id, sha, nil
}

func (r *run) own(fileID, ownerID int64) error {
	_, err := r.tx.ExecContext(r.ctx, `UPDATE files SET owner_id = ? WHERE id = ?`, ownerID, fileID)
	return err
}

// contents records the chapter text, lesson versions, exercise sets and attempts found in topics/<dir>.
func (r *run) contents(topicID int64, dir, key string) error {
	if err := r.sources(topicID, dir); err != nil {
		return err
	}
	if err := r.lessons(topicID, dir); err != nil {
		return err
	}
	manifests, num, err := r.numbered("topics/"+dir+"/exercises/v*/manifest.json", dirBase, "v%d")
	if err != nil {
		return err
	}
	for i, p := range manifests {
		if err := r.set(topicID, dir, key, num[p], p, i == len(manifests)-1); err != nil {
			return err
		}
	}
	return nil
}

// sources records source-history/source.NNNN.md and source.md (§12.6). source_sha256 is the file's
// own hash: the slice last written is unknown, and an equal hash keeps the next re-import from touching it.
func (r *run) sources(topicID int64, dir string) error {
	hist, _, err := r.numbered("topics/"+dir+"/source-history/source.*.md", base, "source.%d.md")
	if err != nil {
		return err
	}
	for _, rel := range hist {
		fid, _, err := r.file(topicID, "source", "topic", rel, false)
		if err != nil {
			return err
		}
		if err := r.own(fid, topicID); err != nil {
			return err
		}
	}
	rel := "topics/" + dir + "/source.md"
	if !r.root.Exists(rel) {
		return nil
	}
	fid, sha, err := r.file(topicID, "source", "topic", rel, false)
	if err != nil {
		return err
	}
	if err := r.own(fid, topicID); err != nil {
		return err
	}
	_, err = r.tx.ExecContext(r.ctx, `UPDATE topics SET source_file_id = ?, source_sha256 = ? WHERE id = ?`, fid, sha, topicID)
	return err
}

func (r *run) lessons(topicID int64, dir string) error {
	hist, num, err := r.numbered("topics/"+dir+"/lesson/history/lesson.*.md", base, "lesson.%d.md")
	if err != nil {
		return err
	}
	add := func(rel string, version int, current bool) error {
		fid, _, err := r.file(topicID, "lesson", "lesson_version", rel, false)
		if err != nil {
			return err
		}
		var id int64
		if err := r.tx.QueryRowContext(r.ctx, `INSERT INTO lesson_versions (topic_id, version, file_id, keywords, is_current)
			VALUES (?, ?, ?, '[]', ?) RETURNING id`, topicID, version, fid, current).Scan(&id); err != nil {
			return err
		}
		r.rep.Lessons++
		return r.own(fid, id)
	}
	last := 0
	for _, rel := range hist {
		if err := add(rel, num[rel], false); err != nil {
			return err
		}
		last = num[rel]
	}
	if rel := "topics/" + dir + "/lesson/lesson.md"; r.root.Exists(rel) {
		return add(rel, last+1, true)
	}
	return nil
}

// set records exercises/v<v> with its exercises and attempts. A manifest that is invalid or
// belongs to another topic is skipped with a warning (its attempts cannot be tied to exercises).
func (r *run) set(topicID int64, dir, key string, v int, manifestRel string, current bool) error {
	b, err := r.root.ReadFile(manifestRel)
	if err != nil {
		return err
	}
	var m schema.Manifest
	if err := schema.ValidateManifest(b); err != nil {
		r.warn("%s: invalid manifest, set skipped: %v", manifestRel, err)
		return nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if m.Topic.StableKey != key || m.Set.Version != v {
		r.warn("%s: belongs to topic %s set v%d, skipped", manifestRel, m.Topic.StableKey, m.Set.Version)
		return nil
	}
	setDir := exercises.SetRel(dir, v)
	ids := make([]int64, 3)
	for i, f := range []struct{ kind, rel string }{
		{"exercises_md", path.Join(setDir, "exercises.md")}, {"manifest", manifestRel}, {"rubric", exercises.RubricRel(dir, v)},
	} {
		if ids[i], _, err = r.file(topicID, f.kind, "exercise_set", f.rel, false); err != nil {
			return err
		}
	}
	g := m.Set.Generator
	var jobID sql.NullInt64 // jobs are not rebuilt
	var setID int64
	if err := r.tx.QueryRowContext(r.ctx, `INSERT INTO exercise_sets (topic_id, version, exercises_file, manifest_file, rubric_file,
		rubric_version, is_current, job_id, skill, skill_version, model, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		topicID, v, ids[0], ids[1], ids[2], m.Set.RubricVersion, current, jobID, g.Skill, g.SkillVersion, g.Model, m.Set.CreatedAt).Scan(&setID); err != nil {
		return err
	}
	r.rep.Sets++
	for _, id := range ids {
		if err := r.own(id, setID); err != nil {
			return err
		}
	}
	exIDs := map[string]int64{}
	for _, ex := range m.Exercises {
		rel := path.Join(exercises.AnswersRel(dir, v), ex.Answer.FileName)
		fid, _, err := r.file(topicID, "answer", "exercise", rel, true)
		if err != nil {
			return err
		}
		// The template's starter code exists only in the database: this is exact for exercises
		// without one; otherwise a still-untouched answer will show as answered.
		tpl := string(fsx.GenerateAnswerTemplate(key, v, ex.ID, ex.Title, ex.Answer.Ext, ""))
		deps, _ := json.Marshal(ex.ExternalKnowledge)
		if ex.ExternalKnowledge == nil {
			deps = []byte("[]")
		}
		var id int64
		if err := r.tx.QueryRowContext(r.ctx, `INSERT INTO exercises (set_id, ex_id, ord, title, type, difficulty, weight, answer_file_id,
			template_sha256, template_norm_sha256, external_deps) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?) RETURNING id`,
			setID, ex.ID, ex.Order, ex.Title, ex.Type, ex.Difficulty, ex.Weight, fid, fsx.HashString(tpl), string(deps)).Scan(&id); err != nil {
			return err
		}
		exIDs[ex.ID] = id
		if err := r.own(fid, id); err != nil {
			return err
		}
	}
	return r.attempts(topicID, dir, key, v, setID, exIDs)
}

func (r *run) attempts(topicID int64, dir, key string, v int, setID int64, exIDs map[string]int64) error {
	results, num, err := r.numbered(path.Join("topics", dir, "reports", fmt.Sprintf("v%d", v), "attempt-*", "result.json"), dirBase, "attempt-%d")
	if err != nil {
		return err
	}
	byNo := map[int]int64{}
	for _, p := range results {
		b, err := r.root.ReadFile(p)
		if err != nil {
			return err
		}
		var res schema.Result
		if err := schema.ValidateResult(b); err != nil {
			r.warn("%s: invalid result.json, attempt skipped: %v", p, err)
			continue
		}
		if err := json.Unmarshal(b, &res); err != nil {
			return err
		}
		k := num[p]
		if res.Topic.StableKey != key || res.SetVersion != v || res.AttemptNo != k {
			r.warn("%s: belongs to topic %s v%d attempt-%d, skipped", p, res.Topic.StableKey, res.SetVersion, res.AttemptNo)
			continue
		}
		adir := evaluation.AttemptRel(dir, v, k)
		summaryID, _, err := r.file(topicID, "summary", "attempt", path.Join(adir, "summary.md"), true)
		if err != nil {
			return err
		}
		resultID, _, err := r.file(topicID, "result", "attempt", p, true)
		if err != nil {
			return err
		}
		a, ev := res.Aggregate, res.Evaluator
		var attemptID int64
		if err := r.tx.QueryRowContext(r.ctx, `INSERT INTO attempts (set_id, attempt_no, mode, rubric_version, topic_score, answered_count,
			total_count, is_partial, result_file, summary_file, skill, skill_version, model, started_at, finished_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, setID, k, res.Mode, res.RubricVersion, a.WeightedScore,
			a.AnsweredCount, a.TotalCount, a.Partial, resultID, summaryID, ev.Skill, ev.SkillVersion, ev.Model,
			res.StartedAt, res.FinishedAt).Scan(&attemptID); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		r.rep.Attempts++
		byNo[k] = attemptID
		for _, id := range []int64{summaryID, resultID} {
			if err := r.own(id, attemptID); err != nil {
				return err
			}
		}
		for _, it := range res.Items {
			exID, ok := exIDs[it.ExerciseID]
			if !ok {
				return fmt.Errorf("%s: exercise %s is not in manifest v%d", p, it.ExerciseID, v)
			}
			var reportID, from sql.NullInt64
			if it.ReportFile != nil && *it.ReportFile != "" {
				id, _, err := r.file(topicID, "report", "attempt_item", path.Join(adir, *it.ReportFile), true)
				if err != nil {
					return err
				}
				reportID = sql.NullInt64{Int64: id, Valid: true}
			}
			if it.CarriedFromAttempt != nil {
				id, ok := byNo[*it.CarriedFromAttempt]
				from = sql.NullInt64{Int64: id, Valid: ok}
			}
			et, _ := json.Marshal(it.ErrorTypes)
			if it.ErrorTypes == nil {
				et = []byte("[]")
			}
			var itemID int64
			if err := r.tx.QueryRowContext(r.ctx, `INSERT INTO attempt_items (attempt_id, exercise_id, status, answer_sha256, score,
				carried_from_attempt, report_file, error_types) VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
				attemptID, exID, it.Status, it.AnswerSHA256, it.Score, from, reportID, string(et)).Scan(&itemID); err != nil {
				return fmt.Errorf("%s %s: %w", p, it.ExerciseID, err)
			}
			if reportID.Valid {
				if err := r.own(reportID.Int64, itemID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
