package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

type geminiFunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response,omitempty"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenerateCandidate struct {
	Content geminiContent `json:"content"`
}

type geminiGenerateResponse struct {
	Candidates []geminiGenerateCandidate `json:"candidates"`
}

type googleAPIErrorEnvelope struct {
	Error struct {
		Code    int              `json:"code"`
		Message string           `json:"message"`
		Status  string           `json:"status"`
		Details []map[string]any `json:"details"`
	} `json:"error"`
}

func (a *App) getLLMCredential(agent AgentDefinition) (string, error) {
	if agentUsesGoogleOAuth(agent) {
		return a.getGmailAccessToken()
	}
	apiKey, err := a.getSecret("llm_api_key")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(apiKey) == "" {
		return "", errors.New("LLM API key not found. Please complete setup first.")
	}
	return apiKey, nil
}

func (a *App) googleQuotaProjectHint() string {
	clientID, err := a.getSecret("google_client_id")
	if err != nil {
		return ""
	}
	prefix := strings.TrimSpace(strings.SplitN(strings.TrimSpace(clientID), "-", 2)[0])
	if prefix == "" {
		return ""
	}
	for _, r := range prefix {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return prefix
}

func (a *App) probeGeminiAccess(agent AgentDefinition) (GeminiSetupOut, error) {
	accessToken, err := a.getGmailAccessToken()
	if err != nil {
		return GeminiSetupOut{}, err
	}
	requestURL := strings.TrimRight(agent.BaseURL, "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return GeminiSetupOut{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if quotaProject := a.googleQuotaProjectHint(); quotaProject != "" {
		req.Header.Set("X-Goog-User-Project", quotaProject)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return GeminiSetupOut{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return GeminiSetupOut{}, err
	}
	if resp.StatusCode < 400 {
		log.Printf("gemini probe success: agent=%s status=%d", agent.AgentID, resp.StatusCode)
		return GeminiSetupOut{
			Success: true,
			Status:  "ready",
			Detail:  "Gemini is ready to use with your Google account.",
		}, nil
	}
	log.Printf("gemini probe failed: agent=%s status=%d body=%s", agent.AgentID, resp.StatusCode, strings.TrimSpace(string(body)))
	return interpretGeminiSetupFailure(resp.StatusCode, body), nil
}

func interpretGeminiSetupFailure(statusCode int, body []byte) GeminiSetupOut {
	message := strings.TrimSpace(string(body))
	reason := ""
	errorStatus := ""
	var payload googleAPIErrorEnvelope
	if err := json.Unmarshal(body, &payload); err == nil {
		if strings.TrimSpace(payload.Error.Message) != "" {
			message = strings.TrimSpace(payload.Error.Message)
		}
		errorStatus = strings.TrimSpace(payload.Error.Status)
		for _, detail := range payload.Error.Details {
			if value, ok := detail["reason"].(string); ok && strings.TrimSpace(value) != "" {
				reason = strings.TrimSpace(value)
				break
			}
		}
	}
	lowerMessage := strings.ToLower(message)
	lowerReason := strings.ToLower(reason)
	lowerErrorStatus := strings.ToLower(errorStatus)
	log.Printf("gemini probe classification: http_status=%d error_status=%q reason=%q message=%q", statusCode, errorStatus, reason, message)
	if strings.Contains(lowerReason, "service_disabled") ||
		strings.Contains(lowerReason, "access_not_configured") ||
		strings.Contains(lowerErrorStatus, "permission_denied") && strings.Contains(lowerMessage, "has not been used in project") ||
		strings.Contains(lowerMessage, "generativelanguage.googleapis.com") && strings.Contains(lowerMessage, "enable") ||
		strings.Contains(lowerMessage, "google generative language api") && strings.Contains(lowerMessage, "not enabled") ||
		strings.Contains(lowerMessage, "has not been used in project") ||
		strings.Contains(lowerMessage, "api has not been used") {
		log.Printf("gemini probe result: classified as api_disabled")
		return GeminiSetupOut{
			Success:   false,
			Status:    "api_disabled",
			Detail:    "Google Generative Language API is not enabled for this Google Cloud project yet.",
			EnableURL: geminiEnableAPIURL,
		}
	}
	if strings.Contains(lowerReason, "access_token_scope_insufficient") || strings.Contains(lowerMessage, "insufficient authentication scopes") {
		log.Printf("gemini probe result: classified as needs_reauth")
		return GeminiSetupOut{
			Success: false,
			Status:  "needs_reauth",
			Detail:  "Insufficient authentication scopes. Please reconnect your Google account and ensure you check all requested permission boxes.",
		}
	}
	log.Printf("gemini probe result: classified as generic_error")
	return GeminiSetupOut{
		Success: false,
		Status:  "error",
		Detail:  message,
	}
}

func (a *App) callGeminiLLM(ctx context.Context, agent AgentDefinition, accessToken string, systemPrompt string, history []ConversationMessage, emitProgress func(string, []string)) (string, []string, error) {
	contents := buildGeminiConversation(history)
	actions := make([]string, 0)
	latestContent := ""
	for iteration := 0; iteration < maxToolCallIterations; iteration++ {
		if err := ctx.Err(); err != nil {
			return "", actions, err
		}
		candidate, message, err := a.callGeminiOnce(ctx, agent, accessToken, systemPrompt, contents)
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
				return "", actions, errors.New("upstream Gemini response returned empty content")
			}
			return content, actions, nil
		}
		contents = append(contents, candidate.Content)
		toolResults := a.executeToolCallsParallel(message.ToolCalls)
		for _, toolResult := range toolResults {
			actions = append(actions, toolResult.Action)
			emitProgressUpdate(emitProgress, latestContent, actions)
			if err := a.logActionHistory(toolResult.ActionType, toolResult.Action, toolResult.Result); err != nil {
				return "", actions, fmt.Errorf("failed to record tool call: %w", err)
			}
			toolCallID := strings.TrimSpace(toolResult.Call.ID)
			if toolCallID == "" {
				toolCallID = fmt.Sprintf("gemini_toolcall_%d_%d", iteration, toolResult.Index)
			}
			contents = append(contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{{
					FunctionResponse: &geminiFunctionResponse{
						ID:       toolCallID,
						Name:     toolResult.Call.Function.Name,
						Response: geminiFunctionResultPayload(toolResult.Result),
					},
				}},
			})
		}
	}
	return "", actions, fmt.Errorf("upstream Gemini exceeded tool-call iteration limit (%d)", maxToolCallIterations)
}

