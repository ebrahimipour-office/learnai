package schema

import (
	"bytes"
	"embed"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed *.schema.json
var SchemasFS embed.FS

var (
	compiler *jsonschema.Compiler
	initOnce sync.Once
	initErr  error

	manifestSchema     *jsonschema.Schema
	rubricSchema       *jsonschema.Schema
	resultSchema       *jsonschema.Schema
	aiLessonSchema     *jsonschema.Schema
	aiExercisesSchema  *jsonschema.Schema
	aiEvaluationSchema *jsonschema.Schema
)

func initSchemas() error {
	compiler = jsonschema.NewCompiler()

	schemaFiles := []struct {
		url      string
		filename string
		target   **jsonschema.Schema
	}{
		{"https://learninghub.local/schemas/manifest-1.json", "manifest.schema.json", &manifestSchema},
		{"https://learninghub.local/schemas/rubric-1.json", "rubric.schema.json", &rubricSchema},
		{"https://learninghub.local/schemas/result-1.json", "result.schema.json", &resultSchema},
		{"https://learninghub.local/schemas/ai-lesson-output-1.json", "ai-lesson-output.schema.json", &aiLessonSchema},
		{"https://learninghub.local/schemas/ai-exercises-output-1.json", "ai-exercises-output.schema.json", &aiExercisesSchema},
		{"https://learninghub.local/schemas/ai-evaluation-output-1.json", "ai-evaluation-output.schema.json", &aiEvaluationSchema},
	}

	// 1. Add all resources to compiler so cross-references resolve
	for _, sf := range schemaFiles {
		data, err := SchemasFS.ReadFile(sf.filename)
		if err != nil {
			return fmt.Errorf("reading embedded schema %s: %w", sf.filename, err)
		}

		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("parsing schema %s JSON: %w", sf.filename, err)
		}

		if err := compiler.AddResource(sf.url, doc); err != nil {
			return fmt.Errorf("adding schema resource %s: %w", sf.url, err)
		}
	}

	// 2. Compile each schema
	for _, sf := range schemaFiles {
		sch, err := compiler.Compile(sf.url)
		if err != nil {
			return fmt.Errorf("compiling schema %s: %w", sf.url, err)
		}
		*sf.target = sch
	}

	return nil
}

func ensureInitialized() error {
	initOnce.Do(func() {
		initErr = initSchemas()
	})
	return initErr
}

// validateData unmarshals JSON and validates it against sch.
func validateData(sch *jsonschema.Schema, data []byte) error {
	if err := ensureInitialized(); err != nil {
		return err
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid json syntax: %w", err)
	}

	if err := sch.Validate(doc); err != nil {
		return fmt.Errorf("schema validation error: %w", err)
	}

	return nil
}

// ValidateManifest validates manifest.json data against its schema.
func ValidateManifest(data []byte) error {
	if err := ensureInitialized(); err != nil {
		return err
	}
	return validateData(manifestSchema, data)
}

// ValidateRubric validates rubric.json data against its schema.
func ValidateRubric(data []byte) error {
	if err := ensureInitialized(); err != nil {
		return err
	}
	return validateData(rubricSchema, data)
}

// ValidateResult validates result.json data against its schema.
func ValidateResult(data []byte) error {
	if err := ensureInitialized(); err != nil {
		return err
	}
	return validateData(resultSchema, data)
}

// ValidateAILessonOutput validates AI lesson output JSON against its schema.
func ValidateAILessonOutput(data []byte) error {
	if err := ensureInitialized(); err != nil {
		return err
	}
	return validateData(aiLessonSchema, data)
}

// ValidateAIExercisesOutput validates AI exercises output JSON against its schema.
func ValidateAIExercisesOutput(data []byte) error {
	if err := ensureInitialized(); err != nil {
		return err
	}
	return validateData(aiExercisesSchema, data)
}

// ValidateAIEvaluationOutput validates AI single-exercise evaluation JSON against its schema.
func ValidateAIEvaluationOutput(data []byte) error {
	if err := ensureInitialized(); err != nil {
		return err
	}
	return validateData(aiEvaluationSchema, data)
}

// GetSchemaBytes returns the raw JSON schema bytes for a given schema filename.
func GetSchemaBytes(filename string) ([]byte, error) {
	return SchemasFS.ReadFile(filename)
}
