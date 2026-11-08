package domain

import "errors"

// Common domain errors
var (
	ErrNotFound          = errors.New("not found")
	ErrAlreadyExists     = errors.New("already exists")
	ErrScopeBusy         = errors.New("another active job is already running on this scope")
	ErrImmutableFile     = errors.New("file is immutable and cannot be overwritten")
	ErrNothingToEvaluate = errors.New("nothing to evaluate: no new or modified answers found")
	ErrInvalidInput      = errors.New("invalid input")
	ErrInvalidState      = errors.New("invalid entity state transition")
)

// TopicStatus represents the learning progression status of a topic (R2.3).
type TopicStatus string

const (
	StatusCreated        TopicStatus = "created"
	StatusContentReady   TopicStatus = "content_ready"
	StatusExercisesReady TopicStatus = "exercises_ready"
	StatusAnswered       TopicStatus = "answered"
	StatusEvaluated      TopicStatus = "evaluated"
)

// TopicState represents active vs archived state (R2.5).
type TopicState string

const (
	StateActive   TopicState = "active"
	StateArchived TopicState = "archived"
)

// TopicOrigin tracks whether a topic came from the primary roadmap or an AI suggestion.
type TopicOrigin string

const (
	OriginRoadmap   TopicOrigin = "roadmap"
	OriginSuggested TopicOrigin = "suggested"
)

// FileKind distinguishes file types registered in the files table (R5.3).
type FileKind string

const (
	FileKindLesson      FileKind = "lesson"
	FileKindExercisesMD FileKind = "exercises_md"
	FileKindManifest    FileKind = "manifest"
	FileKindRubric      FileKind = "rubric"
	FileKindAnswer      FileKind = "answer"
	FileKindReport      FileKind = "report"
	FileKindSummary     FileKind = "summary"
	FileKindResult      FileKind = "result"
)

// FilePresence tracks whether the file was found on disk during the last sync.
type FilePresence string

const (
	FilePresencePresent FilePresence = "present"
	FilePresenceMissing FilePresence = "missing"
)

// ExerciseType categorizes exercise pedagogy (R3.2).
type ExerciseType string

const (
	ExerciseTypeCoding         ExerciseType = "coding"
	ExerciseTypeConceptual     ExerciseType = "conceptual"
	ExerciseTypeCodeAnalysis   ExerciseType = "code_analysis"
	ExerciseTypeProblemSolving ExerciseType = "problem_solving"
)

// Difficulty specifies exercise difficulty tier.
type Difficulty string

const (
	DifficultyEasy   Difficulty = "easy"
	DifficultyMedium Difficulty = "medium"
	DifficultyHard   Difficulty = "hard"
)

// JobKind identifies the background job operation (R4).
type JobKind string

const (
	JobKindLesson         JobKind = "lesson"
	JobKindExercises      JobKind = "exercises"
	JobKindEvaluate       JobKind = "evaluate"
	JobKindRoadmapAnalyze JobKind = "roadmap_analyze"
	JobKindChapterDetect  JobKind = "chapter_detect"
)

// JobStatus tracks the lifecycle state of a background job (R4).
type JobStatus string

const (
	JobStatusQueued    JobStatus = "queued"
	JobStatusRunning   JobStatus = "running"
	JobStatusSucceeded JobStatus = "succeeded"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCanceled  JobStatus = "canceled"
)

// AttemptMode distinguishes incremental vs full evaluation.
type AttemptMode string

const (
	AttemptModeFull        AttemptMode = "full"
	AttemptModeIncremental AttemptMode = "incremental"
)

// SuggestionKind defines AI recommendation scope (R8.3).
type SuggestionKind string

const (
	SuggestionKindPrerequisite  SuggestionKind = "prerequisite"
	SuggestionKindSupplementary SuggestionKind = "supplementary"
)

// SuggestionStatus tracks decision state on an AI suggestion.
type SuggestionStatus string

const (
	SuggestionStatusPending  SuggestionStatus = "pending"
	SuggestionStatusAccepted SuggestionStatus = "accepted"
	SuggestionStatusRejected SuggestionStatus = "rejected"
)
