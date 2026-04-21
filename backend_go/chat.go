package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func buildChatSystemPrompt() string {
	prompt := strings.TrimSpace(`You are Towel, a Gmail assistant with capabilities to manage, analyse and declutter email on behalf of the user.

## Objective:
Your objective is to put in place as many mechanisms as possible to reduce the amount of prompting and interaction with you the user has to do over time. Use filters and scheduled tasks to pre-empt issues. You have a vast set of tools at your disposal which you can use liberally to gather maximum context to take actions or respond to the user.


## Tool policy:
- On first sync, we create embeddings for semantic search. Use the semantic email search tool extensively for context based search, fuzzy recall, topical search, etc.
- You also have access to an SQLite DB with a synced copy of the mailbox. Use query_db to run SQL queries for exact filtering, counts, summaries, analysis, trends, and to verify or narrow semantic hits.
- Use Gmail API tools when the user needs message information that are not synced to our database, or create/update actions on emails, filters, labels.
- Prefer combining tools: semantic search for candidate discovery, SQL for exact validation, Gmail tools for final inspection or action.
- Never invent tool results.


## Memory policy:
- Use search_memories when user-specific preferences, communication style, constraints, recurring workflows, or long-running context may matter for the current reply.
- It is not necessary to create memories on every turn. Create a memory only after the conversation has progressed enough to reveal a strong high-signal user preference or fact that will help in future conversations.
- Use memory search liberally to gather exhaustive context around the user's request. Minimise confirmations with the user by referring to memories.
- Do not store one-off requests, speculative inferences, mailbox content, secrets, access tokens, or sensitive data unless the user explicitly asked for that information to be remembered.
- When making a create_memory call, briefly state in your assistant message that you are saving that preference or fact as a memory. This keeps the thread history explicit so later turns can see that the memory was already captured.


## Response style:
- Respond concisely, directly, and without sycophantic language or exclamations.
- Always format responses as proper Markdown.
- Use headings, lists, tables, and fenced code blocks only when they improve clarity.
- When retrieval is relevant, summarize the evidence you found instead of pasting raw bodies.
- Do not ask unnecessary and impertinent questions. Gather more context from memories as well as tool calls. The user expects you to help them organise, declutter or analyse their email. Most actions that are not desctrucive (like creating labels, creating filters, applying labels) are appreciated. Post labelling archival or deletion warrants confirmation.
- Format responses to messages from Google Chat for the interface - markdown is not supported and tables need to be formatted as plain text.


## Scheduled tasks:
A powerful feature where you can execute agent instructions on each email received or updated. This functionality helps you clean up email in the future so the user does not have to prompt for it.
- Create tasks when you identify patterns in the user's request or email content that would benefit from automated handling.
- Use this feature profusely to expand the width of your capabilities. Evaluate each interaction with the user as an opportunity to set up a new scheduled task.
- However, if a scheduled task can be a simple filter instead, use that. Scheduled tasks are better for complex multi-step logic where email content may have to be parsed to take an action.
- Multiple scheduled tasks running on the same set of emails parallely can blow up token usage on the user's LLM API keys. It is always better to combine or coalesce multiple scheduled tasks into one.


## Notes:
- Gmail has a set of default labels. Do not treat these as user labels or even labels at all from the user's perspective. The user does not know that in a database these are used as labels:
	- state: (INBOX, SENT, DRAFT, TRASH, SPAM, UNREAD, STARRED, IMPORTANT, CHAT)
	- categories: (CATEGORY_PERSONAL, CATEGORY_SOCIAL, CATEGORY_PROMOTIONS, CATEGORY_UPDATES, CATEGORY_FORUMS)
	- built-in markers: (BLUE_STAR, GREEN_STAR, ORANGE_STAR, PURPLE_STAR, RED_STAR, YELLOW_STAR, BLUE_CIRCLE, GREEN_CIRCLE, ORANGE_CIRCLE, PURPLE_CIRCLE, RED_CIRCLE, YELLOW_CIRCLE)
`)

	// Append Gmail search operations reference from file
	if mdContent, err := os.ReadFile("prompt_helpers/gmail_search_operations.md"); err == nil {
		prompt += "\n\n" + string(mdContent)
	}
	return prompt
}

