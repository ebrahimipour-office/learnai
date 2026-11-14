package schema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"learninghub/internal/schema"
)

// node walks a schema by a dotted path; "[]" steps into array items.
func node(t *testing.T, file, path string) map[string]any {
	t.Helper()
	b, err := schema.GetSchemaBytes(file)
	if err != nil {
		t.Fatal(err)
	}
	var n map[string]any
	if err := json.Unmarshal(b, &n); err != nil {
		t.Fatal(err)
	}
	for _, k := range strings.Split(path, ".") {
		var next any
		switch {
		case k == "[]":
			next = n["items"]
		case strings.HasPrefix(k, "$"):
			next = n["$defs"].(map[string]any)[k[1:]]
		default:
			next = n["properties"].(map[string]any)[k]
		}
		m, ok := next.(map[string]any)
		if !ok {
			t.Fatalf("%s: no node %q in %s", file, k, path)
		}
		n = m
	}
	return n
}

// CR-30: every limit of manifest/rubric on a field Build copies straight from the AI
// output must be in ai-exercises-output, so the AI validator rejects it first.
func TestAIExercisesLimitsMatchDownstream(t *testing.T) {
	const ai, ex = "ai-exercises-output.schema.json", "exercises.[]"
	pairs := []struct{ aiPath, file, path string }{
		{ex + ".title", "manifest.schema.json", "$exercise.title"},
		{ex + ".answer.language", "manifest.schema.json", "$exercise.answer.language"},
		{ex + ".external_knowledge", "manifest.schema.json", "$exercise.external_knowledge"},
		{ex + ".external_knowledge.[].topic", "manifest.schema.json", "$exercise.external_knowledge.[].topic"},
		{ex + ".external_knowledge.[].note", "manifest.schema.json", "$exercise.external_knowledge.[].note"},
		{"exercises", "manifest.schema.json", "exercises"},
		{ex + ".rubric.reference_answer.language", "rubric.schema.json", "exercises.[].reference_answer.language"},
		{ex + ".rubric.reference_answer.content", "rubric.schema.json", "exercises.[].reference_answer.content"},
		{ex + ".rubric.criteria", "rubric.schema.json", "exercises.[].criteria"},
		{ex + ".rubric.criteria.[].description", "rubric.schema.json", "exercises.[].criteria.[].description"},
		{ex + ".rubric.criteria.[].max_points", "rubric.schema.json", "exercises.[].criteria.[].max_points"},
		{ex + ".rubric.common_errors", "rubric.schema.json", "exercises.[].common_errors"},
		{ex + ".rubric.common_errors.[].description", "rubric.schema.json", "exercises.[].common_errors.[].description"},
		{ex + ".rubric.out_of_scope_knowledge", "rubric.schema.json", "exercises.[].out_of_scope_knowledge"},
		{ex + ".rubric.out_of_scope_knowledge.[].topic", "rubric.schema.json", "exercises.[].out_of_scope_knowledge.[].topic"},
		{ex + ".rubric.grading_notes", "rubric.schema.json", "exercises.[].grading_notes"},
	}
	for _, p := range pairs {
		a, d := node(t, ai, p.aiPath), node(t, p.file, p.path)
		for _, k := range []string{"maxLength", "minLength", "maxItems", "minItems", "minimum", "maximum"} {
			if dv, ok := d[k]; ok && a[k] != dv {
				t.Errorf("%s %s = %v, %s %s = %v", ai, p.aiPath+"."+k, a[k], p.file, p.path, dv)
			}
		}
	}
}
