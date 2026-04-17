package main

import (
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": a.config.AppName, "status": "ok"})
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

func (a *App) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	status, err := a.buildSetupStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *App) handleSaveGoogleCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload GoogleOAuthCredentialsIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload.ClientID = strings.TrimSpace(payload.ClientID)
	payload.ClientSecret = strings.TrimSpace(payload.ClientSecret)
	if len(payload.ClientID) < 10 || len(payload.ClientSecret) < 10 {
		writeError(w, http.StatusBadRequest, "client_id and client_secret must each be at least 10 characters")
		return
	}
	if err := a.saveGoogleClientCredentials(payload.ClientID, payload.ClientSecret); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

func (a *App) handleGoogleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	authURL, err := a.buildGoogleAuthURL()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, GoogleAuthURLResponse{AuthURL: authURL})
}

func (a *App) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if code == "" || state == "" {
		http.Redirect(w, r, "http://localhost:3000/setup/gmail?oauth=error&msg=OAuth+callback+is+missing+required+query+parameters", http.StatusSeeOther)
		return
	}
	sessionID, err := a.completeGoogleOAuthCallback(code, state)
	if err != nil {
		http.Redirect(w, r, "http://localhost:3000/setup/gmail?oauth=error&msg="+html.EscapeString(err.Error()), http.StatusSeeOther)
		return
	}
	// Set session cookie for API authentication
	if sessionID != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "session_id",
			Value:    sessionID,
			Path:     "/",
			HttpOnly: true,
			Secure:   false,                // Set to true if using HTTPS
			SameSite: http.SameSiteLaxMode, // Changed from NoneMode to LaxMode
			MaxAge:   7 * 24 * 60 * 60,     // 7 days
		})
	}
	http.Redirect(w, r, frontendSetupURL+"?oauth=success", http.StatusSeeOther)
}

