package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type googleUserProfile struct {
	Email   string
	Name    string
	Picture string
}

func (a *App) buildGoogleAuthURL() (string, error) {
	clientID, err := a.getSecret("google_client_id")
	if err != nil {
		return "", err
	}
	clientSecret, err := a.getSecret("google_client_secret")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return "", errors.New("Google OAuth client credentials are not configured yet.")
	}
	stateToken, err := randomToken(32)
	if err != nil {
		return "", err
	}
	codeVerifier, err := randomToken(72)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if err := a.upsertSecret("google_oauth_state", stateToken); err != nil {
		return "", err
	}
	if err := a.upsertSecret("google_oauth_code_verifier", codeVerifier); err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("client_id", clientID)
	query.Set("redirect_uri", a.buildRedirectURI())
	query.Set("response_type", "code")
	query.Set("scope", strings.Join([]string{
		"openid",
		"email",
		"https://www.googleapis.com/auth/gmail.modify",
		"https://www.googleapis.com/auth/gmail.labels",
		"https://www.googleapis.com/auth/gmail.settings.basic",
	}, " "))
	query.Set("access_type", "offline")
	query.Set("prompt", "consent")
	query.Set("state", stateToken)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	return googleAuthEndpoint + "?" + query.Encode(), nil
}

func (a *App) completeGoogleOAuthCallback(code string, stateValue string) error {
	savedState, err := a.getSecret("google_oauth_state")
	if err != nil {
		return err
	}
	codeVerifier, err := a.getSecret("google_oauth_code_verifier")
	if err != nil {
		return err
	}
	clientID, err := a.getSecret("google_client_id")
	if err != nil {
		return err
	}
	clientSecret, err := a.getSecret("google_client_secret")
	if err != nil {
		return err
	}
	if savedState == "" || codeVerifier == "" || clientID == "" || clientSecret == "" {
		return errors.New("OAuth setup is incomplete. Save Google client credentials first.")
	}
	if savedState != stateValue {
		return errors.New("OAuth state validation failed.")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	form.Set("code_verifier", codeVerifier)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", a.buildRedirectURI())
	req, err := http.NewRequest(http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Google token exchange failed: %s", strings.TrimSpace(string(body)))
	}
	var tokenPayload map[string]any
	if err := json.Unmarshal(body, &tokenPayload); err != nil {
		return err
	}
	accessToken, _ := tokenPayload["access_token"].(string)
	tokenJSON, err := json.Marshal(tokenPayload)
	if err != nil {
		return err
	}
	if err := a.upsertSecret("google_token_bundle", string(tokenJSON)); err != nil {
		return err
	}
	if _, err := a.db.Exec(`UPDATE setup_state SET google_account_connected = 1, updated_at = CURRENT_TIMESTAMP WHERE id = 1`); err != nil {
		return err
	}
	if strings.TrimSpace(accessToken) != "" {
		go a.refreshGoogleProfile(accessToken)
	}
	_, _, err = a.refreshOnboardingState()
	return err
}

func (a *App) refreshGoogleProfile(accessToken string) {
	profile, err := a.fetchGoogleUserProfile(accessToken)
	if err != nil {
		return
	}
	_ = a.saveGoogleProfile(profile.Email, profile.Name, profile.Picture)
}

func (a *App) fetchGoogleUserProfile(accessToken string) (googleUserProfile, error) {
	userinfoReq, err := http.NewRequest(http.MethodGet, googleUserinfoEndpoint, nil)
	if err != nil {
		return googleUserProfile{}, err
	}
	userinfoReq.Header.Set("Authorization", "Bearer "+accessToken)
	userinfoResp, err := a.httpClient.Do(userinfoReq)
	if err != nil {
		return googleUserProfile{}, err
	}
	defer userinfoResp.Body.Close()
	userinfoBody, err := io.ReadAll(userinfoResp.Body)
	if err != nil {
		return googleUserProfile{}, err
	}
	if userinfoResp.StatusCode >= 400 {
		return googleUserProfile{}, fmt.Errorf("Google userinfo request failed: %s", strings.TrimSpace(string(userinfoBody)))
	}
	var userinfoPayload map[string]any
	if err := json.Unmarshal(userinfoBody, &userinfoPayload); err != nil {
		return googleUserProfile{}, err
	}
	profile := googleUserProfile{}
	if email, ok := userinfoPayload["email"].(string); ok {
		profile.Email = strings.TrimSpace(email)
	}
	if name, ok := userinfoPayload["name"].(string); ok {
		profile.Name = strings.TrimSpace(name)
	}
	if picture, ok := userinfoPayload["picture"].(string); ok {
		profile.Picture = strings.TrimSpace(picture)
	}
	return profile, nil
}

func (a *App) buildRedirectURI() string {
	return strings.TrimRight(a.config.PublicAPIBaseURL, "/") + a.config.APIPrefix + "/setup/google/callback"
}
