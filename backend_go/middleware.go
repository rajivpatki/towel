package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const sessionContextKey contextKey = "session"

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleRoot)
	mux.HandleFunc("/health", a.handleHealth)

	// Public setup endpoints (no auth required)
	mux.HandleFunc(a.config.APIPrefix+"/setup/status", a.handleSetupStatus)
	mux.HandleFunc(a.config.APIPrefix+"/setup/google/oauth-credentials", a.handleSaveGoogleCredentials)
	mux.HandleFunc(a.config.APIPrefix+"/setup/google/connect", a.handleGoogleConnect)
	mux.HandleFunc(a.config.APIPrefix+"/setup/google/callback", a.handleGoogleCallback)
	mux.HandleFunc(a.config.APIPrefix+"/setup/llm", a.handleSaveLLMSetup)
	mux.HandleFunc(a.config.APIPrefix+"/setup/llm/gemini", a.handleSaveGeminiSetup)

	// Protected endpoints (require valid session)
	mux.HandleFunc(a.config.APIPrefix+"/tools/gmail", a.requireAuth(a.handleGmailTools))
	mux.HandleFunc(a.config.APIPrefix+"/development/email-sync/status", a.requireAuth(a.handleEmailSyncStatus))
	mux.HandleFunc(a.config.APIPrefix+"/development/email-sync/query", a.requireAuth(a.handleEmailSyncQuery))
	mux.HandleFunc(a.config.APIPrefix+"/development/email-sync/trigger", a.requireAuth(a.handleEmailSyncTrigger))
	mux.HandleFunc(a.config.APIPrefix+"/settings/email-sync", a.requireAuth(a.handleEmailSyncWindowSettings))
	mux.HandleFunc(a.config.APIPrefix+"/settings/google-chat/status", a.requireAuth(a.handleGoogleChatStatus))
	mux.HandleFunc(a.config.APIPrefix+"/settings/google-chat/restart", a.requireAuth(a.handleGoogleChatRestart))
	mux.HandleFunc(a.config.APIPrefix+"/chat", a.requireAuth(a.handleChat))
	mux.HandleFunc(a.config.APIPrefix+"/chat/session", a.requireAuth(a.handleChatSession))
	mux.HandleFunc(a.config.APIPrefix+"/chat/session/stop", a.requireAuth(a.handleChatSessionStop))
	mux.HandleFunc(a.config.APIPrefix+"/chat/stream/state", a.requireAuth(a.handleChatStreamState))
	mux.HandleFunc(a.config.APIPrefix+"/chat/stream", a.requireAuth(a.handleChatStream))
	mux.HandleFunc(a.config.APIPrefix+"/chat/conversations", a.requireAuth(a.handleConversationList))
	mux.HandleFunc(a.config.APIPrefix+"/chat/conversations/", a.requireAuth(a.handleConversationMessages))
	mux.HandleFunc(a.config.APIPrefix+"/history", a.requireAuth(a.handleHistory))
	mux.HandleFunc(a.config.APIPrefix+"/memories", a.requireAuth(a.handleMemories))
	mux.HandleFunc(a.config.APIPrefix+"/memories/", a.requireAuth(a.handleMemoryItem))
	mux.HandleFunc(a.config.APIPrefix+"/scheduled-tasks", a.requireAuth(a.handleScheduledTasks))
	mux.HandleFunc(a.config.APIPrefix+"/scheduled-tasks/", a.requireAuth(a.handleScheduledTaskItem))
	mux.HandleFunc(a.config.APIPrefix+"/settings", a.requireAuth(a.handleSettings))
	mux.HandleFunc(a.config.APIPrefix+"/profile/image", a.requireAuth(a.handleProfileImage))
	return a.withCORS(mux)
}

