package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func getAgentDefinition(agentID string) (AgentDefinition, bool) {
	for _, agent := range agentDefinitions {
		if agent.AgentID == agentID {
			agent.AuthMode = normalizeAgentAuthMode(agent.AuthMode, agent.Provider)
			return agent, true
		}
	}
	if appInstance != nil {
		agents, err := appInstance.getCustomAgents()
		if err == nil {
			for _, agent := range agents {
				if agent.AgentID == agentID {
					agent.AuthMode = normalizeAgentAuthMode(agent.AuthMode, agent.Provider)
					return agent, true
				}
			}
		}
	}
	return AgentDefinition{}, false
}

func normalizeAgentAuthMode(authMode string, provider string) string {
	mode := strings.ToLower(strings.TrimSpace(authMode))
	if mode != "" {
		return mode
	}
	if strings.EqualFold(strings.TrimSpace(provider), "gemini") {
		return "google_oauth"
	}
	return "api_key"
}

func agentUsesGoogleOAuth(agent AgentDefinition) bool {
	return normalizeAgentAuthMode(agent.AuthMode, agent.Provider) == "google_oauth"
}

func agentUsesAPIKey(agent AgentDefinition) bool {
	return normalizeAgentAuthMode(agent.AuthMode, agent.Provider) == "api_key"
}

func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	if decoder.More() {
		return errors.New("invalid request body: unexpected trailing data")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, errorResponse{Detail: detail})
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