func buildScheduledTaskSystemPrompt() string {
	prompt := strings.TrimSpace(`You are Towel running an autonomous scheduled email task after a mailbox refresh.


## Execution model:
- This run has no user interface and no follow-up interaction.
- Never ask for confirmation, clarification, approval, or a later reply.
- If the task instruction calls for concrete hard actions, perform them now using the available tools.
- Work only on the updated emails included in the trigger context unless adjacent context is required to complete the task correctly.
- Prefer bulk Gmail actions when appropriate.
- Do not invent tool results.


## Tool policy:
- You have access to the full Towel toolset, including Gmail tools, query_db, semantic_email_search, memories, and scheduled-task tools.
- Use query_db and semantic_email_search for efficient analysis, but use Gmail tools for authoritative inspection or mailbox mutations.
- When the task instruction implies action, do not stop at analysis.


## Response style:
- Return a concise execution summary describing what you did, what you found, and any important limitation encountered.
- Do not frame the summary as a pending next step or request for user input.`)

	if mdContent, err := os.ReadFile("prompt_helpers/gmail_search_operations.md"); err == nil {
		prompt += "\n\n" + string(mdContent)
	}
	return prompt
}

func (a *App) resolveSelectedAgentAndCredential() (AgentDefinition, string, error) {
	state, err := a.getSetupState()
	if err != nil {
		return AgentDefinition{}, "", err
	}
	if state.SelectedAgentID == nil || strings.TrimSpace(*state.SelectedAgentID) == "" {
		return AgentDefinition{}, "", errors.New("LLM not configured. Please complete setup first.")
	}
	agent, ok := getAgentDefinition(*state.SelectedAgentID)
	if !ok {
		return AgentDefinition{}, "", fmt.Errorf("Unsupported agent: %s", *state.SelectedAgentID)
	}
	credential, err := a.getLLMCredential(agent)
	if err != nil {
		return AgentDefinition{}, "", err
	}
	return agent, credential, nil
}

func (a *App) runAgentTurn(ctx context.Context, systemPrompt string, history []ConversationMessage, emitProgress func(string, []string)) (string, []string, error) {
	agent, credential, err := a.resolveSelectedAgentAndCredential()
	if err != nil {
		return "", nil, err
	}
	if agentUsesGoogleOAuth(agent) {
		return a.callGeminiLLM(ctx, agent, credential, systemPrompt, history, emitProgress)
	}
	messages := make([]map[string]any, 0, len(history)+1)
	messages = append(messages, map[string]any{"role": "system", "content": systemPrompt})
	for _, item := range history {
		role := strings.TrimSpace(item.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		messages = append(messages, map[string]any{"role": role, "content": item.Content})
	}
	return a.callLLM(ctx, agent, credential, messages, emitProgress)
}

func (a *App) processChatMessage(ctx context.Context, conversationID string, userMessage string, emitProgress func(string, []string)) (ChatMessageOut, error) {
	if err := ctx.Err(); err != nil {
		return ChatMessageOut{}, err
	}
	if _, _, err := a.resolveSelectedAgentAndCredential(); err != nil {
		return ChatMessageOut{}, err
	}
	a.maybeSyncEmailsBeforeChat(userMessage)

	if err := a.ensureConversation(conversationID); err != nil {
		return ChatMessageOut{}, err
	}
	if err := a.saveConversationMessage(conversationID, "user", userMessage); err != nil {
		return ChatMessageOut{}, err
	}

	history, err := a.getConversationMessages(conversationID)
	if err != nil {
		return ChatMessageOut{}, err
	}

	systemPrompt := buildChatSystemPrompt()
	assistantMessage, actions, err := a.runAgentTurn(ctx, systemPrompt, history, emitProgress)
	if err != nil {
		return ChatMessageOut{}, fmt.Errorf("Chat processing failed: %v", err)
	}
	assistantMessage = strings.TrimSpace(assistantMessage)
	if assistantMessage == "" {
		return ChatMessageOut{}, errors.New("Chat processing failed: model returned an empty response")
	}

	if err := a.saveConversationMessage(conversationID, "assistant", assistantMessage); err != nil {
		return ChatMessageOut{}, err
	}

	return ChatMessageOut{
		ConversationID: conversationID,
		Response:       assistantMessage,
		Actions:        actions,
	}, nil
}

func emitProgressUpdate(emitProgress func(string, []string), content string, actions []string) {
	if emitProgress == nil {
		return
	}
	actionsCopy := append([]string(nil), actions...)
	emitProgress(strings.TrimSpace(content), actionsCopy)
}

type toolCallExecutionResult struct {
	Index      int
	Call       llmToolCall
	Result     string
	Action     string
	ActionType string
}

func (a *App) executeToolCallsParallel(toolCalls []llmToolCall) []toolCallExecutionResult {
	if len(toolCalls) == 0 {
		return nil
	}

	results := make([]toolCallExecutionResult, len(toolCalls))
	resultCh := make(chan toolCallExecutionResult, len(toolCalls))

	for index, toolCall := range toolCalls {
		go func(index int, toolCall llmToolCall) {
			result, action, actionType := a.executeToolCall(toolCall)
			resultCh <- toolCallExecutionResult{
				Index:      index,
				Call:       toolCall,
				Result:     result,
				Action:     action,
				ActionType: actionType,
			}
		}(index, toolCall)
	}

	for range toolCalls {
		item := <-resultCh
		results[item.Index] = item
	}

	return results
}

func (a *App) callLLM(ctx context.Context, agent AgentDefinition, apiKey string, messages []map[string]any, emitProgress func(string, []string)) (string, []string, error) {
	tools := buildLLMToolsPayload()
	actions := make([]string, 0)
	latestContent := ""

	for iteration := 0; iteration < maxToolCallIterations; iteration++ {
		if err := ctx.Err(); err != nil {
			return "", actions, err
		}
		message, err := a.callLLMOnce(ctx, agent, apiKey, messages, tools)
		if err != nil {
			return "", actions, err
		}

		content := stringifyLLMContent(message.Content)
		if strings.TrimSpace(content) != "" {
			latestContent = strings.TrimSpace(content)
			emitProgressUpdate(emitProgress, latestContent, actions)
		}
		if len(message.ToolCalls) == 0 {
			if strings.TrimSpace(content) == "" {
				return "", actions, errors.New("upstream LLM returned empty content")
			}
			return content, actions, nil
		}

		assistantMessage := map[string]any{
			"role":       "assistant",
			"tool_calls": convertToolCalls(message.ToolCalls),
			"content":    "",
		}
		if message.Content != nil {
			assistantMessage["content"] = message.Content
		}
		messages = append(messages, assistantMessage)

		toolResults := a.executeToolCallsParallel(message.ToolCalls)
		for _, toolResult := range toolResults {
			actions = append(actions, toolResult.Action)
			emitProgressUpdate(emitProgress, latestContent, actions)
			if err := a.logActionHistory(toolResult.ActionType, toolResult.Action, toolResult.Result); err != nil {
				return "", actions, fmt.Errorf("failed to record tool call: %w", err)
			}

			toolCallID := strings.TrimSpace(toolResult.Call.ID)
			if toolCallID == "" {
				toolCallID = fmt.Sprintf("toolcall_%d_%d", iteration, toolResult.Index)
			}
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": toolCallID,
				"content":      toolResult.Result,
			})
		}
	}

	return "", actions, fmt.Errorf("upstream LLM exceeded tool-call iteration limit (%d)", maxToolCallIterations)
}

