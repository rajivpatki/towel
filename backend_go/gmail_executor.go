package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const gmailAPIBase = "https://www.googleapis.com/gmail/v1"
const googleTokenEndpoint = "https://oauth2.googleapis.com/token"

// TokenBundle represents the stored Google OAuth token response
type TokenBundle struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
}

func (a *App) getGmailAccessToken() (string, error) {
	tokenJSON, err := a.getSecret("google_token_bundle")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(tokenJSON) == "" {
		return "", errors.New("Google account not connected")
	}

	var bundle TokenBundle
	if err := json.Unmarshal([]byte(tokenJSON), &bundle); err != nil {
		return "", fmt.Errorf("invalid token bundle: %w", err)
	}
	if strings.TrimSpace(bundle.AccessToken) == "" {
		return "", errors.New("access token not available")
	}
	return bundle.AccessToken, nil
}

// refreshGmailToken attempts to refresh the access token using the refresh token
func (a *App) refreshGmailToken() (string, error) {
	tokenJSON, err := a.getSecret("google_token_bundle")
	if err != nil {
		return "", err
	}

	var bundle TokenBundle
	if err := json.Unmarshal([]byte(tokenJSON), &bundle); err != nil {
		return "", fmt.Errorf("invalid token bundle: %w", err)
	}
	if strings.TrimSpace(bundle.RefreshToken) == "" {
		return "", errors.New("no refresh token available - please reconnect Gmail")
	}

	clientID, err := a.getSecret("google_client_id")
	if err != nil {
		return "", err
	}
	clientSecret, err := a.getSecret("google_client_secret")
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", bundle.RefreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequest(http.MethodPost, googleTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token refresh failed: %s", string(body))
	}

	var refreshResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(body, &refreshResp); err != nil {
		return "", err
	}

	// Update stored bundle with new access token
	bundle.AccessToken = refreshResp.AccessToken
	if refreshResp.ExpiresIn > 0 {
		bundle.ExpiresIn = refreshResp.ExpiresIn
	}
	if refreshResp.TokenType != "" {
		bundle.TokenType = refreshResp.TokenType
	}

	updatedJSON, err := json.Marshal(bundle)
	if err != nil {
		return "", err
	}

	if err := a.upsertSecret("google_token_bundle", string(updatedJSON)); err != nil {
		return "", fmt.Errorf("failed to store refreshed token: %w", err)
	}

	return bundle.AccessToken, nil
}

func (a *App) executeGmailTool(toolName string, arguments map[string]any) (string, error) {
	accessToken, err := a.getGmailAccessToken()
	if err != nil {
		return "", fmt.Errorf("Gmail auth error: %w", err)
	}

	result, statusCode, err := a.tryExecuteGmailTool(toolName, accessToken, arguments)
	if err != nil && (statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden) {
		newToken, refreshErr := a.refreshGmailToken()
		if refreshErr != nil {
			return "", fmt.Errorf("Gmail token expired and refresh failed: %w (original error: %v)", refreshErr, err)
		}
		result, statusCode, err = a.tryExecuteGmailTool(toolName, newToken, arguments)
	}
	return result, err
}

func (a *App) tryExecuteGmailTool(toolName, accessToken string, arguments map[string]any) (string, int, error) {
	switch toolName {
	case "gmail.list_labels":
		return a.gmailListLabels(accessToken)
	case "gmail.create_towel_label":
		return a.gmailCreateTowelLabel(accessToken, arguments)
	case "gmail.list_messages":
		return a.gmailListMessages(accessToken, arguments)
	case "gmail.read_message":
		return a.gmailReadMessage(accessToken, arguments)
	case "gmail.move_to_towel_spam":
		return a.gmailMoveToTowelSpam(accessToken, arguments)
	case "gmail.move_to_towel_delete":
		return a.gmailMoveToTowelDelete(accessToken, arguments)
	case "gmail.create_filter":
		return a.gmailCreateFilter(accessToken, arguments)
	case "gmail.sender_analytics":
		return a.gmailSenderAnalytics(accessToken, arguments)
	default:
		return "", 0, fmt.Errorf("unknown Gmail tool: %s", toolName)
	}
}

