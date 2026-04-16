package main

import (
	"bytes"
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
- Prefer safe, reversible actions.

## Tool policy:
- Use tools when a claim depends on mailbox state.
- Never invent tool results.
- If tool outputs are partial or placeholder, say so clearly and propose the next safest step.
- Treat destructive actions as pseudo-actions under the Towel/ namespace.

## Response style:
- Respond in very short and concise statement without sycophantic language or exclammations.
- Always format responses as proper Markdown. You should use inline html tags to make the response more versatile.
- Use headings, lists, tables, blockquotes, and fenced code blocks when they improve readability.
- Do not nag the user with impertinent questions. For instance, before you confirm an action with a user check if the action that you are confirming requires the use of tools that modify (delete, archive, update). If not, attend to the user's request, present the output and then confirm if that is what the user wanted.
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

func (a *App) processChatMessage(conversationID string, userMessage string, emitToken func(string)) (ChatMessageOut, error) {
	state, err := a.getSetupState()
	if err != nil {
		return ChatMessageOut{}, err
	}
	if state.SelectedAgentID == nil || strings.TrimSpace(*state.SelectedAgentID) == "" {
		return ChatMessageOut{}, errors.New("LLM not configured. Please complete setup first.")
	}
	apiKey, err := a.getSecret("llm_api_key")
	if err != nil {
		return ChatMessageOut{}, err
	}
	if strings.TrimSpace(apiKey) == "" {
		return ChatMessageOut{}, errors.New("LLM API key not found. Please complete setup first.")
	}
	agent, ok := getAgentDefinition(*state.SelectedAgentID)
	if !ok {
		return ChatMessageOut{}, fmt.Errorf("Unsupported agent: %s", *state.SelectedAgentID)
	}
	preferences, err := a.getAllPreferences()
	if err != nil {
		return ChatMessageOut{}, err
	}

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
	messages := make([]map[string]any, 0, len(history)+1)
	messages = append(messages, map[string]any{"role": "system", "content": systemPrompt})
	for _, item := range history {
		role := strings.TrimSpace(item.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		messages = append(messages, map[string]any{"role": role, "content": item.Content})
	}

	assistantMessage, actions, err := a.callLLM(agent, apiKey, messages, emitToken)
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

func (a *App) callLLM(agent AgentDefinition, apiKey string, messages []map[string]any, emitToken func(string)) (string, []string, error) {
	tools := buildLLMToolsPayload()
	actions := make([]string, 0)

	for iteration := 0; iteration < maxToolCallIterations; iteration++ {
		message, err := a.callLLMOnce(agent, apiKey, messages, tools)
		if err != nil {
			return "", actions, err
		}

		content := stringifyLLMContent(message.Content)
		if len(message.ToolCalls) == 0 {
			if strings.TrimSpace(content) == "" {
				return "", actions, errors.New("upstream LLM returned empty content")
			}
			emitTokenizedText(content, emitToken)
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

		if text := strings.TrimSpace(content); text != "" {
			note := "Assistant: " + text
			actions = append(actions, note)
			emitTokenizedText(note+"\n", emitToken)
		}

		for toolIndex, toolCall := range message.ToolCalls {
			result, action, actionType := a.executeToolCall(toolCall)
			actions = append(actions, action)
			emitTokenizedText(action+"\n", emitToken)
			if err := a.logActionHistory(actionType, action, result); err != nil {
				return "", actions, fmt.Errorf("failed to record tool call: %w", err)
			}

			toolCallID := strings.TrimSpace(toolCall.ID)
			if toolCallID == "" {
				toolCallID = fmt.Sprintf("toolcall_%d_%d", iteration, toolIndex)
			}
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": toolCallID,
				"content":      result,
			})
		}
		emitTokenizedText("\n", emitToken)
	}

	return "", actions, fmt.Errorf("upstream LLM exceeded tool-call iteration limit (%d)", maxToolCallIterations)
}

func buildLLMToolsPayload() []map[string]any {
	tools := make([]map[string]any, 0, len(gmailToolDefinitions))
	for _, tool := range gmailToolDefinitions {
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

func (a *App) callLLMOnce(agent AgentDefinition, apiKey string, messages []map[string]any, tools []map[string]any) (llmResponseMessage, error) {
	requestPayload := map[string]any{
		"model":                 agent.Model,
		"messages":              messages,
		"temperature":           0.7,
		"max_completion_tokens": 1000,
		"tools":                 tools,
		"tool_choice":           "auto",
	}
	body, err := json.Marshal(requestPayload)
	if err != nil {
		return llmResponseMessage{}, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(agent.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
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
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return llmResponseMessage{}, err
	}
	if resp.StatusCode >= 400 {
		return llmResponseMessage{}, fmt.Errorf("upstream LLM request failed: %s", strings.TrimSpace(string(responseBody)))
	}
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
	return payload.Choices[0].Message, nil
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
	for _, tool := range gmailToolDefinitions {
		if strings.ReplaceAll(tool.Name, ".", "_") == functionName {
			return tool, true
		}
	}
	return GmailToolDefinition{}, false
}

func emitTokenizedText(value string, emitToken func(string)) {
	if emitToken == nil || value == "" {
		return
	}
	for _, token := range splitTextTokens(value) {
		emitToken(token)
	}
}

func splitTextTokens(value string) []string {
	tokens := make([]string, 0)
	var builder strings.Builder
	runeCount := 0
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		tokens = append(tokens, builder.String())
		builder.Reset()
		runeCount = 0
	}
	for _, r := range value {
		builder.WriteRune(r)
		runeCount++
		if r == ' ' || r == '\n' || runeCount >= 24 {
			flush()
		}
	}
	flush()
	return tokens
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