func buildLLMToolsPayload() []map[string]any {
	definitions := allToolDefinitionsSnapshot()
	tools := make([]map[string]any, 0, len(definitions))
	for _, tool := range definitions {
		functionName := strings.ReplaceAll(tool.Name, ".", "_")
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			}
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        functionName,
				"description": tool.Description,
				"parameters":  parameters,
			},
		})
	}
	return tools
}

func convertToolCalls(toolCalls []llmToolCall) []map[string]any {
	converted := make([]map[string]any, 0, len(toolCalls))
	for _, call := range toolCalls {
		callType := strings.TrimSpace(call.Type)
		if callType == "" {
			callType = "function"
		}
		converted = append(converted, map[string]any{
			"id":   call.ID,
			"type": callType,
			"function": map[string]any{
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			},
		})
	}
	return converted
}

func (a *App) callLLMOnce(ctx context.Context, agent AgentDefinition, apiKey string, messages []map[string]any, tools []map[string]any) (llmResponseMessage, error) {
	requestPayload := map[string]any{
		"model":       agent.Model,
		"messages":    messages,
		"temperature": 0.7,
		"tools":       tools,
		"tool_choice": "auto",
		"stream":      false,
	}
	body, err := json.Marshal(requestPayload)
	if err != nil {
		return llmResponseMessage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(agent.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return llmResponseMessage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return llmResponseMessage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		responseBody, _ := io.ReadAll(resp.Body)
		return llmResponseMessage{}, fmt.Errorf("upstream LLM request failed: %s", strings.TrimSpace(string(responseBody)))
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return llmResponseMessage{}, err
	}
	return parseOpenAIChatCompletionResponse(responseBody)
}

func parseOpenAIChatCompletionResponse(responseBody []byte) (llmResponseMessage, error) {
	var payload struct {
		Choices []struct {
			Message llmResponseMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return llmResponseMessage{}, err
	}
	if len(payload.Choices) == 0 {
		return llmResponseMessage{}, errors.New("upstream LLM response did not include any choices")
	}
	message := payload.Choices[0].Message
	if strings.TrimSpace(message.Role) == "" {
		message.Role = "assistant"
	}
	if stringifyLLMContent(message.Content) == "" && len(message.ToolCalls) == 0 {
		return llmResponseMessage{}, errors.New("upstream LLM response did not include any content")
	}
	return message, nil
}

func (a *App) executeToolCall(call llmToolCall) (string, string, string) {
	argumentsText := strings.TrimSpace(call.Function.Arguments)
	arguments := map[string]any{}
	if argumentsText != "" {
		if err := json.Unmarshal([]byte(argumentsText), &arguments); err != nil {
			arguments = map[string]any{"raw": argumentsText}
		}
	}

	toolName := call.Function.Name
	safetyModel := "unknown"
	toolDescription := "Tool definition not found."
	if definition, ok := getToolDefinitionByFunctionName(call.Function.Name); ok {
		toolName = definition.Name
		safetyModel = definition.SafetyModel
		toolDescription = definition.Description
	}

	// Execute real Gmail tool if available
	var resultJSON string
	var execErr error
	if strings.HasPrefix(toolName, "users.") {
		resultJSON, execErr = a.executeGmailTool(toolName, arguments)
	} else if toolName == "query_db" {
		resultJSON, execErr = a.executeQueryDBTool(arguments)
	} else if toolName == "semantic_email_search" {
		resultJSON, execErr = a.executeSemanticEmailSearchTool(arguments)
	} else if toolName == "create_memory" {
		resultJSON, execErr = a.executeCreateMemoryTool(arguments)
	} else if toolName == "search_memories" {
		resultJSON, execErr = a.executeSearchMemoriesTool(arguments)
	} else if toolName == "create_scheduled_task" {
		resultJSON, execErr = a.executeCreateScheduledTaskTool(arguments)
	} else if toolName == "update_scheduled_task" {
		resultJSON, execErr = a.executeUpdateScheduledTaskTool(arguments)
	} else if toolName == "search_scheduled_tasks" {
		resultJSON, execErr = a.executeSearchScheduledTasksTool(arguments)
	} else if toolName == "delete_scheduled_task" {
		resultJSON, execErr = a.executeDeleteScheduledTaskTool(arguments)
	}

	if execErr != nil {
		// Return error as result so LLM can see what went wrong
		resultPayload := map[string]any{
			"ok":           false,
			"tool":         toolName,
			"safety_model": safetyModel,
			"description":  toolDescription,
			"arguments":    arguments,
			"error":        execErr.Error(),
		}
		resultBytes, _ := json.Marshal(resultPayload)
		action := fmt.Sprintf("Failed to execute %s: %s", toolName, truncateString(execErr.Error(), 120))
		return string(resultBytes), action, toolName
	}

	if resultJSON == "" {
		// Fallback for non-Gmail tools or unexpected cases
		resultPayload := map[string]any{
			"ok":           true,
			"tool":         toolName,
			"safety_model": safetyModel,
			"description":  toolDescription,
			"arguments":    arguments,
			"result":       "Tool executed (no specific implementation for this tool).",
		}
		resultBytes, _ := json.Marshal(resultPayload)
		resultJSON = string(resultBytes)
	}

	action := "Executed tool " + toolName + "."
	if argumentsText != "" {
		action = fmt.Sprintf("Executed tool %s with args %s.", toolName, truncateString(argumentsText, 180))
	}

	return resultJSON, action, toolName
}

func getToolDefinitionByFunctionName(functionName string) (GmailToolDefinition, bool) {
	for _, tool := range allToolDefinitionsSnapshot() {
		if strings.ReplaceAll(tool.Name, ".", "_") == functionName {
			return tool, true
		}
	}
	return GmailToolDefinition{}, false
}

func stringifyLLMContent(content any) string {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if piece, ok := item.(map[string]any); ok {
				if text, ok := piece["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "")
		}
		encoded, _ := json.Marshal(value)
		if string(encoded) == "null" {
			return ""
		}
		return string(encoded)
	default:
		encoded, _ := json.Marshal(value)
		if string(encoded) == "null" {
			return ""
		}
		return string(encoded)
	}
}
