package schema

// Manifest represents the parsed manifest.json structure.
type Manifest struct {
	SchemaVersion int                `json:"schema_version"`
	Topic         ManifestTopic      `json:"topic"`
	Set           ManifestSet        `json:"set"`
	Exercises     []ManifestExercise `json:"exercises"`
}

type ManifestTopic struct {
	StableKey string `json:"stable_key"`
	Title     string `json:"title"`
}

type ManifestSet struct {
	Version       int               `json:"version"`
	RubricVersion int               `json:"rubric_version"`
	CreatedAt     string            `json:"created_at"`
	Generator     ManifestGenerator `json:"generator"`
}

type ManifestGenerator struct {
	Adapter      string `json:"adapter"`
	Model        string `json:"model,omitempty"`
	Skill        string `json:"skill"`
	SkillVersion string `json:"skill_version"`
	JobID        int64  `json:"job_id,omitempty"`
}

type ManifestExercise struct {
	ID                string              `json:"id"`
	Order             int                 `json:"order"`
	Title             string              `json:"title"`
	Type              string              `json:"type"`
	Difficulty        string              `json:"difficulty"`
	Weight            float64             `json:"weight"`
	EstimatedMinutes  int                 `json:"estimated_minutes,omitempty"`
	Answer            ManifestAnswer      `json:"answer"`
	ExternalKnowledge []ExternalKnowledge `json:"external_knowledge,omitempty"`
}

type ManifestAnswer struct {
	Slug     string `json:"slug"`
	Ext      string `json:"ext"`
	Language string `json:"language,omitempty"`
	FileName string `json:"file_name"`
}

type ExternalKnowledge struct {
	Topic string `json:"topic"`
	Note  string `json:"note"`
}

// Rubric represents rubric-vN.json structure.
type Rubric struct {
	SchemaVersion  int               `json:"schema_version"`
	RubricVersion  int               `json:"rubric_version"`
	SetVersion     int               `json:"set_version"`
	TopicStableKey string            `json:"topic_stable_key"`
	Generator      ManifestGenerator `json:"generator,omitempty"`
	Exercises      []RubricExercise  `json:"exercises"`
}

type RubricExercise struct {
	ID                  string                `json:"id"`
	ReferenceAnswer     ReferenceAnswer       `json:"reference_answer"`
	Criteria            []Criterion           `json:"criteria"`
	CommonErrors        []CommonError         `json:"common_errors,omitempty"`
	OutOfScopeKnowledge []OutOfScopeKnowledge `json:"out_of_scope_knowledge,omitempty"`
	GradingNotes        string                `json:"grading_notes,omitempty"`
}

type ReferenceAnswer struct {
	Format   string `json:"format"`
	Language string `json:"language,omitempty"`
	Content  string `json:"content"`
}

type Criterion struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	MaxPoints   int    `json:"max_points"`
	Category    string `json:"category"`
}

type CommonError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type OutOfScopeKnowledge struct {
	Topic    string `json:"topic"`
	Handling string `json:"handling"`
}

// Result represents result.json structure.
type Result struct {
	SchemaVersion int               `json:"schema_version"`
	Topic         ManifestTopic     `json:"topic"`
	SetVersion    int               `json:"set_version"`
	AttemptNo     int               `json:"attempt_no"`
	Mode          string            `json:"mode"`
	RubricVersion int               `json:"rubric_version"`
	StartedAt     string            `json:"started_at"`
	FinishedAt    string            `json:"finished_at"`
	JobID         int64             `json:"job_id,omitempty"`
	Evaluator     ManifestGenerator `json:"evaluator"`
	Items         []ResultItem      `json:"items"`
	Aggregate     ResultAggregate   `json:"aggregate"`
}

type ResultItem struct {
	ExerciseID         string          `json:"exercise_id"`
	Weight             float64         `json:"weight"`
	Status             string          `json:"status"`
	Score              int             `json:"score"`
	AnswerSHA256       *string         `json:"answer_sha256,omitempty"`
	ReportFile         *string         `json:"report_file,omitempty"`
	CarriedFromAttempt *int            `json:"carried_from_attempt,omitempty"`
	CriteriaScores     []CriteriaScore `json:"criteria_scores,omitempty"`
	ErrorTypes         []string        `json:"error_types,omitempty"`
}

type CriteriaScore struct {
	CriterionID string `json:"criterion_id"`
	Awarded     int    `json:"awarded"`
	Max         int    `json:"max"`
}

type ResultAggregate struct {
	WeightedScore float64 `json:"weighted_score"`
	AnsweredCount int     `json:"answered_count"`
	TotalCount    int     `json:"total_count"`
	Partial       bool    `json:"partial"`
	Formula       string  `json:"formula"`
	ComputedBy    string  `json:"computed_by"`
}
