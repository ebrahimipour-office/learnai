package roadmaps

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"learninghub/internal/roadmap"
)

// CR-17: 3000 new × 3000 gone must not run 9M Levenshtein pairs.
func TestDiffTopicsLargeIsFast(t *testing.T) {
	kept := make([]roadmap.Topic, 3000)
	old := make([]oldTopic, 3000)
	for i := range kept {
		kept[i] = roadmap.Topic{StableKey: fmt.Sprintf("n%d", i), Title: fmt.Sprintf("new topic number %d about things", i)}
		title := fmt.Sprintf("old topic number %d about stuff", i)
		old[i] = oldTopic{Key: fmt.Sprintf("o%d", i), Title: title, TitleNorm: roadmap.Normalize(title), State: "active"}
	}
	start := time.Now()
	d := diffTopics(kept, old)
	if el := time.Since(start); el > time.Second {
		t.Fatalf("diffTopics took %v, want < 1s", el)
	}
	if !d.GuessSkipped || len(d.New) != 3000 || len(d.Gone) != 3000 {
		t.Fatalf("skipped=%v new=%d gone=%d", d.GuessSkipped, len(d.New), len(d.Gone))
	}
}

// Under the pair cap guesses still work, and very long titles are capped rather than compared in full.
func TestDiffTopicsGuessBounded(t *testing.T) {
	long := strings.Repeat("a", 5000)
	old := []oldTopic{
		{Key: "o1", Title: "Goroutines", TitleNorm: roadmap.Normalize("Goroutines"), State: "active"},
		{Key: "o2", Title: long + "x", TitleNorm: roadmap.Normalize(long + "x"), State: "active"},
	}
	kept := []roadmap.Topic{{StableKey: "n1", Title: "Goroutine"}, {StableKey: "n2", Title: long + "y"}, {StableKey: "n3", Title: "Zzz"}}
	d := diffTopics(kept, old)
	if d.GuessSkipped || d.New[0].Guess != "Goroutines" || d.New[1].Guess != long+"x" || d.New[2].Guess != "" {
		t.Fatalf("guesses = %q, %d chars, %q", d.New[0].Guess, len(d.New[1].Guess), d.New[2].Guess)
	}
}
