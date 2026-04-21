package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const gmailAPIBase = "https://www.googleapis.com/gmail/v1"
const googleTokenEndpoint = "https://oauth2.googleapis.com/token"
const gmailTokenRefreshLeadTime = 5 * time.Minute

type GmailToolFunc func(accessToken string, arguments map[string]any) (string, int, error)

// TokenBundle represents the stored Google OAuth token response
type TokenBundle struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	ExpiresIn     int    `json:"expires_in,omitempty"`
	TokenType     string `json:"token_type,omitempty"`
	ExpiresAtUnix int64  `json:"expires_at_unix,omitempty"`
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
	if strings.TrimSpace(bundle.AccessToken) == "" && strings.TrimSpace(bundle.RefreshToken) == "" {
		return "", errors.New("access token not available")
	}
	if gmailTokenShouldRefresh(bundle) {
		refreshedToken, refreshErr := a.refreshGmailToken()
		if refreshErr == nil {
			return refreshedToken, nil
		}
		if strings.TrimSpace(bundle.AccessToken) == "" {
			return "", fmt.Errorf("access token refresh failed: %w", refreshErr)
		}
		if bundle.ExpiresAtUnix > 0 && time.Now().Unix() >= bundle.ExpiresAtUnix {
			return "", fmt.Errorf("access token expired and refresh failed: %w", refreshErr)
		}
	}
	if strings.TrimSpace(bundle.AccessToken) == "" {
		return "", errors.New("access token not available")
	}
	return bundle.AccessToken, nil
}

func gmailTokenExpiryUnix(expiresIn int) int64 {
	if expiresIn <= 0 {
		return 0
	}
	return time.Now().UTC().Add(time.Duration(expiresIn) * time.Second).Unix()
}

func gmailTokenShouldRefresh(bundle TokenBundle) bool {
	if strings.TrimSpace(bundle.RefreshToken) == "" {
		return false
	}
	if strings.TrimSpace(bundle.AccessToken) == "" {
		return true
	}
	if bundle.ExpiresAtUnix <= 0 {
		return true
	}
	return time.Now().Add(gmailTokenRefreshLeadTime).Unix() >= bundle.ExpiresAtUnix
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
	if strings.TrimSpace(refreshResp.AccessToken) == "" {
		return "", errors.New("token refresh response did not include an access token")
	}

	// Update stored bundle with new access token
	bundle.AccessToken = refreshResp.AccessToken
	if refreshResp.ExpiresIn > 0 {
		bundle.ExpiresIn = refreshResp.ExpiresIn
		bundle.ExpiresAtUnix = gmailTokenExpiryUnix(refreshResp.ExpiresIn)
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

func (a *App) gmailToolMapping() map[string]GmailToolFunc {
	return map[string]GmailToolFunc{
		"users.labels.list":             a.gmailUsersLabelsList,
		"users.labels.create":           a.gmailUsersLabelsCreate,
		"users.drafts.create":           a.gmailUsersDraftsCreate,
		"users.messages.list":           a.gmailUsersMessagesList,
		"users.messages.get":            a.gmailUsersMessagesGet,
		"users.messages.batchModify":    a.gmailUsersMessagesBatchModify,
		"users.messages.modify":         a.gmailUsersMessagesModify,
		"users.settings.filters.list":   a.gmailUsersSettingsFiltersList,
		"users.settings.filters.get":    a.gmailUsersSettingsFiltersGet,
		"users.settings.filters.create": a.gmailUsersSettingsFiltersCreate,
		"users.settings.filters.delete": a.gmailUsersSettingsFiltersDelete,
		"users.threads.list":            a.gmailUsersThreadsList,
		"users.threads.get":             a.gmailUsersThreadsGet,
	}
}

func (a *App) tryExecuteGmailTool(toolName, accessToken string, arguments map[string]any) (string, int, error) {
	toolFunc, ok := a.gmailToolMapping()[toolName]
	if !ok {
		return "", 0, fmt.Errorf("unknown Gmail tool: %s", toolName)
	}
	return toolFunc(accessToken, arguments)
}

func gmailUserID(arguments map[string]any) string {
	if userID, ok := arguments["userId"].(string); ok && strings.TrimSpace(userID) != "" {
		return userID
	}
	return "me"
}

func gmailBodyArguments(arguments map[string]any, excludedKeys ...string) map[string]any {
	excluded := make(map[string]struct{}, len(excludedKeys))
	for _, key := range excludedKeys {
		excluded[key] = struct{}{}
	}
	payload := make(map[string]any)
	for key, value := range arguments {
		if _, skip := excluded[key]; skip {
			continue
		}
		payload[key] = value
	}
	return payload
}

func addGmailQueryValue(params url.Values, key string, value any) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			params.Add(key, typed)
		}
	case float64:
		params.Add(key, strconv.FormatFloat(typed, 'f', -1, 64))
	case bool:
		params.Add(key, strconv.FormatBool(typed))
	case []string:
		for _, item := range typed {
			if strings.TrimSpace(item) != "" {
				params.Add(key, item)
			}
		}
	case []any:
		for _, item := range typed {
			addGmailQueryValue(params, key, item)
		}
	case int:
		params.Add(key, strconv.Itoa(typed))
	case int32:
		params.Add(key, strconv.FormatInt(int64(typed), 10))
	case int64:
		params.Add(key, strconv.FormatInt(typed, 10))
	}
}