func (a *App) handleSaveLLMSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload LLMSetupIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload.AgentID = strings.TrimSpace(payload.AgentID)
	payload.APIKey = strings.TrimSpace(payload.APIKey)
	if payload.AgentID == "" || len(payload.APIKey) < 10 {
		writeError(w, http.StatusBadRequest, "agent_id is required and api_key must be at least 10 characters")
		return
	}
	if err := a.saveLLMCredentials(payload.AgentID, payload.APIKey); err != nil {
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "Unsupported agent") {
			statusCode = http.StatusBadRequest
		}
		writeError(w, statusCode, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

func (a *App) handleSaveGeminiSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload GeminiSetupIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload.AgentID = strings.TrimSpace(payload.AgentID)
	if payload.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	log.Printf("gemini setup request received: agent=%s", payload.AgentID)
	agent, ok := getAgentDefinition(payload.AgentID)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Unsupported agent: %s", payload.AgentID))
		return
	}
	if !agentUsesGoogleOAuth(agent) {
		writeError(w, http.StatusBadRequest, "Selected agent does not use Google OAuth")
		return
	}
	probe, err := a.probeGeminiAccess(agent)
	if err != nil {
		log.Printf("gemini setup probe error: agent=%s err=%v", payload.AgentID, err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if probe.Status != "ready" {
		log.Printf("gemini setup probe not ready: agent=%s status=%s detail=%s", payload.AgentID, probe.Status, probe.Detail)
		writeJSON(w, http.StatusOK, probe)
		return
	}
	if err := a.saveGeminiSelection(agent.AgentID); err != nil {
		log.Printf("gemini setup save selection failed: agent=%s err=%v", payload.AgentID, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("gemini setup completed: agent=%s", payload.AgentID)
	probe.Success = true
	writeJSON(w, http.StatusOK, probe)
}

func (a *App) handleGmailTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, gmailToolDefinitions)
}

func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload ChatMessageIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload.Message = strings.TrimSpace(payload.Message)
	if payload.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	conversationID, err := a.resolveConversationID(payload.ConversationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	response, err := a.processChatMessage(r.Context(), conversationID, payload.Message, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleChatSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload ChatMessageIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload.Message = strings.TrimSpace(payload.Message)
	if payload.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	conversationID, err := a.resolveConversationID(payload.ConversationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := a.startStreamSession(conversationID, cancel); err != nil {
		cancel()
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	go func() {
		a.waitForStreamSubscriber(ctx, conversationID, 2*time.Second)
		emitProgress := func(content string, actions []string) {
			_ = a.emitStreamProgress(conversationID, ChatMessageOut{
				ConversationID: conversationID,
				Response:       content,
				Actions:        actions,
			})
		}
		response, processErr := a.processChatMessage(ctx, conversationID, payload.Message, emitProgress)
		if processErr != nil {
			if ctx.Err() == context.Canceled || processErr == context.Canceled {
				return
			}
			_ = a.emitStreamError(conversationID, processErr.Error())
			return
		}
		_ = a.emitStreamDone(conversationID, response)
	}()
	writeJSON(w, http.StatusOK, ChatSessionStartOut{
		ConversationID: conversationID,
		SessionID:      conversationID,
	})
}

func (a *App) handleChatSessionStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload ChatSessionStopIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload.SessionID = strings.TrimSpace(payload.SessionID)
	if payload.SessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if !isValidConversationID(payload.SessionID) {
		writeError(w, http.StatusBadRequest, "session_id is invalid")
		return
	}
	if !a.stopStreamSession(payload.SessionID) {
		writeError(w, http.StatusNotFound, "stream session is not active")
		return
	}
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

func (a *App) handleChatStreamState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if !isValidConversationID(sessionID) {
		writeError(w, http.StatusBadRequest, "session_id is invalid")
		return
	}

	state, ok := a.getStreamSessionState(sessionID)
	if !ok {
		writeError(w, http.StatusNotFound, "stream session not found")
		return
	}

	writeJSON(w, http.StatusOK, state)
}

func (a *App) handleChatStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if !isValidConversationID(sessionID) {
		writeError(w, http.StatusBadRequest, "session_id is invalid")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	backlog, updates, unsubscribe, completed, err := a.subscribeStreamSession(sessionID, parseLastEventID(r))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	for _, event := range backlog {
		if err := writeSSEEvent(w, event); err != nil {
			return
		}
		flusher.Flush()
	}

	if completed {
		return
	}

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-updates:
			if !ok {
				return
			}
			if err := writeSSEEvent(w, event); err != nil {
				return
			}
			flusher.Flush()
			if event.Event == "done" || event.Event == "failed" || event.Event == "stopped" {
				return
			}
		case <-keepAlive.C:
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (a *App) handleConversationList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	page := 1
	if rawPage := strings.TrimSpace(r.URL.Query().Get("page")); rawPage != "" {
		parsedPage, err := strconv.Atoi(rawPage)
		if err != nil || parsedPage < 1 {
			writeError(w, http.StatusBadRequest, "page must be a positive integer")
			return
		}
		page = parsedPage
	}
	pageSize := 15
	if rawPageSize := strings.TrimSpace(r.URL.Query().Get("page_size")); rawPageSize != "" {
		parsedPageSize, err := strconv.Atoi(rawPageSize)
		if err != nil || parsedPageSize < 1 || parsedPageSize > 100 {
			writeError(w, http.StatusBadRequest, "page_size must be between 1 and 100")
			return
		}
		pageSize = parsedPageSize
	}
	offset := (page - 1) * pageSize
	items, err := a.getConversationSummaries(pageSize+1, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hasMore := len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	writeJSON(w, http.StatusOK, ConversationListOut{
		Items:    items,
		Page:     page,
		PageSize: pageSize,
		HasMore:  hasMore,
	})
}

func (a *App) handleConversationMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	prefix := a.config.APIPrefix + "/chat/conversations/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	conversationID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, prefix))
	if conversationID == "" {
		writeError(w, http.StatusBadRequest, "conversation_id is required")
		return
	}
	if !isValidConversationID(conversationID) {
		writeError(w, http.StatusBadRequest, "conversation_id is invalid")
		return
	}
	exists, err := a.conversationExists(conversationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "conversation not found")
		return
	}
	if r.Method == http.MethodDelete {
		if err := a.deleteConversation(conversationID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
		return
	}
	messages, err := a.getConversationMessages(conversationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ConversationMessagesOut{
		ConversationID: conversationID,
		Messages:       messages,
	})
}

func (a *App) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	items, err := a.getActionHistory(100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, HistoryListOut{Items: items})
}

func (a *App) handlePreferences(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		preferences, err := a.getAllPreferences()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, PreferencesOut{Preferences: preferences})
	case http.MethodPost:
		var payload PreferencesIn
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.savePreferences(payload.Preferences); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save preferences: %v", err))
			return
		}
		writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := a.getSetupState()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		hasAPIKey, err := a.hasSecret("llm_api_key")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		customAgents, err := a.getCustomAgents()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		agents := make([]SettingsAgent, 0, len(agentDefinitions)+len(customAgents))
		for _, agent := range agentDefinitions {
			agents = append(agents, SettingsAgent{
				AgentID:       agent.AgentID,
				Provider:      agent.Provider,
				AuthMode:      normalizeAgentAuthMode(agent.AuthMode, agent.Provider),
				Label:         agent.Label,
				Model:         agent.Model,
				ReasoningMode: agent.ReasoningMode,
				Verbosity:     agent.Verbosity,
				BaseURL:       agent.BaseURL,
				IsCustom:      false,
			})
		}
		for _, agent := range customAgents {
			agents = append(agents, SettingsAgent{
				AgentID:       agent.AgentID,
				Provider:      agent.Provider,
				AuthMode:      normalizeAgentAuthMode(agent.AuthMode, agent.Provider),
				Label:         agent.Label,
				Model:         agent.Model,
				ReasoningMode: agent.ReasoningMode,
				Verbosity:     agent.Verbosity,
				BaseURL:       agent.BaseURL,
				IsCustom:      true,
			})
		}
		writeJSON(w, http.StatusOK, SettingsOut{
			SelectedAgentID: state.SelectedAgentID,
			HasAPIKey:       hasAPIKey,
			Agents:          agents,
		})
	case http.MethodPost:
		var payload SettingsIn
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.saveSettings(payload.SelectedAgentID, payload.APIKey, payload.Agents); err != nil {
			statusCode := http.StatusInternalServerError
			if strings.Contains(err.Error(), "Unsupported agent") {
				statusCode = http.StatusBadRequest
			}
			writeError(w, statusCode, fmt.Sprintf("Failed to save settings: %v", err))
			return
		}
		writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (a *App) buildSetupStatus() (SetupStatus, error) {
	state, llmConfigured, err := a.refreshOnboardingState()
	if err != nil {
		return SetupStatus{}, err
	}
	customAgents, err := a.getCustomAgents()
	if err != nil {
		return SetupStatus{}, err
	}
	availableAgents := make([]AgentDefinition, 0, len(agentDefinitions)+len(customAgents))
	availableAgents = append(availableAgents, agentDefinitions...)
	availableAgents = append(availableAgents, customAgents...)
	return SetupStatus{
		GoogleClientConfigured: state.GoogleClientConfigured,
		GoogleAccountConnected: state.GoogleAccountConnected,
		GoogleEmail:            state.GoogleEmail,
		GoogleName:             state.GoogleName,
		GooglePicture:          state.GooglePicture,
		LLMConfigured:          llmConfigured,
		SelectedAgentID:        state.SelectedAgentID,
		OnboardingCompleted:    state.OnboardingCompleted,
		AvailableAgents:        availableAgents,
		GmailTools:             gmailToolDefinitions,
	}, nil
}

func (a *App) handleProfileImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	state, err := a.getSetupState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to retrieve profile")
		return
	}
	if state.GooglePicture == nil || *state.GooglePicture == "" {
		writeError(w, http.StatusNotFound, "No profile picture available")
		return
	}

	imageURL := *state.GooglePicture
	parsedURL, err := url.Parse(imageURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		writeError(w, http.StatusBadRequest, "Invalid profile picture URL")
		return
	}

	req, err := http.NewRequest(http.MethodGet, imageURL, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to create request")
		return
	}
	req.Header.Set("User-Agent", "Towel/1.0")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Failed to fetch image")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		writeError(w, resp.StatusCode, "Failed to fetch image from source")
		return
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "public, max-age=21600")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if _, err := io.Copy(w, resp.Body); err != nil {
		return
	}
}
