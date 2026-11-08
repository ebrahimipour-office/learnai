package roadmap

import (
	"regexp"
	"testing"
)

func keysOf(t *testing.T, src string) (groups, topics []string) {
	t.Helper()
	s, err := Select(Parse([]byte(src)), Rule{})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range s.Groups {
		groups = append(groups, g.StableKey)
	}
	for _, tp := range s.Topics {
		topics = append(topics, tp.StableKey)
	}
	return groups, topics
}

func TestNormalizeFoldsCosmeticDifferences(t *testing.T) {
	for _, set := range [][]string{
		{"کتاب علمی", "كتاب علمي", "کتاب علمى"},
		{"نوع‌ها", "نوع ها", "  نوع   ها  "},
		{"فصل 12", "فصل ۱۲", "فصل ١٢"},
		{"café", "café"}, // composed vs decomposed é
		{"HTTP Basics", "http basics"},
		{"مقدمه", "مقـــدمه"},
	} {
		for _, v := range set[1:] {
			if Normalize(v) != Normalize(set[0]) {
				t.Errorf("Normalize(%q) = %q, want %q", v, Normalize(v), Normalize(set[0]))
			}
		}
	}
}

func TestStableKeyIgnoresCosmeticChanges(t *testing.T) {
	g1, t1 := keysOf(t, "# فاز ۱\n## كتاب علمي\n## نوع‌ها\n# پایان\n")
	g2, t2 := keysOf(t, "# فاز 1\n## کتاب علمی\n## نوع  ها\n# پایان\n")
	if g1[0] != g2[0] || t1[0] != t2[0] || t1[1] != t2[1] {
		t.Fatalf("keys changed: %v %v vs %v %v", g1, t1, g2, t2)
	}
	pat := regexp.MustCompile(`^[0-9a-f]{16}$`)
	for _, k := range append(g1, t1...) {
		if !pat.MatchString(k) {
			t.Errorf("key %q does not match schema pattern", k)
		}
	}
}

func TestStableKeyDiffersByPath(t *testing.T) {
	_, keys := keysOf(t, "# A\n## Intro\n# B\n## Intro\n")
	if keys[0] == keys[1] {
		t.Fatal("same title under different groups got the same key")
	}
}

func TestStableKeyDuplicatesGetSuffix(t *testing.T) {
	// The literal "X#2" must not collide with the suffixed second "X".
	_, keys := keysOf(t, "# A\n## X\n## X\n## X#2\n")
	if keys[0] == keys[1] || keys[1] == keys[2] || keys[0] == keys[2] {
		t.Fatalf("duplicate keys: %v", keys)
	}
	if keys[1] != stableKey("a\nx#2") {
		t.Errorf("second X key = %s, want key of path a\\nx#2", keys[1])
	}
	// Order-stable: the first occurrence keeps the unsuffixed key.
	if keys[0] != stableKey("a\nx") {
		t.Errorf("first X key = %s, want key of path a\\nx", keys[0])
	}
}

// Documented behaviour (R2.3): renaming a heading is a new topic; renaming a group re-keys its topics.
func TestStableKeyRenameMeansNewKey(t *testing.T) {
	_, before := keysOf(t, "# A\n## HTTP\n")
	_, renamed := keysOf(t, "# A\n## HTTP Basics\n")
	_, movedGroup := keysOf(t, "# A2\n## HTTP\n")
	if before[0] == renamed[0] || before[0] == movedGroup[0] {
		t.Fatal("rename did not change the key")
	}
}