func (s *AccountServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/health", s.handleHealth)

	mux.HandleFunc(s.config.APIPrefix+"/accounts", s.handleAccounts)
	mux.HandleFunc(s.config.APIPrefix+"/accounts/switch", s.handleAccountSwitch)

	mux.HandleFunc(s.config.APIPrefix+"/setup/status", s.handleSetupStatus)
	mux.HandleFunc(s.config.APIPrefix+"/setup/google/oauth-credentials", s.handleSaveGoogleCredentials)
	mux.HandleFunc(s.config.APIPrefix+"/setup/google/connect", s.withSetupAccount(func(a *App, w http.ResponseWriter, r *http.Request) {
		a.handleGoogleConnect(w, r)
	}))
	mux.HandleFunc(s.config.APIPrefix+"/setup/google/callback", s.withSetupAccount(func(a *App, w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		state := strings.TrimSpace(r.URL.Query().Get("state"))
		if strings.TrimSpace(r.URL.Query().Get("code")) == "" || strings.TrimSpace(r.URL.Query().Get("state")) == "" {
			http.Redirect(w, r, "http://localhost:3000/setup/gmail?oauth=error&msg=OAuth+callback+is+missing+required+query+parameters", http.StatusSeeOther)
			return
		}
		sessionID, err := a.completeGoogleOAuthCallback(code, state)
		if err != nil {
			http.Redirect(w, r, "http://localhost:3000/setup/gmail?oauth=error&msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		setAccountCookie(w, a.config.AccountID)
		if sessionID != "" {
			setSessionCookie(w, sessionID)
		}
		http.Redirect(w, r, frontendSetupURL+"?oauth=success", http.StatusSeeOther)
	}))
	mux.HandleFunc(s.config.APIPrefix+"/setup/llm", s.withSetupAccount(func(a *App, w http.ResponseWriter, r *http.Request) {
		a.handleSaveLLMSetup(w, r)
	}))
	mux.HandleFunc(s.config.APIPrefix+"/setup/llm/gemini", s.withSetupAccount(func(a *App, w http.ResponseWriter, r *http.Request) {
		a.handleSaveGeminiSetup(w, r)
	}))

	mux.HandleFunc(s.config.APIPrefix+"/tools/gmail", s.requireAuth((*App).handleGmailTools))
	mux.HandleFunc(s.config.APIPrefix+"/development/email-sync/status", s.requireAuth((*App).handleEmailSyncStatus))
	mux.HandleFunc(s.config.APIPrefix+"/development/email-sync/query", s.requireAuth((*App).handleEmailSyncQuery))
	mux.HandleFunc(s.config.APIPrefix+"/development/email-sync/trigger", s.requireAuth((*App).handleEmailSyncTrigger))
	mux.HandleFunc(s.config.APIPrefix+"/settings/email-sync", s.requireAuth((*App).handleEmailSyncWindowSettings))
	mux.HandleFunc(s.config.APIPrefix+"/settings/google-chat/status", s.requireAuth((*App).handleGoogleChatStatus))
	mux.HandleFunc(s.config.APIPrefix+"/settings/google-chat/restart", s.requireAuth((*App).handleGoogleChatRestart))
	mux.HandleFunc(s.config.APIPrefix+"/chat", s.requireAuth((*App).handleChat))
	mux.HandleFunc(s.config.APIPrefix+"/chat/session", s.requireAuth((*App).handleChatSession))
	mux.HandleFunc(s.config.APIPrefix+"/chat/session/stop", s.requireAuth((*App).handleChatSessionStop))
	mux.HandleFunc(s.config.APIPrefix+"/chat/stream/state", s.requireAuth((*App).handleChatStreamState))
	mux.HandleFunc(s.config.APIPrefix+"/chat/stream", s.requireAuth((*App).handleChatStream))
	mux.HandleFunc(s.config.APIPrefix+"/chat/conversations", s.requireAuth((*App).handleConversationList))
	mux.HandleFunc(s.config.APIPrefix+"/chat/conversations/", s.requireAuth((*App).handleConversationMessages))
	mux.HandleFunc(s.config.APIPrefix+"/history", s.requireAuth((*App).handleHistory))
	mux.HandleFunc(s.config.APIPrefix+"/memories", s.requireAuth((*App).handleMemories))
	mux.HandleFunc(s.config.APIPrefix+"/memories/", s.requireAuth((*App).handleMemoryItem))
	mux.HandleFunc(s.config.APIPrefix+"/scheduled-tasks", s.requireAuth((*App).handleScheduledTasks))
	mux.HandleFunc(s.config.APIPrefix+"/scheduled-tasks/", s.requireAuth((*App).handleScheduledTaskItem))
	mux.HandleFunc(s.config.APIPrefix+"/settings", s.requireAuth((*App).handleSettings))
	mux.HandleFunc(s.config.APIPrefix+"/profile/image", s.requireAuth((*App).handleProfileImage))
	return s.withCORS(mux)
}

func (a *App) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if allowed := a.allowedOrigin(origin); allowed != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Last-Event-ID")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *AccountServer) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if allowed := s.allowedOrigin(origin); allowed != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Last-Event-ID")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) allowedOrigin(origin string) string {
	// Allow requests with no origin (e.g., Electron file:// protocol, mobile apps)
	if origin == "" || origin == "null" {
		// Check if any allowed origin is "null" (for Electron) or "*" (wildcard)
		for _, allowed := range a.config.CORSOrigins {
			if allowed == "null" || allowed == "*" {
				return "null"
			}
		}
		return ""
	}
	for _, allowed := range a.config.CORSOrigins {
		if origin == allowed {
			return origin
		}
		// Wildcard allows any origin
		if allowed == "*" {
			return origin
		}
	}
	return ""
}

func (s *AccountServer) allowedOrigin(origin string) string {
	if origin == "" || origin == "null" {
		for _, allowed := range s.config.CORSOrigins {
			if allowed == "null" || allowed == "*" {
				return "null"
			}
		}
		return ""
	}
	for _, allowed := range s.config.CORSOrigins {
		if origin == allowed {
			return origin
		}
		if allowed == "*" {
			return origin
		}
	}
	return ""
}

func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract session ID from cookie
		cookie, err := r.Cookie("session_id")
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "Unauthorized: valid session required")
			return
		}
		// Validate session in database
		session, err := a.getValidSession(cookie.Value)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Error validating session")
			return
		}
		if session == nil {
			writeError(w, http.StatusUnauthorized, "Unauthorized: session expired or invalid")
			return
		}
		// Store session in request context for downstream handlers
		ctx := context.WithValue(r.Context(), sessionContextKey, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

func (s *AccountServer) withSetupAccount(next func(*App, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app, accountID := s.selectedAccount(r)
		if app == nil {
			writeError(w, http.StatusBadRequest, "Create an account by saving Google OAuth credentials with account_email first")
			return
		}
		setAccountCookie(w, accountID)
		next(app, w, r)
	}
}

func (s *AccountServer) requireAuth(next func(*App, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app, _ := s.selectedAccount(r)
		if app == nil {
			writeError(w, http.StatusUnauthorized, "Unauthorized: account setup required")
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "Unauthorized: valid session required")
			return
		}
		session, err := app.getValidSession(cookie.Value)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Error validating session")
			return
		}
		if session == nil {
			writeError(w, http.StatusUnauthorized, "Unauthorized: session expired or invalid")
			return
		}
		ctx := context.WithValue(r.Context(), sessionContextKey, session)
		next(app, w, r.WithContext(ctx))
	}
}
