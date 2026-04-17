package main

import (
	"context"
	"net/http"
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
	mux.HandleFunc(a.config.APIPrefix+"/chat", a.requireAuth(a.handleChat))
	mux.HandleFunc(a.config.APIPrefix+"/chat/session", a.requireAuth(a.handleChatSession))
	mux.HandleFunc(a.config.APIPrefix+"/chat/session/stop", a.requireAuth(a.handleChatSessionStop))
	mux.HandleFunc(a.config.APIPrefix+"/chat/stream/state", a.requireAuth(a.handleChatStreamState))
	mux.HandleFunc(a.config.APIPrefix+"/chat/stream", a.requireAuth(a.handleChatStream))
	mux.HandleFunc(a.config.APIPrefix+"/chat/conversations", a.requireAuth(a.handleConversationList))
	mux.HandleFunc(a.config.APIPrefix+"/chat/conversations/", a.requireAuth(a.handleConversationMessages))
	mux.HandleFunc(a.config.APIPrefix+"/history", a.requireAuth(a.handleHistory))
	mux.HandleFunc(a.config.APIPrefix+"/preferences", a.requireAuth(a.handlePreferences))
	mux.HandleFunc(a.config.APIPrefix+"/settings", a.requireAuth(a.handleSettings))
	mux.HandleFunc(a.config.APIPrefix+"/profile/image", a.requireAuth(a.handleProfileImage))
	return a.withCORS(mux)
}

func (a *App) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if allowed := a.allowedOrigin(origin); allowed != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
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