func buildGeminiConversation(history []ConversationMessage) []geminiContent {
	contents := make([]geminiContent, 0, len(history))
	for _, item := range history {
		role := strings.TrimSpace(item.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		geminiRole := "user"
		if role == "assistant" {
			geminiRole = "model"
		}
		contents = append(contents, geminiContent{
			Role: geminiRole,
			Parts: []geminiPart{{
				Text: item.Content,
			}},
		})
	}
	return contents
}

func sanitizeGeminiSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		sanitized := make(map[string]any, 0)
		for key, item := range typed {
			if key == "additionalProperties" {
				continue
			}
			sanitized[key] = sanitizeGeminiSchemaValue(item)
		}
		return sanitized
	case []any:
		sanitized := make([]any, 0, len(typed))
		for _, item := range typed {
			sanitized = append(sanitized, sanitizeGeminiSchemaValue(item))
		}
		return sanitized
	default:
		return value
	}
}

func sanitizeGeminiParameters(parameters map[string]any) map[string]any {
	if parameters == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	sanitized, ok := sanitizeGeminiSchemaValue(parameters).(map[string]any)
	if !ok {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	return sanitized
}

func buildGeminiToolsPayload() []map[string]any {
	definitions := allToolDefinitionsSnapshot()
	declarations := make([]map[string]any, 0, len(definitions))
	for _, tool := range definitions {
		parameters := sanitizeGeminiParameters(tool.Parameters)
		declarations = append(declarations, map[string]any{
			"name":        strings.ReplaceAll(tool.Name, ".", "_"),
			"description": tool.Description,
			"parameters":  parameters,
		})
	}
	return []map[string]any{{
		"functionDeclarations": declarations,
	}}
}

func (a *App) callGeminiOnce(ctx context.Context, agent AgentDefinition, accessToken string, systemPrompt string, contents []geminiContent) (geminiGenerateCandidate, llmResponseMessage, error) {
	requestPayload := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{
				"text": systemPrompt,
			}},
		},
		"contents": contents,
		"tools":    buildGeminiToolsPayload(),
		"toolConfig": map[string]any{
			"functionCallingConfig": map[string]any{
				"mode": "AUTO",
			},
		},
		"generationConfig": map[string]any{
			"temperature": 0.7,
		},
	}
	body, err := json.Marshal(requestPayload)
	if err != nil {
		return geminiGenerateCandidate{}, llmResponseMessage{}, err
	}
	requestURL := strings.TrimRight(agent.BaseURL, "/") + "/models/" + agent.Model + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return geminiGenerateCandidate{}, llmResponseMessage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if quotaProject := a.googleQuotaProjectHint(); quotaProject != "" {
		req.Header.Set("X-Goog-User-Project", quotaProject)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return geminiGenerateCandidate{}, llmResponseMessage{}, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return geminiGenerateCandidate{}, llmResponseMessage{}, err
	}
	if resp.StatusCode >= 400 {
		log.Printf("gemini generateContent failed: agent=%s status=%d body=%s", agent.AgentID, resp.StatusCode, strings.TrimSpace(string(responseBody)))
		setupFailure := interpretGeminiSetupFailure(resp.StatusCode, responseBody)
		return geminiGenerateCandidate{}, llmResponseMessage{}, fmt.Errorf("upstream Gemini request failed: %s", setupFailure.Detail)
	}
	var payload geminiGenerateResponse
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return geminiGenerateCandidate{}, llmResponseMessage{}, err
	}
	if len(payload.Candidates) == 0 {
		return geminiGenerateCandidate{}, llmResponseMessage{}, errors.New("upstream Gemini response did not include any candidates")
	}
	candidate := payload.Candidates[0]
	message := llmResponseMessage{
		Role:    "assistant",
		Content: stringifyGeminiPartsText(candidate.Content.Parts),
	}
	toolCalls := make([]llmToolCall, 0)
	for index, part := range candidate.Content.Parts {
		if part.FunctionCall == nil {
			continue
		}
		argumentsBytes, err := json.Marshal(part.FunctionCall.Args)
		if err != nil {
			return geminiGenerateCandidate{}, llmResponseMessage{}, err
		}
		toolCallID := strings.TrimSpace(part.FunctionCall.ID)
		if toolCallID == "" {
			toolCallID = fmt.Sprintf("gemini_call_%d", index)
		}
		toolCalls = append(toolCalls, llmToolCall{
			ID:   toolCallID,
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{
				Name:      strings.TrimSpace(part.FunctionCall.Name),
				Arguments: string(argumentsBytes),
			},
		})
	}
	message.ToolCalls = toolCalls
	return candidate, message, nil
}

func stringifyGeminiPartsText(parts []geminiPart) string {
	pieces := make([]string, 0)
	for _, part := range parts {
		if strings.TrimSpace(part.Text) != "" {
			pieces = append(pieces, part.Text)
		}
	}
	return strings.Join(pieces, "")
}

func geminiFunctionResultPayload(result string) map[string]any {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return map[string]any{"result": ""}
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		return decoded
	}
	var generic any
	if err := json.Unmarshal([]byte(trimmed), &generic); err == nil {
		return map[string]any{"result": generic}
	}
	return map[string]any{"result": trimmed}
}
