package main

import (
	"net/http"
	"strings"
)

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleRoot)
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc(a.config.APIPrefix+"/setup/status", a.handleSetupStatus)
	mux.HandleFunc(a.config.APIPrefix+"/setup/google/oauth-credentials", a.handleSaveGoogleCredentials)
	mux.HandleFunc(a.config.APIPrefix+"/setup/google/connect", a.handleGoogleConnect)
	mux.HandleFunc(a.config.APIPrefix+"/setup/google/callback", a.handleGoogleCallback)
	mux.HandleFunc(a.config.APIPrefix+"/setup/llm", a.handleSaveLLMSetup)
	mux.HandleFunc(a.config.APIPrefix+"/tools/gmail", a.handleGmailTools)
	mux.HandleFunc(a.config.APIPrefix+"/chat", a.handleChat)
	mux.HandleFunc(a.config.APIPrefix+"/chat/session", a.handleChatSession)
	mux.HandleFunc(a.config.APIPrefix+"/chat/stream/state", a.handleChatStreamState)
	mux.HandleFunc(a.config.APIPrefix+"/chat/stream", a.handleChatStream)
	mux.HandleFunc(a.config.APIPrefix+"/chat/conversations", a.handleConversationList)
	mux.HandleFunc(a.config.APIPrefix+"/chat/conversations/", a.handleConversationMessages)
	mux.HandleFunc(a.config.APIPrefix+"/history", a.handleHistory)
	mux.HandleFunc(a.config.APIPrefix+"/preferences", a.handlePreferences)
	mux.HandleFunc(a.config.APIPrefix+"/settings", a.handleSettings)
	mux.HandleFunc(a.config.APIPrefix+"/profile/image", a.handleProfileImage)
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
