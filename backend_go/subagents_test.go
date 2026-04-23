package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildSpawnSubagentToolDefinition(t *testing.T) {
	definition := buildSpawnSubagentToolDefinition()

	if definition.Name != "spawn_subagent" {
		t.Fatalf("name = %q, want spawn_subagent", definition.Name)
	}
	if !strings.Contains(definition.Description, "ephemeral subagent") {
		t.Fatalf("description missing ephemeral guidance: %q", definition.Description)
	}
	if !strings.Contains(definition.Description, "full Towel toolset") {
		t.Fatalf("description missing toolset guidance: %q", definition.Description)
	}
	properties, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", definition.Parameters["properties"])
	}
	taskSchema, ok := properties["task"].(map[string]any)
	if !ok {
		t.Fatalf("task schema type = %T, want map[string]any", properties["task"])
	}
	contextSchema, ok := properties["context"].(map[string]any)
	if !ok {
		t.Fatalf("context schema type = %T, want map[string]any", properties["context"])
	}
	if got := taskSchema["type"]; got != "string" {
		t.Fatalf("task type = %v, want string", got)
	}
	if got := contextSchema["type"]; got != "string" {
		t.Fatalf("context type = %v, want string", got)
	}
	required, ok := definition.Parameters["required"].([]string)
	if !ok {
		t.Fatalf("required type = %T, want []string", definition.Parameters["required"])
	}
	if len(required) != 1 || required[0] != "task" {
		t.Fatalf("required = %v, want [task]", required)
	}
}

func TestBuildSubagentSystemPromptIncludesRuntimeContract(t *testing.T) {
	prompt := buildSubagentSystemPrompt()

	if !strings.Contains(prompt, "You are an ephemeral subagent spawned by another Towel agent.") {
		t.Fatalf("prompt missing subagent identity: %q", prompt)
	}
	if !strings.Contains(prompt, "\"status\": \"completed\" | \"needs_input\"") {
		t.Fatalf("prompt missing response schema: %q", prompt)
	}
	if !strings.Contains(prompt, "single JSON object and nothing else") {
		t.Fatalf("prompt missing JSON-only requirement: %q", prompt)
	}
}

func TestAllToolDefinitionsIncludeSpawnSubagent(t *testing.T) {
	app := newTestApp(t)
	definitions := app.allToolDefinitions()

	for _, definition := range definitions {
		if definition.Name == "spawn_subagent" {
			return
		}
	}
	t.Fatal("spawn_subagent tool definition not found")
}

func TestExecuteSpawnSubagentToolReturnsStructuredPayload(t *testing.T) {
	app := newTestApp(t)

	var gotPrompt string
	var gotHistory []ConversationMessage
	var gotDepth int
	result, err := app.executeSpawnSubagentToolWithRunner(
		context.Background(),
		AgentDefinition{AgentID: "openai:gpt-5.4", Model: "gpt-5.4"},
		"test-key",
		0,
		map[string]any{
			"task":    "Inspect unread recruiter emails and summarize active threads.",
			"context": "Focus on the last 14 days and keep the result concise.",
		},
		func(ctx context.Context, systemPrompt string, history []ConversationMessage, subagentDepth int) (string, []string, error) {
			gotPrompt = systemPrompt
			gotHistory = history
			gotDepth = subagentDepth
			return `{"status":"needs_input","summary":"Need a narrower target set.","answer":"I can continue once the target senders or labels are specified.","follow_up_question":"Which recruiter domains or labels should I focus on?"}`, []string{"Executed tool query_db with args {\"sql\":\"SELECT 1\"}."}, nil
		},
	)
	if err != nil {
		t.Fatalf("execute spawn_subagent: %v", err)
	}

	if gotDepth != 1 {
		t.Fatalf("subagent depth = %d, want 1", gotDepth)
	}
	if len(gotHistory) != 1 {
		t.Fatalf("history len = %d, want 1", len(gotHistory))
	}
	if gotHistory[0].Role != "user" {
		t.Fatalf("history role = %q, want user", gotHistory[0].Role)
	}
	if !strings.Contains(gotHistory[0].Content, "Inspect unread recruiter emails") {
		t.Fatalf("history content missing task: %q", gotHistory[0].Content)
	}
	if !strings.Contains(gotHistory[0].Content, "last 14 days") {
		t.Fatalf("history content missing context: %q", gotHistory[0].Content)
	}
	if !strings.Contains(gotPrompt, "single JSON object and nothing else") {
		t.Fatalf("system prompt missing JSON-only guidance: %q", gotPrompt)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got := payload["tool"]; got != "spawn_subagent" {
		t.Fatalf("tool = %v, want spawn_subagent", got)
	}
	if got := payload["status"]; got != "needs_input" {
		t.Fatalf("status = %v, want needs_input", got)
	}
	if got := payload["follow_up_question"]; got != "Which recruiter domains or labels should I focus on?" {
		t.Fatalf("follow_up_question = %v", got)
	}
	if got := payload["ephemeral"]; got != true {
		t.Fatalf("ephemeral = %v, want true", got)
	}
	if got := payload["interaction_model"]; got != "ephemeral_single_turn" {
		t.Fatalf("interaction_model = %v, want ephemeral_single_turn", got)
	}
	actions, ok := payload["actions"].([]any)
	if !ok || len(actions) != 1 {
		t.Fatalf("actions = %T %v, want one action", payload["actions"], payload["actions"])
	}
}

func TestExecuteSpawnSubagentToolNormalizesPlainTextResponse(t *testing.T) {
	app := newTestApp(t)

	result, err := app.executeSpawnSubagentToolWithRunner(
		context.Background(),
		AgentDefinition{AgentID: "openai:gpt-5.4", Model: "gpt-5.4"},
		"test-key",
		0,
		map[string]any{"task": "Review billing mail."},
		func(ctx context.Context, systemPrompt string, history []ConversationMessage, subagentDepth int) (string, []string, error) {
			return "Reviewed 3 billing threads. Stripe and AWS need follow-up; Vercel is informational.", nil, nil
		},
	)
	if err != nil {
		t.Fatalf("execute spawn_subagent: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got := payload["status"]; got != "completed" {
		t.Fatalf("status = %v, want completed", got)
	}
	answer, _ := payload["answer"].(string)
	if !strings.Contains(answer, "Stripe and AWS need follow-up") {
		t.Fatalf("answer = %q, missing normalized plain text", answer)
	}
}

func TestExecuteSpawnSubagentToolRejectsExcessiveDepth(t *testing.T) {
	app := newTestApp(t)

	_, err := app.executeSpawnSubagentToolWithRunner(
		context.Background(),
		AgentDefinition{AgentID: "openai:gpt-5.4", Model: "gpt-5.4"},
		"test-key",
		subagentMaxDepth,
		map[string]any{"task": "Anything"},
		func(ctx context.Context, systemPrompt string, history []ConversationMessage, subagentDepth int) (string, []string, error) {
			return `{"status":"completed","summary":"ok","answer":"ok"}`, nil, nil
		},
	)
	if err == nil {
		t.Fatal("expected depth-limit error")
	}
	if !strings.Contains(err.Error(), "nesting depth limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}
