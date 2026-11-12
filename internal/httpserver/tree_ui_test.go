package httpserver_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// TestTreeUI is T8.7 (§13.6): the tree preview lists every record indented with a tick and an
// editable title; the topic list nests children under their parent; the panel shows the ancestor
// breadcrumb and the sub-records.
func TestTreeUI(t *testing.T) {
	e := newEnv(t)
	draft := e.upload("go.md", []byte("# Go\n## Basics\nintro\n### Types\n### Loops\n## Tools\n")).Header().Get("Location")
	e.selection(draft, url.Values{"rule": {"tree"}}, true)

	page := e.do(httptest.NewRequest(http.MethodGet, draft, nil)).Body.String()
	for _, want := range []string{`class="chapters tree-rows"`, `class="topic depth-0"`, `class="topic depth-1"`,
		`name="ch_title" value="Types"`, "خط 2 تا 3"} {
		if !strings.Contains(page, want) {
			t.Errorf("tree preview lacks %q", want)
		}
	}
	if strings.Contains(page, "ادغام") {
		t.Error("tree preview offers merge")
	}

	// Rename Types and untick Tools; the rows keep the heading lines.
	keys := regexp.MustCompile(`name="include" value="([^"]+)"`).FindAllStringSubmatch(page, -1)
	if len(keys) != 4 {
		t.Fatalf("rows: %d, want 4", len(keys))
	}
	e.selection(draft, url.Values{"rule": {"tree"}, "shown_rule": {"tree"},
		"ch_line": {"2", "4", "5", "6"}, "ch_title": {"Basics", "Data types", "Loops", "Tools"},
		"include": {keys[0][1], keys[1][1], keys[2][1]}}, true)
	if rec := e.confirm(draft, url.Values{"csrf_token": {e.sm.CSRFToken()}, "confirm": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}

	var basics, types int64
	if err := e.db.Writer().QueryRow(`SELECT id FROM topics WHERE title = 'Basics'`).Scan(&basics); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Writer().QueryRow(`SELECT id FROM topics WHERE title = 'Data types' AND parent_id = ?`, basics).Scan(&types); err != nil {
		t.Fatalf("renamed child: %v", err)
	}

	list := e.do(httptest.NewRequest(http.MethodGet, "/topics", nil)).Body.String()
	nested := regexp.MustCompile(`(?s)<a href="/topics/` + fmt.Sprint(basics) + `"><bdi>Basics</bdi></a>.*?<ul>\s*<li class="topic">\s*<a href="/topics/` + fmt.Sprint(types) + `">`)
	if !nested.MatchString(list) {
		t.Errorf("list does not nest Data types under Basics:\n%s", list)
	}
	if strings.Contains(list, "Tools") {
		t.Error("unticked Tools is listed")
	}

	child := e.do(httptest.NewRequest(http.MethodGet, fmt.Sprintf("/topics/%d", types), nil)).Body.String()
	if !strings.Contains(child, fmt.Sprintf(`› <a href="/topics/%d"><bdi>Basics</bdi></a>`, basics)) {
		t.Error("child panel has no breadcrumb link to its parent")
	}
	parent := e.do(httptest.NewRequest(http.MethodGet, fmt.Sprintf("/topics/%d", basics), nil)).Body.String()
	if !strings.Contains(parent, "زیررکوردها (2)") || !strings.Contains(parent, fmt.Sprintf(`<a href="/topics/%d"><bdi>Data types</bdi></a>`, types)) {
		t.Error("parent panel does not list its sub-records")
	}
}

func TestTopicSearchEmptyAndGroupMatching(t *testing.T) {
	e := newEnv(t)
	draft := e.upload("book.md", []byte("# Go Book\n## Phase 1 - Advanced Concepts\n### Concurrency\n### Channels\n")).Header().Get("Location")
	e.selection(draft, url.Values{"rule": {"deepest"}}, true)
	if rec := e.confirm(draft, url.Values{"csrf_token": {e.sm.CSRFToken()}, "confirm": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}

	// 1. Searching by group name matches its topics
	resGroup := e.do(httptest.NewRequest(http.MethodGet, "/topics?q=Advanced", nil)).Body.String()
	if !strings.Contains(resGroup, "Concurrency") || !strings.Contains(resGroup, "Channels") {
		t.Errorf("expected search by group name 'Advanced' to return topics, got: %s", resGroup)
	}

	// 2. Searching with 0 matches displays distinct empty search message
	resEmpty := e.do(httptest.NewRequest(http.MethodGet, "/topics?q=NonExistentQuery", nil)).Body.String()
	if !strings.Contains(resEmpty, "هیچ موضوعی با این مشخصات یافت نشد.") {
		t.Errorf("expected empty search note, got: %s", resEmpty)
	}
	if !strings.Contains(resEmpty, "<strong>0</strong> موضوع یافت شد") {
		t.Errorf("expected 0 matches count, got: %s", resEmpty)
	}
}
