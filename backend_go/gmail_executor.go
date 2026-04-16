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
const gmailTokenRefreshLeadTime = 5 * time.Minute

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

func (a *App) tryExecuteGmailTool(toolName, accessToken string, arguments map[string]any) (string, int, error) {
	switch toolName {
	case "users.labels.list":
		return a.gmailUsersLabelsList(accessToken, arguments)
	case "users.labels.create":
		return a.gmailUsersLabelsCreate(accessToken, arguments)
	case "users.messages.list":
		return a.gmailUsersMessagesList(accessToken, arguments)
	case "users.messages.get":
		return a.gmailUsersMessagesGet(accessToken, arguments)
	case "users.messages.batchModify":
		return a.gmailUsersMessagesBatchModify(accessToken, arguments)
	case "users.messages.modify":
		return a.gmailUsersMessagesModify(accessToken, arguments)
	case "users.settings.filters.create":
		return a.gmailUsersSettingsFiltersCreate(accessToken, arguments)
	case "users.threads.list":
		return a.gmailUsersThreadsList(accessToken, arguments)
	case "users.threads.get":
		return a.gmailUsersThreadsGet(accessToken, arguments)
	default:
		return "", 0, fmt.Errorf("unknown Gmail tool: %s", toolName)
	}
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