// doGmailRequest executes an HTTP request and returns the response body and status code
func (a *App) doGmailRequest(req *http.Request) ([]byte, int, error) {
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// gmailListLabels lists all labels in the user's mailbox, returns (result, httpStatus, error)
func (a *App) gmailListLabels(accessToken string) (string, int, error) {
	req, err := http.NewRequest(http.MethodGet, gmailAPIBase+"/users/me/labels", nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	if status != http.StatusOK {
		return "", status, fmt.Errorf("Gmail API error (list_labels): %s", string(body))
	}

	var result struct {
		Labels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", status, err
	}

	labels := make([]map[string]string, 0, len(result.Labels))
	for _, label := range result.Labels {
		labels = append(labels, map[string]string{
			"id":   label.ID,
			"name": label.Name,
		})
	}

	output, _ := json.Marshal(map[string]any{
		"labels": labels,
		"count":  len(labels),
	})
	return string(output), status, nil
}

// gmailCreateTowelLabel creates a label under Towel/ hierarchy
func (a *App) gmailCreateTowelLabel(accessToken string, arguments map[string]any) (string, int, error) {
	labelName := "Towel/Organized"
	if name, ok := arguments["name"].(string); ok && strings.TrimSpace(name) != "" {
		labelName = strings.TrimSpace(name)
		if !strings.HasPrefix(labelName, "Towel/") {
			labelName = "Towel/" + labelName
		}
	}

	payload := map[string]any{
		"name":                  labelName,
		"labelListVisibility":   "labelShow",
		"messageListVisibility": "show",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequest(http.MethodPost, gmailAPIBase+"/users/me/labels", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	if status != http.StatusOK {
		return "", status, fmt.Errorf("Gmail API error (create_label): %s", string(respBody))
	}

	var result struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", status, err
	}

	output, _ := json.Marshal(map[string]any{
		"success":    true,
		"label_id":   result.ID,
		"label_name": result.Name,
	})
	return string(output), status, nil
}

// gmailListMessages searches for messages with optional query
func (a *App) gmailListMessages(accessToken string, arguments map[string]any) (string, int, error) {
	q := ""
	if query, ok := arguments["query"].(string); ok {
		q = strings.TrimSpace(query)
	}
	maxResults := 20
	if max, ok := arguments["max_results"].(float64); ok && max > 0 {
		maxResults = int(max)
		if maxResults > 100 {
			maxResults = 100
		}
	}

	params := url.Values{}
	params.Set("maxResults", strconv.Itoa(maxResults))
	if q != "" {
		params.Set("q", q)
	}

	req, err := http.NewRequest(http.MethodGet, gmailAPIBase+"/users/me/messages?"+params.Encode(), nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	if status != http.StatusOK {
		return "", status, fmt.Errorf("Gmail API error (list_messages): %s", string(body))
	}

	var result struct {
		Messages []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"messages"`
		ResultSizeEstimate int `json:"resultSizeEstimate"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", status, err
	}

	output, _ := json.Marshal(map[string]any{
		"messages":             result.Messages,
		"result_size_estimate": result.ResultSizeEstimate,
	})
	return string(output), status, nil
}

// gmailReadMessage fetches full message details
func (a *App) gmailReadMessage(accessToken string, arguments map[string]any) (string, int, error) {
	messageID := ""
	if id, ok := arguments["message_id"].(string); ok {
		messageID = strings.TrimSpace(id)
	}
	if messageID == "" {
		return "", 0, errors.New("message_id is required")
	}

	req, err := http.NewRequest(http.MethodGet, gmailAPIBase+"/users/me/messages/"+url.PathEscape(messageID), nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	if status != http.StatusOK {
		return "", status, fmt.Errorf("Gmail API error (read_message): %s", string(body))
	}

	var result struct {
		ID       string   `json:"id"`
		ThreadID string   `json:"threadId"`
		LabelIDs []string `json:"labelIds"`
		Snippet  string   `json:"snippet"`
		Payload  struct {
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
			Body struct {
				Data string `json:"data"`
			} `json:"body"`
			Parts []struct {
				MimeType string `json:"mimeType"`
				Body     struct {
					Data string `json:"data"`
				} `json:"body"`
			} `json:"parts"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", status, err
	}

	// Extract key headers
	subject := ""
	from := ""
	for _, h := range result.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "subject":
			subject = h.Value
		case "from":
			from = h.Value
		}
	}

	// Extract body text (prefer text/plain)
	bodyText := ""
	if result.Payload.Body.Data != "" {
		bodyText = decodeBase64URL(result.Payload.Body.Data)
	}
	for _, part := range result.Payload.Parts {
		if part.MimeType == "text/plain" && part.Body.Data != "" {
			bodyText = decodeBase64URL(part.Body.Data)
			break
		}
	}

	output, _ := json.Marshal(map[string]any{
		"id":           result.ID,
		"thread_id":    result.ThreadID,
		"subject":      subject,
		"from":         from,
		"snippet":      result.Snippet,
		"labels":       result.LabelIDs,
		"body_preview": truncateString(bodyText, 500),
	})
	return string(output), status, nil
}

// gmailMoveToTowelSpam moves messages to Towel/Spam label (creates if needed)
func (a *App) gmailMoveToTowelSpam(accessToken string, arguments map[string]any) (string, int, error) {
	messageIDs := []string{}
	if ids, ok := arguments["message_ids"].([]any); ok {
		for _, id := range ids {
			if s, ok := id.(string); ok {
				messageIDs = append(messageIDs, s)
			}
		}
	}
	if len(messageIDs) == 0 {
		if id, ok := arguments["message_id"].(string); ok && id != "" {
			messageIDs = append(messageIDs, id)
		}
	}
	if len(messageIDs) == 0 {
		return "", 0, errors.New("message_ids or message_id is required")
	}

	// Ensure Towel/Spam label exists
	spamLabelID, status, err := a.ensureLabelExists(accessToken, "Towel/Spam")
	if err != nil {
		return "", status, fmt.Errorf("failed to ensure Towel/Spam label: %w", err)
	}

	// Batch modify to add label and remove from INBOX
	payload := map[string]any{
		"ids":            messageIDs,
		"addLabelIds":    []string{spamLabelID},
		"removeLabelIds": []string{"INBOX"},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequest(http.MethodPost, gmailAPIBase+"/users/me/messages/batchModify", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	if status != http.StatusOK {
		return "", status, fmt.Errorf("Gmail API error (batchModify): %s", string(respBody))
	}

	output, _ := json.Marshal(map[string]any{
		"success":       true,
		"action":        "move_to_towel_spam",
		"message_count": len(messageIDs),
		"label":         "Towel/Spam",
	})
	return string(output), status, nil
}

// gmailMoveToTowelDelete moves messages to Towel/Delete label
func (a *App) gmailMoveToTowelDelete(accessToken string, arguments map[string]any) (string, int, error) {
	messageIDs := []string{}
	if ids, ok := arguments["message_ids"].([]any); ok {
		for _, id := range ids {
			if s, ok := id.(string); ok {
				messageIDs = append(messageIDs, s)
			}
		}
	}
	if len(messageIDs) == 0 {
		if id, ok := arguments["message_id"].(string); ok && id != "" {
			messageIDs = append(messageIDs, id)
		}
	}
	if len(messageIDs) == 0 {
		return "", 0, errors.New("message_ids or message_id is required")
	}

	deleteLabelID, status, err := a.ensureLabelExists(accessToken, "Towel/Delete")
	if err != nil {
		return "", status, fmt.Errorf("failed to ensure Towel/Delete label: %w", err)
	}

	payload := map[string]any{
		"ids":            messageIDs,
		"addLabelIds":    []string{deleteLabelID},
		"removeLabelIds": []string{"INBOX"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequest(http.MethodPost, gmailAPIBase+"/users/me/messages/batchModify", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	if status != http.StatusOK {
		return "", status, fmt.Errorf("Gmail API error (batchModify): %s", string(respBody))
	}

	output, _ := json.Marshal(map[string]any{
		"success":       true,
		"action":        "move_to_towel_delete",
		"message_count": len(messageIDs),
		"label":         "Towel/Delete",
	})
	return string(output), status, nil
}

// gmailCreateFilter creates a Gmail filter with Towel labels
func (a *App) gmailCreateFilter(accessToken string, arguments map[string]any) (string, int, error) {
	criteria := map[string]string{}
	if from, ok := arguments["from"].(string); ok && from != "" {
		criteria["from"] = from
	}
	if to, ok := arguments["to"].(string); ok && to != "" {
		criteria["to"] = to
	}
	if subject, ok := arguments["subject"].(string); ok && subject != "" {
		criteria["subject"] = subject
	}
	if query, ok := arguments["query"].(string); ok && query != "" {
		criteria["query"] = query
	}

	if len(criteria) == 0 {
		return "", 0, errors.New("at least one filter criterion (from, to, subject, query) is required")
	}

	// Determine label to apply
	labelName := "Towel/Organized"
	if target, ok := arguments["label"].(string); ok && target != "" {
		labelName = target
		if !strings.HasPrefix(labelName, "Towel/") {
			labelName = "Towel/" + labelName
		}
	}

	// Ensure label exists
	labelID, status, err := a.ensureLabelExists(accessToken, labelName)
	if err != nil {
		return "", status, fmt.Errorf("failed to ensure label: %w", err)
	}

	action := map[string]any{
		"addLabelIds":    []string{labelID},
		"removeLabelIds": []string{"INBOX"},
	}

	payload := map[string]any{
		"criteria": criteria,
		"action":   action,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequest(http.MethodPost, gmailAPIBase+"/users/me/settings/filters", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	if status != http.StatusOK {
		return "", status, fmt.Errorf("Gmail API error (create_filter): %s", string(respBody))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", status, err
	}

	output, _ := json.Marshal(map[string]any{
		"success":   true,
		"filter_id": result.ID,
		"label":     labelName,
		"criteria":  criteria,
	})
	return string(output), status, nil
}

// gmailSenderAnalytics aggregates sender statistics
func (a *App) gmailSenderAnalytics(accessToken string, arguments map[string]any) (string, int, error) {
	// Default to last 30 days
	days := 30
	if d, ok := arguments["days"].(float64); ok && d > 0 {
		days = int(d)
		if days > 365 {
			days = 365
		}
	}

	// Query for messages in date range
	since := time.Now().AddDate(0, 0, -days).Format("2006/01/02")
	q := "after:" + since

	params := url.Values{}
	params.Set("q", q)
	params.Set("maxResults", "100")

	req, err := http.NewRequest(http.MethodGet, gmailAPIBase+"/users/me/messages?"+params.Encode(), nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	if status != http.StatusOK {
		return "", status, fmt.Errorf("Gmail API error (sender_analytics): %s", string(body))
	}

	var result struct {
		Messages []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", status, err
	}

	// Sample up to 20 messages to analyze senders (to avoid too many API calls)
	sampleSize := 20
	if len(result.Messages) < sampleSize {
		sampleSize = len(result.Messages)
	}

	senderCounts := map[string]int{}
	for i := 0; i < sampleSize; i++ {
		msgID := result.Messages[i].ID
		sender, senderStatus, err := a.getMessageSender(accessToken, msgID)
		if err != nil {
			if senderStatus == http.StatusUnauthorized || senderStatus == http.StatusForbidden {
				return "", senderStatus, err
			}
			continue
		}
		senderCounts[sender]++
	}

	// Convert to sorted list
	type senderStat struct {
		Sender string `json:"sender"`
		Count  int    `json:"count"`
	}
	stats := make([]senderStat, 0, len(senderCounts))
	for sender, count := range senderCounts {
		stats = append(stats, senderStat{Sender: sender, Count: count})
	}

	output, _ := json.Marshal(map[string]any{
		"period_days":    days,
		"total_messages": len(result.Messages),
		"sampled":        sampleSize,
		"sender_stats":   stats,
		"note":           "Sender stats based on sampled messages to respect API quotas",
	})
	return string(output), status, nil
}

// Helper: ensure a label exists, creating if necessary
func (a *App) ensureLabelExists(accessToken string, labelName string) (string, int, error) {
	// First, list labels to find if it exists
	req, err := http.NewRequest(http.MethodGet, gmailAPIBase+"/users/me/labels", nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	if status != http.StatusOK {
		return "", status, fmt.Errorf("list labels failed: %s", string(body))
	}

	var result struct {
		Labels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", status, err
	}

	for _, label := range result.Labels {
		if label.Name == labelName {
			return label.ID, status, nil
		}
	}

	// Create the label
	payload := map[string]any{
		"name":                  labelName,
		"labelListVisibility":   "labelShow",
		"messageListVisibility": "show",
	}
	payloadBytes, _ := json.Marshal(payload)

	req, err = http.NewRequest(http.MethodPost, gmailAPIBase+"/users/me/labels", strings.NewReader(string(payloadBytes)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	respBody, createStatus, err := a.doGmailRequest(req)
	if err != nil {
		return "", createStatus, err
	}
	if createStatus != http.StatusOK {
		return "", createStatus, fmt.Errorf("create label failed: %s", string(respBody))
	}

	var newLabel struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &newLabel); err != nil {
		return "", createStatus, err
	}
	return newLabel.ID, createStatus, nil
}

// Helper: get sender from message headers
func (a *App) getMessageSender(accessToken string, messageID string) (string, int, error) {
	req, err := http.NewRequest(http.MethodGet, gmailAPIBase+"/users/me/messages/"+url.PathEscape(messageID)+"?format=metadata&metadataHeaders=From", nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	if status != http.StatusOK {
		return "", status, fmt.Errorf("API error: %s", string(body))
	}

	var result struct {
		Payload struct {
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", status, err
	}

	for _, h := range result.Payload.Headers {
		if strings.ToLower(h.Name) == "from" {
			return h.Value, status, nil
		}
	}
	return "unknown", status, nil
}

// Helper: decode base64url encoded strings
func decodeBase64URL(s string) string {
	// Replace base64url characters with standard base64
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	// Add padding if needed
	for len(s)%4 != 0 {
		s += "="
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(decoded)
}
