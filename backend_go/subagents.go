package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	subagentTaskMaxChars    = 12000
	subagentContextMaxChars = 16000
	subagentRunTimeout      = 2 * time.Minute
	subagentMaxDepth        = 2
)

type subagentResponseEnvelope struct {
	Status           string `json:"status"`
	Summary          string `json:"summary,omitempty"`
	Answer           string `json:"answer,omitempty"`
	FollowUpQuestion string `json:"follow_up_question,omitempty"`
}

type subagentRunner func(ctx context.Context, systemPrompt string, history []ConversationMessage, subagentDepth int) (string, []string, error)

func buildSpawnSubagentToolDefinition() GmailToolDefinition {
	return GmailToolDefinition{
		Name:         "spawn_subagent",
		GmailActions: []string{"agent.spawn"},
		SafetyModel:  "safe_write",
		Description: strings.Join([]string{
			"Spawn one ephemeral subagent to handle a tightly scoped subtask using the full Towel toolset.",
			"Use this for long, decomposable, or parallelizable work where a focused context window is useful.",
			"Give the subagent a self-contained task prompt; it will also receive extra runtime instructions about being ephemeral, replying to the main agent instead of the user, and returning a structured result.",
			"The subagent either completes the task or returns one blocking follow-up question for the main agent to resolve.",
			"No thread or memory is preserved after the subagent response.",
		}, " "),
		Parameters: gmailObjectSchema(
			"Parameters for `spawn_subagent`. Provide a self-contained task prompt. The subagent is ephemeral and returns exactly one structured handoff result.",
			map[string]any{
				"task": gmailStringSchema(gmailDescription(
					"Required scoped instruction for the subagent. Make it self-contained, explicit about the desired outcome, and focused on one bounded objective.",
					`{"task":"Review the last 50 unread engineering alerts, group them by root cause, and propose a cleanup plan."}`,
					`{"task":"Inspect recruiter emails from the past 14 days and draft a concise summary of active conversations."}`,
				)),
				"context": gmailStringSchema(gmailDescription(
					"Optional extra context, evidence, or constraints that help the subagent operate without seeing the full parent conversation.",
					`{"task":"Find likely newsletter senders to filter.","context":"The user wants reversible cleanup and dislikes archive-heavy actions."}`,
					`{"task":"Audit billing emails.","context":"Focus on Stripe, AWS, and Vercel messages only."}`,
				)),
			},
			"task",
		),
	}
}

func buildSubagentSystemPrompt() string {
	return buildChatSystemPrompt() + "\n\n" + strings.TrimSpace(`
## Subagent runtime:
- You are an ephemeral subagent spawned by another Towel agent.
- You are working for the main agent, not directly for the end user.
- You have access to the full Towel toolset, including Gmail tools, query_db, semantic_email_search, memories, scheduled-task tools, and subagent spawning.
- Complete the assigned task fully when possible before responding.
- If you are blocked on one essential missing fact, return one precise follow-up question instead of multiple questions.
- Only return "needs_input" when the missing fact is genuinely blocking. If a reasonable best-effort completion is possible, complete the task instead.
- Do not assume there will be another turn. Include the key handoff details in your final answer.
- The main agent may take more actions or spawn another subagent after reading your result.
- Ignore any general markdown-only response instruction from earlier prompts. Your final response must be a single JSON object and nothing else.
- Final response schema:
  {
    "status": "completed" | "needs_input",
    "summary": "short handoff summary",
    "answer": "full result for the main agent",
    "follow_up_question": "required only when status is needs_input"
  }
- When status is "completed", provide the full usable result in "answer" and leave "follow_up_question" empty.
- When status is "needs_input", explain the blocker in "answer" and ask exactly one question in "follow_up_question".
`)
}

