package flashcards

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"learninghub/internal/ai"
)

func TestValidate(t *testing.T) {
	cards, err := Validate(json.RawMessage(`{"cards":[{"front":"  WaitGroup?  ","back":" waits "},{"front":" ","back":"x"},{"front":"y","back":""}]}`))
	if err != nil || len(cards) != 1 || cards[0] != (Card{"WaitGroup?", "waits"}) {
		t.Fatalf("got %+v, %v; want one trimmed card, blank sides dropped", cards, err)
	}
	for name, raw := range map[string]string{
		"all blank":  `{"cards":[{"front":" ","back":" "}]}`,
		"no cards":   `{"cards":[]}`,
		"over cap":   `{"cards":[` + strings.Repeat(`{"front":"a","back":"b"},`, 10) + `{"front":"a","back":"b"}]}`,
		"long front": `{"cards":[{"front":"` + strings.Repeat("a", 301) + `","back":"b"}]}`,
		"extra key":  `{"cards":[{"front":"a","back":"b","x":1}]}`,
		"not json":   `nope`,
	} {
		if _, err := Validate(json.RawMessage(raw)); !errors.Is(err, ai.ErrInvalidOutput) {
			t.Errorf("%s: err = %v, want ErrInvalidOutput", name, err)
		}
	}
}
