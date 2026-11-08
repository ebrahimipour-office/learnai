package antigravity

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

var ErrForbiddenArg = errors.New("antigravity: forbidden argument")

// forbidden flags (dashes stripped, since Go flag parsing accepts -x and --x alike):
// permission bypass, edit modes, workspace widening, and flags the adapter owns.
var forbidden = map[string]bool{
	"dangerously-skip-permissions": true,
	"mode":                         true,
	"add-dir":                      true,
	"p":                            true,
	"print":                        true,
	"prompt":                       true,
	"i":                            true,
	"prompt-interactive":           true,
	"c":                            true,
	"continue":                     true,
	"conversation":                 true,
	"output-format":                true,
	"input-format":                 true,
	"json-schema":                  true,
	"print-timeout":                true,
	"model":                        true,
	"effort":                       true,
}

var efforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

const schemaFile = "output.schema.json"

type argSpec struct {
	Prompt    string
	WorkDir   string
	HasSchema bool
	Model     string
	Effort    string
	Timeout   time.Duration
	Sandbox   bool
	Extra     []string
}

// buildArgs returns the agy argv (never run through a shell).
func buildArgs(s argSpec) ([]string, error) {
	if err := checkExtra(s.Extra, forbidden); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return nil, errors.New("antigravity: empty prompt")
	}
	if s.Model == "" || strings.HasPrefix(s.Model, "-") {
		return nil, fmt.Errorf("antigravity: invalid model %q", s.Model)
	}
	if s.Effort != "" && !efforts[s.Effort] {
		return nil, fmt.Errorf("antigravity: invalid effort %q", s.Effort)
	}
	args := []string{"-p", s.Prompt, "--output-format", "stream-json"}
	if s.HasSchema {
		args = append(args, "--json-schema", filepath.Join(s.WorkDir, schemaFile))
	}
	args = append(args, "--model", s.Model)
	if s.Effort != "" {
		args = append(args, "--effort", s.Effort)
	}
	if s.Timeout > 0 {
		args = append(args, "--print-timeout", s.Timeout.String())
	}
	if s.Sandbox {
		args = append(args, "--sandbox")
	}
	return append(args, s.Extra...), nil
}