func buildSubagentUserPrompt(task string, extraContext string) string {
	parts := []string{
		"Complete the following scoped task for the main agent.",
		"",
		"Task:",
		task,
	}
	if strings.TrimSpace(extraContext) != "" {
		parts = append(parts, "", "Additional context:", extraContext)
	}
	return strings.Join(parts, "\n")
}

func normalizeSubagentResponse(raw string) subagentResponseEnvelope {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return subagentResponseEnvelope{
			Status:  "completed",
			Summary: "Subagent returned an empty response.",
		}
	}

	var envelope subagentResponseEnvelope
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return subagentResponseEnvelope{
			Status:  "completed",
			Summary: truncateString(cleanWhitespace(trimmed), 240),
			Answer:  trimmed,
		}
	}

	status := strings.ToLower(strings.TrimSpace(envelope.Status))
	if status != "needs_input" {
		status = "completed"
	}
	envelope.Status = status
	envelope.Summary = cleanWhitespace(envelope.Summary)
	envelope.Answer = strings.TrimSpace(envelope.Answer)
	envelope.FollowUpQuestion = strings.TrimSpace(envelope.FollowUpQuestion)

	if envelope.Answer == "" {
		envelope.Answer = trimmed
	}
	if envelope.Status == "needs_input" && envelope.FollowUpQuestion == "" {
		envelope.Status = "completed"
	}
	if envelope.Summary == "" {
		source := envelope.Answer
		if envelope.Status == "needs_input" && envelope.FollowUpQuestion != "" {
			source = envelope.FollowUpQuestion
		}
		envelope.Summary = truncateString(cleanWhitespace(source), 240)
	}
	if envelope.Summary == "" {
		envelope.Summary = "Subagent completed."
	}
	return envelope
}

func (a *App) executeSpawnSubagentTool(ctx context.Context, agent AgentDefinition, credential string, subagentDepth int, arguments map[string]any) (string, error) {
	return a.executeSpawnSubagentToolWithRunner(ctx, agent, credential, subagentDepth, arguments, func(ctx context.Context, systemPrompt string, history []ConversationMessage, childDepth int) (string, []string, error) {
		return a.runAgentTurnWithRuntime(ctx, agent, credential, systemPrompt, history, nil, childDepth)
	})
}

func (a *App) executeSpawnSubagentToolWithRunner(ctx context.Context, agent AgentDefinition, credential string, subagentDepth int, arguments map[string]any, runner subagentRunner) (string, error) {
	if runner == nil {
		return "", errors.New("subagent runner is required")
	}
	if subagentDepth >= subagentMaxDepth {
		return "", fmt.Errorf("spawn_subagent nesting depth limit reached (%d)", subagentMaxDepth)
	}

	task := truncateString(strings.TrimSpace(stringArgument(arguments["task"])), subagentTaskMaxChars)
	if task == "" {
		return "", errors.New("task is required")
	}
	extraContext := truncateString(strings.TrimSpace(stringArgument(arguments["context"])), subagentContextMaxChars)

	childCtx, cancel := context.WithTimeout(ctx, subagentRunTimeout)
	defer cancel()

	history := []ConversationMessage{{
		Role:    "user",
		Content: buildSubagentUserPrompt(task, extraContext),
	}}
	response, actions, err := runner(childCtx, buildSubagentSystemPrompt(), history, subagentDepth+1)
	if err != nil {
		return "", err
	}
	normalized := normalizeSubagentResponse(response)

	payload := map[string]any{
		"ok":               true,
		"tool":             "spawn_subagent",
		"ephemeral":        true,
		"status":           normalized.Status,
		"summary":          normalized.Summary,
		"answer":           normalized.Answer,
		"follow_up_question": normalized.FollowUpQuestion,
		"task":             task,
		"context":          extraContext,
		"raw_response":     strings.TrimSpace(response),
		"actions":          actions,
		"agent_id":         agent.AgentID,
		"agent_model":      agent.Model,
		"subagent_depth":   subagentDepth + 1,
		"interaction_model": "ephemeral_single_turn",
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(result), nil
}