func gmailQueryArguments(arguments map[string]any, excludedKeys ...string) url.Values {
	excluded := make(map[string]struct{}, len(excludedKeys))
	for _, key := range excludedKeys {
		excluded[key] = struct{}{}
	}
	params := url.Values{}
	for key, value := range arguments {
		if _, skip := excluded[key]; skip {
			continue
		}
		addGmailQueryValue(params, key, value)
	}
	return params
}

func gmailOptionalStringArgument(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return value
}

func gmailStringListArgument(arguments map[string]any, key string) []string {
	value, ok := arguments[key]
	if !ok || value == nil {
		return nil
	}

	items := make([]string, 0)
	appendValue := func(raw string) {
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}

	switch typed := value.(type) {
	case string:
		appendValue(typed)
	case []string:
		for _, item := range typed {
			appendValue(item)
		}
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				appendValue(text)
			}
		}
	}

	return items
}

func sanitizeEmailHeaderValue(value string) string {
	sanitized := strings.ReplaceAll(value, "\r", " ")
	sanitized = strings.ReplaceAll(sanitized, "\n", " ")
	return strings.TrimSpace(sanitized)
}

func joinDraftHeaderValues(items []string, separator string) string {
	cleaned := make([]string, 0, len(items))
	for _, item := range items {
		value := sanitizeEmailHeaderValue(item)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return strings.Join(cleaned, separator)
}

func quotedPrintableEncode(value string) string {
	var buffer bytes.Buffer
	writer := quotedprintable.NewWriter(&buffer)
	_, _ = writer.Write([]byte(value))
	_ = writer.Close()
	return buffer.String()
}

func buildGmailDraftRaw(arguments map[string]any) (string, string, error) {
	to := gmailStringListArgument(arguments, "to")
	cc := gmailStringListArgument(arguments, "cc")
	bcc := gmailStringListArgument(arguments, "bcc")
	references := gmailStringListArgument(arguments, "references")

	subject := sanitizeEmailHeaderValue(gmailOptionalStringArgument(arguments, "subject"))
	bodyText := gmailOptionalStringArgument(arguments, "bodyText")
	bodyHTML := gmailOptionalStringArgument(arguments, "bodyHtml")
	inReplyTo := sanitizeEmailHeaderValue(gmailOptionalStringArgument(arguments, "inReplyTo"))
	threadID := sanitizeEmailHeaderValue(gmailOptionalStringArgument(arguments, "threadId"))

	if len(to) == 0 {
		return "", "", errors.New("to is required")
	}
	if subject == "" {
		return "", "", errors.New("subject is required")
	}
	if strings.TrimSpace(bodyText) == "" && strings.TrimSpace(bodyHTML) == "" {
		return "", "", errors.New("bodyText or bodyHtml is required")
	}

	var message bytes.Buffer
	writeLine := func(line string) {
		message.WriteString(line)
		message.WriteString("\r\n")
	}

	writeLine("To: " + joinDraftHeaderValues(to, ", "))
	if len(cc) > 0 {
		writeLine("Cc: " + joinDraftHeaderValues(cc, ", "))
	}
	if len(bcc) > 0 {
		writeLine("Bcc: " + joinDraftHeaderValues(bcc, ", "))
	}
	writeLine("Subject: " + mime.QEncoding.Encode("utf-8", subject))
	if inReplyTo != "" {
		writeLine("In-Reply-To: " + inReplyTo)
	}
	if len(references) > 0 {
		writeLine("References: " + joinDraftHeaderValues(references, " "))
	}
	writeLine("MIME-Version: 1.0")

	switch {
	case strings.TrimSpace(bodyText) != "" && strings.TrimSpace(bodyHTML) != "":
		boundary := fmt.Sprintf("towel_%d", time.Now().UnixNano())
		writeLine(`Content-Type: multipart/alternative; boundary="` + boundary + `"`)
		writeLine("")
		writeLine("--" + boundary)
		writeLine(`Content-Type: text/plain; charset="UTF-8"`)
		writeLine("Content-Transfer-Encoding: quoted-printable")
		writeLine("")
		writeLine(quotedPrintableEncode(bodyText))
		writeLine("--" + boundary)
		writeLine(`Content-Type: text/html; charset="UTF-8"`)
		writeLine("Content-Transfer-Encoding: quoted-printable")
		writeLine("")
		writeLine(quotedPrintableEncode(bodyHTML))
		writeLine("--" + boundary + "--")
	case strings.TrimSpace(bodyHTML) != "":
		writeLine(`Content-Type: text/html; charset="UTF-8"`)
		writeLine("Content-Transfer-Encoding: quoted-printable")
		writeLine("")
		writeLine(quotedPrintableEncode(bodyHTML))
	default:
		writeLine(`Content-Type: text/plain; charset="UTF-8"`)
		writeLine("Content-Transfer-Encoding: quoted-printable")
		writeLine("")
		writeLine(quotedPrintableEncode(bodyText))
	}

	return base64.RawURLEncoding.EncodeToString(message.Bytes()), threadID, nil
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

// gmailUsersLabelsList lists all labels in the user's mailbox.
func (a *App) gmailUsersLabelsList(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	req, err := http.NewRequest(http.MethodGet, gmailAPIBase+"/users/"+url.PathEscape(userID)+"/labels", nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(body), status, nil
}

// gmailUsersLabelsCreate creates a new label.
func (a *App) gmailUsersLabelsCreate(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	body, err := json.Marshal(gmailBodyArguments(arguments, "userId"))
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequest(http.MethodPost, gmailAPIBase+"/users/"+url.PathEscape(userID)+"/labels", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(respBody), status, nil
}

// gmailUsersDraftsCreate creates an unsent Gmail draft message.
func (a *App) gmailUsersDraftsCreate(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	raw, threadID, err := buildGmailDraftRaw(arguments)
	if err != nil {
		return "", 0, err
	}

	messagePayload := map[string]any{
		"raw": raw,
	}
	if threadID != "" {
		messagePayload["threadId"] = threadID
	}

	body, err := json.Marshal(map[string]any{
		"message": messagePayload,
	})
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequest(http.MethodPost, gmailAPIBase+"/users/"+url.PathEscape(userID)+"/drafts", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(respBody), status, nil
}

// gmailUsersMessagesList lists the messages in the user's mailbox.
func (a *App) gmailUsersMessagesList(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	params := gmailQueryArguments(arguments, "userId")
	urlStr := gmailAPIBase + "/users/" + url.PathEscape(userID) + "/messages"
	if encoded := params.Encode(); encoded != "" {
		urlStr += "?" + encoded
	}

	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(body), status, nil
}

// gmailUsersMessagesGet gets the specified message.
func (a *App) gmailUsersMessagesGet(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	id, ok := arguments["id"].(string)
	if !ok || id == "" {
		return "", 0, errors.New("id is required")
	}

	params := gmailQueryArguments(arguments, "userId", "id")
	urlStr := gmailAPIBase + "/users/" + url.PathEscape(userID) + "/messages/" + url.PathEscape(id)
	if len(params) > 0 {
		urlStr += "?" + params.Encode()
	}

	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(body), status, nil
}

// gmailUsersMessagesBatchModify modifies the labels on the specified messages.
func (a *App) gmailUsersMessagesBatchModify(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	body, err := json.Marshal(gmailBodyArguments(arguments, "userId"))
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequest(http.MethodPost, gmailAPIBase+"/users/"+url.PathEscape(userID)+"/messages/batchModify", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(respBody), status, nil
}

// gmailUsersMessagesModify modifies the labels on the specified message.
func (a *App) gmailUsersMessagesModify(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	id, ok := arguments["id"].(string)
	if !ok || id == "" {
		return "", 0, errors.New("id is required")
	}

	payload := gmailBodyArguments(arguments, "userId", "id")
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequest(http.MethodPost, gmailAPIBase+"/users/"+url.PathEscape(userID)+"/messages/"+url.PathEscape(id)+"/modify", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(respBody), status, nil
}

// gmailUsersSettingsFiltersCreate creates a new filter.
func (a *App) gmailUsersSettingsFiltersCreate(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	body, err := json.Marshal(gmailBodyArguments(arguments, "userId"))
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequest(http.MethodPost, gmailAPIBase+"/users/"+url.PathEscape(userID)+"/settings/filters", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(respBody), status, nil
}

func (a *App) gmailUsersSettingsFiltersList(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	params := gmailQueryArguments(arguments, "userId")
	urlStr := gmailAPIBase + "/users/" + url.PathEscape(userID) + "/settings/filters"
	if encoded := params.Encode(); encoded != "" {
		urlStr += "?" + encoded
	}

	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(body), status, nil
}

func (a *App) gmailUsersSettingsFiltersGet(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	id, ok := arguments["id"].(string)
	if !ok || id == "" {
		return "", 0, errors.New("id is required")
	}

	params := gmailQueryArguments(arguments, "userId", "id")
	urlStr := gmailAPIBase + "/users/" + url.PathEscape(userID) + "/settings/filters/" + url.PathEscape(id)
	if encoded := params.Encode(); encoded != "" {
		urlStr += "?" + encoded
	}

	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(body), status, nil
}

func (a *App) gmailUsersSettingsFiltersDelete(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	id, ok := arguments["id"].(string)
	if !ok || id == "" {
		return "", 0, errors.New("id is required")
	}

	req, err := http.NewRequest(http.MethodDelete, gmailAPIBase+"/users/"+url.PathEscape(userID)+"/settings/filters/"+url.PathEscape(id), nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(body), status, nil
}

// gmailUsersThreadsList lists the threads in the user's mailbox.
func (a *App) gmailUsersThreadsList(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	params := gmailQueryArguments(arguments, "userId")
	urlStr := gmailAPIBase + "/users/" + url.PathEscape(userID) + "/threads"
	if encoded := params.Encode(); encoded != "" {
		urlStr += "?" + encoded
	}

	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(body), status, nil
}

// gmailUsersThreadsGet gets the specified thread.
func (a *App) gmailUsersThreadsGet(accessToken string, arguments map[string]any) (string, int, error) {
	userID := gmailUserID(arguments)
	id, ok := arguments["id"].(string)
	if !ok || id == "" {
		return "", 0, errors.New("id is required")
	}

	params := gmailQueryArguments(arguments, "userId", "id")
	urlStr := gmailAPIBase + "/users/" + url.PathEscape(userID) + "/threads/" + url.PathEscape(id)
	if len(params) > 0 {
		urlStr += "?" + params.Encode()
	}

	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	body, status, err := a.doGmailRequest(req)
	if err != nil {
		return "", status, err
	}
	return string(body), status, nil
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
