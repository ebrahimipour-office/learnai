package roadmaps_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"learninghub/internal/fsx"
	"learninghub/internal/roadmap"
)

const chapterBook = "# Book\nintro\n## One\ntext one\n## Two\ntext two\n## Three\ntext three\n"

// T7.5 acceptance (§12.6): N chapters ⇒ N topic rows with ranges and N source.md files with matching hashes;
// a failure part-way leaves zero rows and zero files; re-import never deletes or overwrites user text.
func TestConfirmChapterSources(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	s, db, root := newServiceIn(t, base)

	id, _ := s.CreateDraft(ctx, "book.md", []byte(chapterBook))
	rid, n, err := s.Confirm(ctx, id)
	if err != nil || n != 3 {
		t.Fatalf("confirm: %d %v", n, err)
	}
	want := map[string][2]int{"01-one": {3, 4}, "02-two": {5, 6}, "03-three": {7, 8}}
	rows, err := db.Writer().Query(`SELECT t.dir_name, t.source_start_line, t.source_end_line, t.source_sha256, f.rel_path, f.sha256, f.kind
		FROM topics t JOIN files f ON f.id = t.source_file_id`)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for rows.Next() {
		var dir, tsha, rel, fsha, kind string
		var start, end int
		if err := rows.Scan(&dir, &start, &end, &tsha, &rel, &fsha, &kind); err != nil {
			t.Fatal(err)
		}
		seen++
		got, err := root.ReadFile(rel)
		slice := roadmap.Slice([]byte(chapterBook), want[dir][0], want[dir][1])
		if err != nil || [2]int{start, end} != want[dir] || rel != "topics/"+dir+"/source.md" || kind != "source" ||
			string(got) != string(slice) || tsha != fsx.HashBytes(got) || fsha != tsha {
			t.Errorf("%s: lines %d-%d rel %s kind %s content %q (%v)", dir, start, end, rel, kind, got, err)
		}
	}
	rows.Close()
	if seen != 3 || count(t, db, "files") != 3 {
		t.Fatalf("want 3 source files, joined %d, files %d", seen, count(t, db, "files"))
	}

	// Re-import: One unchanged (its user edit stays), Two changed after a user edit, Three changed with source.md gone.
	if err := os.WriteFile(filepath.Join(base, "topics/01-one/source.md"), []byte("my notes one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "topics/02-two/source.md"), []byte("my notes two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(base, "topics/03-three/source.md")); err != nil {
		t.Fatal(err)
	}
	v2 := "# Book\n## One\ntext one\n## Two\ntext two, revised\n## Three\nnew three\n"
	d, _ := reimport(t, s, rid, v2)
	if _, _, err := s.Confirm(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		b, err := root.ReadFile(rel)
		if err != nil {
			return "<" + err.Error() + ">"
		}
		return string(b)
	}
	for rel, want := range map[string]string{
		"topics/01-one/source.md":                     "my notes one",
		"topics/02-two/source.md":                     "## Two\ntext two, revised\n",
		"topics/02-two/source-history/source.0001.md": "my notes two",
		"topics/03-three/source.md":                   "## Three\nnew three\n",
	} {
		if got := read(rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if root.Exists("topics/01-one/source-history") || root.Exists("topics/03-three/source-history") {
		t.Error("unchanged or missing source.md must not get a history copy")
	}
	var start, histRows int
	_ = db.Writer().QueryRow(`SELECT source_start_line FROM topics WHERE dir_name = '01-one'`).Scan(&start)
	_ = db.Writer().QueryRow(`SELECT COUNT(*) FROM files WHERE rel_path LIKE '%source-history%' AND sha256 = ?`,
		fsx.HashBytes([]byte("my notes two"))).Scan(&histRows)
	if start != 2 || histRows != 1 || count(t, db, "files") != 4 {
		t.Errorf("moved range start %d, history rows %d, files %d", start, histRows, count(t, db, "files"))
	}
}

func TestConfirmSourceWriteFailureLeavesNothing(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	s, db, _ := newServiceIn(t, base)
	id, _ := s.CreateDraft(ctx, "book.md", []byte(chapterBook))
	// A plain file named like the third topic's folder fails its source.md after the first two are written.
	if err := os.MkdirAll(filepath.Join(base, "topics"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "topics", "03-three"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Confirm(ctx, id); err == nil {
		t.Fatal("want error")
	}
	for _, table := range append(recordTables, "files") {
		if n := count(t, db, table); n != 0 {
			t.Errorf("%s: want 0 rows after failure, got %d", table, n)
		}
	}
	for _, p := range []string{"topics/01-one/source.md", "topics/02-two/source.md", "roadmaps/book/rev-001.md"} {
		if _, err := os.Stat(filepath.Join(base, p)); !os.IsNotExist(err) {
			t.Errorf("%s left behind after failure: %v", p, err)
		}
	}
}

// CR-06: topic numbers skip the highest NN in the DB and on disk, not COUNT(*)+1.
func TestTopicSeqSkipsGaps(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	s, db, _ := newServiceIn(t, base)
	id, _ := s.CreateDraft(ctx, "book.md", []byte(chapterBook))
	if _, _, err := s.Confirm(ctx, id); err != nil {
		t.Fatal(err)
	}
	// A rebuild-style gap in the DB (07) and a leftover folder on disk (09).
	if _, err := db.Writer().Exec(`UPDATE topics SET dir_name = '07-three' WHERE dir_name = '03-three'`); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "topics", "09-left"), 0o755); err != nil {
		t.Fatal(err)
	}
	id, _ = s.CreateDraft(ctx, "other.md", []byte("# Other\n## B\nb\n"))
	if _, _, err := s.Confirm(ctx, id); err != nil {
		t.Fatal(err)
	}
	var dir string
	_ = db.Writer().QueryRow(`SELECT dir_name FROM topics WHERE title = 'B'`).Scan(&dir)
	if dir != "10-b" {
		t.Fatalf("dir_name = %q, want 10-b", dir)
	}
}
