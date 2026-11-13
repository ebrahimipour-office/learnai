package antigravity

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"learninghub/internal/ai"
)

func decode(t *testing.T, dec decoder, lines ...string) (*result, []string) {
	t.Helper()
	events := make(chan ai.Event, len(lines))
	res, _ := readStream(context.Background(), strings.NewReader(strings.Join(lines, "\n")), events, nil, dec)
	close(events)
	var kinds []string
	for e := range events {
		kinds = append(kinds, e.Kind)
	}
	return res, kinds
}

func TestClaudeDecoder(t *testing.T) {
	// Shapes captured from claude 2.1.319 (trimmed).
	res, kinds := decode(t, &claudeDecoder{},
		`{"type":"system","subtype":"init","session_id":"s1","tools":["Read"]}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"StructuredOutput","input":{"answer":42}}]}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"{\"answer\":42}","structured_output":{"answer":42},"session_id":"s1","usage":{"input_tokens":2,"output_tokens":52,"cache_read_input_tokens":5130,"output_tokens_details":{"thinking_tokens":3}}}`)
	if !slices.Equal(kinds, []string{"started", "step", "heartbeat"}) {
		t.Fatalf("events: %v", kinds)
	}
	if res == nil || res.Status != "SUCCESS" || res.ConversationID != "s1" || res.Usage.TotalTokens != 54 || res.Usage.ThinkingTokens != 3 {
		t.Fatalf("result: %+v", res)
	}
	if s, err := res.structured(); err != nil || string(s) != `{"answer":42}` {
		t.Fatalf("structured: %s %v", s, err)
	}

	res, _ = decode(t, &claudeDecoder{}, `{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}`)
	if res.Status == "SUCCESS" || !errors.Is(classify(DefaultPatterns, res.Error), ai.ErrAuthRequired) {
		t.Fatalf("auth error: %+v", res)
	}
}

func TestCodexDecoder(t *testing.T) {
	res, kinds := decode(t, &codexDecoder{},
		`{"type":"thread.started","thread_id":"th1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"reasoning","text":"thinking"}}`,
		"{\"type\":\"item.completed\",\"item\":{\"id\":\"i1\",\"type\":\"agent_message\",\"text\":\"```json\\n{\\\"a\\\":1}\\n```\"}}",
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":6}}`)
	if !slices.Equal(kinds, []string{"started", "heartbeat", "step", "step", "heartbeat"}) {
		t.Fatalf("events: %v", kinds)
	}
	if res == nil || res.Status != "SUCCESS" || res.ConversationID != "th1" || res.Usage.TotalTokens != 16 {
		t.Fatalf("result: %+v", res)
	}
	if s, err := res.structured(); err != nil || string(s) != `{"a":1}` {
		t.Fatalf("structured: %s %v", s, err)
	}

	res, _ = decode(t, &codexDecoder{}, `{"type":"turn.failed","error":{"message":"You've hit your usage limit."}}`)
	if res.Status == "SUCCESS" || !errors.Is(classify(DefaultPatterns, res.Error), ai.ErrRateLimited) {
		t.Fatalf("failed turn: %+v", res)
	}
	if res, _ := decode(t, &codexDecoder{}, "not json"); res != nil {
		t.Fatalf("no events must give no result: %+v", res)
	}
}

func TestDialectArgs(t *testing.T) {
	spec := argSpec{Prompt: "go", Effort: "max"}
	c, err := buildClaudeArgs(spec, []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(c, " ")
	for _, want := range []string{
		"--tools Read,Glob,Grep",
		"--allowedTools Read(./**),Glob(./**),Grep(./**)",
		"--json-schema {\"type\":\"object\"}",
		"--effort max",
		"--strict-mcp-config",
	} {
		if !strings.Contains(j, want) {
			t.Errorf("claude args miss %q: %q", want, c)
		}
	}
	if slices.Contains(c, "--model") {
		t.Errorf("empty model must use claude's default: %q", c)
	}
	idx := slices.Index(c, "--setting-sources")
	if idx == -1 || idx+1 >= len(c) || c[idx+1] != "" {
		t.Errorf("claude args must pass empty --setting-sources: %q", c)
	}
	if strings.Contains(j, "project") {
		t.Errorf("claude args must not include project setting sources: %q", c)
	}

	x, err := buildCodexArgs(argSpec{Prompt: "-p looks like a flag", Model: "gpt-x", Effort: "max"})
	if err != nil {
		t.Fatal(err)
	}
	if j := strings.Join(x, " "); !strings.Contains(j, "--sandbox read-only") || !strings.Contains(j, "model_reasoning_effort=xhigh") ||
		x[len(x)-2] != "--" || x[len(x)-1] != "-p looks like a flag" {
		t.Errorf("codex args: %q", x)
	}

	for _, bad := range []string{
		"--permission-mode=bypassPermissions",
		"--add-dir",
		"--allow-dangerously-skip-permissions",
		"--setting-sources=project",
		"--allowedTools=Read",
	} {
		if _, err := buildClaudeArgs(argSpec{Prompt: "go", Extra: []string{bad}}, nil); !errors.Is(err, ErrForbiddenArg) {
			t.Errorf("claude accepted %q", bad)
		}
	}
	for _, bad := range []string{"--yolo", "-s", "--sandbox=danger-full-access", "-c", "--dangerously-bypass-approvals-and-sandbox"} {
		if _, err := buildCodexArgs(argSpec{Prompt: "go", Extra: []string{bad}}); !errors.Is(err, ErrForbiddenArg) {
			t.Errorf("codex accepted %q", bad)
		}
	}
}
