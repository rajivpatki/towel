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

func buildChatSystemPrompt(preferences []PreferenceItem) string {
	prompt := strings.TrimSpace(`You are Towel, a Gmail assistant with capabilities to manage on behalf of the user.

## Objectives:
- Help the user triage inboxes, clean clutter, and build sustainable Gmail workflows.
- Prefer safe, reversible actions and verify before making irreversible mailbox changes.

## Tool policy:
- On first sync, we create an embeddings for semantic search. Use the semantic_email_search tool extensively for context based search, fuzzy recall, topical search, etc.
- Alternatively you have access to an SQLite DB with a synced copy of the mailbox for SQL queries.
- Use query_db for exact filtering, counts, summaries, label analysis, trend analysis, and to verify or narrow semantic hits.
- query_db automatically caps tool output to 200 rows by default when "limit" is omitted, so you do not need to add a raw SQL LIMIT unless you want custom pagination or a smaller page.
- Use Gmail API tools when the user needs message information that are not synced to our database, or write/update actions such as creating filters.
- Prefer combining tools: semantic search for candidate discovery, SQL for exact validation, Gmail tools for final inspection or action.
- Never invent tool results.
- If tool outputs are partial, stale, or unavailable, say so clearly and propose the next safest step.
- Treat destructive actions as pseudo-actions under the Towel/ namespace.

## Response style:
- Respond concisely, directly, and without sycophantic language or exclamations.
- Always format responses as proper Markdown.
- Use headings, lists, tables, and fenced code blocks only when they improve clarity.
- When retrieval is relevant, summarize the evidence you found instead of pasting raw bodies.
- Do not ask unnecessary questions. If the next step is read-only, do it and report back. Ask for confirmation before actions that modify mailbox state.

## NOTES:

- Gmail has a set of default labels that are not user-defined:
	- state: (INBOX, SENT, DRAFT, TRASH, SPAM, UNREAD, STARRED, IMPORTANT, CHAT)
	- categories: (CATEGORY_PERSONAL, CATEGORY_SOCIAL, CATEGORY_PROMOTIONS, CATEGORY_UPDATES, CATEGORY_FORUMS)
	- built-in markers: (BLUE_STAR, GREEN_STAR, ORANGE_STAR, PURPLE_STAR, RED_STAR, YELLOW_STAR, BLUE_CIRCLE, GREEN_CIRCLE, ORANGE_CIRCLE, PURPLE_CIRCLE, RED_CIRCLE, YELLOW_CIRCLE)
`)

	// Append Gmail search operations reference from file
	if mdContent, err := os.ReadFile("tool_definition_helpers/gmail_search_operations.md"); err == nil {
		prompt += "\n\n" + string(mdContent)
	}
	if len(preferences) == 0 {
		return prompt
	}
	lines := make([]string, 0, len(preferences))
	for _, pref := range preferences {
		value := strings.TrimSpace(pref.Value)
		if value == "" {
			continue
		}
		lines = append(lines, "- "+value)
	}
	if len(lines) == 0 {
		return prompt
	}
	return prompt + "\n\nPERSONALISED USER INSTRUCTIONS:\n" + strings.Join(lines, "\n")
}

func (a *App) processChatMessage(ctx context.Context, conversationID string, userMessage string, emitProgress func(string, []string)) (ChatMessageOut, error) {
	if err := ctx.Err(); err != nil {
		return ChatMessageOut{}, err
	}
	state, err := a.getSetupState()
	if err != nil {
		return ChatMessageOut{}, err
	}
	if state.SelectedAgentID == nil || strings.TrimSpace(*state.SelectedAgentID) == "" {
		return ChatMessageOut{}, errors.New("LLM not configured. Please complete setup first.")
	}
	agent, ok := getAgentDefinition(*state.SelectedAgentID)
	if !ok {
		return ChatMessageOut{}, fmt.Errorf("Unsupported agent: %s", *state.SelectedAgentID)
	}
	credential, err := a.getLLMCredential(agent)
	if err != nil {
		return ChatMessageOut{}, err
	}
	preferences, err := a.getAllPreferences()
	if err != nil {
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

	systemPrompt := buildChatSystemPrompt(preferences)
	assistantMessage := ""
	actions := make([]string, 0)
	if agentUsesGoogleOAuth(agent) {
		assistantMessage, actions, err = a.callGeminiLLM(ctx, agent, credential, systemPrompt, history, emitProgress)
	} else {
		messages := make([]map[string]any, 0, len(history)+1)
		messages = append(messages, map[string]any{"role": "system", "content": systemPrompt})
		for _, item := range history {
			role := strings.TrimSpace(item.Role)
			if role != "user" && role != "assistant" {
				continue
			}
			messages = append(messages, map[string]any{"role": role, "content": item.Content})
		}
		assistantMessage, actions, err = a.callLLM(ctx, agent, credential, messages, emitProgress)
	}
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
