package qa

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"learninghub/internal/ai"
)

func TestValidate(t *testing.T) {
	if got, err := Validate(json.RawMessage(`{"answer_markdown":"  **yes**\n"}`)); err != nil || got != "**yes**" {
		t.Fatalf("got %q, %v; want trimmed answer", got, err)
	}
	for name, raw := range map[string]string{
		"blank":     `{"answer_markdown":"   "}`,
		"empty":     `{"answer_markdown":""}`,
		"too long":  `{"answer_markdown":"` + strings.Repeat("a", 20001) + `"}`,
		"missing":   `{}`,
		"extra key": `{"answer_markdown":"a","x":1}`,
	} {
		if _, err := Validate(json.RawMessage(raw)); !errors.Is(err, ai.ErrInvalidOutput) {
			t.Errorf("%s: err = %v, want ErrInvalidOutput", name, err)
		}
	}
}
