package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) startStreamSession(sessionID string, cancel context.CancelFunc) error {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.pruneStreamSessionsLocked()

	if existing, ok := a.streamSessions[sessionID]; ok {
		if !existing.Completed {
			return errors.New("a stream is already active for this conversation")
		}
		for subscriberID, ch := range existing.Subscribers {
			close(ch)
			delete(existing.Subscribers, subscriberID)
		}
	}

	a.streamSessions[sessionID] = &streamSession{
		ID:          sessionID,
		Events:      make([]streamEvent, 0, 128),
		NextEventID: 1,
		Subscribers: make(map[int64]chan streamEvent),
		Cancel:      cancel,
		UpdatedAt:   time.Now().UTC(),
	}
	return nil
}

func (a *App) stopStreamSession(sessionID string) bool {
	a.streamMu.Lock()
	session, ok := a.streamSessions[sessionID]
	if !ok || session.Completed || session.Canceled {
		a.streamMu.Unlock()
		return false
	}
	session.Canceled = true
	session.Completed = true
	session.UpdatedAt = time.Now().UTC()
	cancel := session.Cancel
	a.streamMu.Unlock()

	if cancel != nil {
		cancel()
	}

	_ = a.publishStreamEvent(sessionID, "stopped", errorResponse{Detail: "Stopped"})
	return true
}

func (a *App) pruneStreamSessionsLocked() {
	cutoff := time.Now().UTC().Add(-streamSessionTTL)
	for sessionID, session := range a.streamSessions {
		if session.UpdatedAt.After(cutoff) {
			continue
		}
		for subscriberID, updates := range session.Subscribers {
			close(updates)
			delete(session.Subscribers, subscriberID)
		}
		delete(a.streamSessions, sessionID)
	}
}

func (a *App) subscribeStreamSession(sessionID string, lastEventID int64) ([]streamEvent, <-chan streamEvent, func(), bool, error) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.pruneStreamSessionsLocked()

	session, ok := a.streamSessions[sessionID]
	if !ok {
		return nil, nil, nil, false, errors.New("stream session not found")
	}

	backlog := make([]streamEvent, 0, len(session.Events))
	for _, event := range session.Events {
		if event.ID > lastEventID {
			backlog = append(backlog, event)
		}
	}

	if session.Completed {
		return backlog, nil, func() {}, true, nil
	}

	a.nextSubscriberID++
	subscriberID := a.nextSubscriberID
	updates := make(chan streamEvent, 128)
	session.Subscribers[subscriberID] = updates
	session.UpdatedAt = time.Now().UTC()

	unsubscribe := func() {
		a.streamMu.Lock()
		defer a.streamMu.Unlock()
		session, exists := a.streamSessions[sessionID]
		if !exists {
			return
		}
		ch, exists := session.Subscribers[subscriberID]
		if !exists {
			return
		}
		delete(session.Subscribers, subscriberID)
		close(ch)
	}

	return backlog, updates, unsubscribe, false, nil
}

func (a *App) publishStreamEvent(sessionID string, eventName string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	session, ok := a.streamSessions[sessionID]
	if !ok {
		return errors.New("stream session not found")
	}

	event := streamEvent{
		ID:    session.NextEventID,
		Event: eventName,
		Data:  encoded,
	}
	session.NextEventID++
	session.Events = append(session.Events, event)
	session.UpdatedAt = time.Now().UTC()
	if eventName == "done" || eventName == "failed" || eventName == "stopped" {
		session.Completed = true
		if eventName == "stopped" {
			session.Canceled = true
		}
		session.Cancel = nil
	}

	for _, updates := range session.Subscribers {
		select {
		case updates <- event:
		default:
		}
	}

	return nil
}

func (a *App) emitStreamToken(sessionID string, token string) error {
	if token == "" {
		return nil
	}
	return a.publishStreamEvent(sessionID, "token", map[string]string{"v": token})
}

func (a *App) emitStreamDone(sessionID string, response ChatMessageOut) error {
	return a.publishStreamEvent(sessionID, "done", response)
}

func (a *App) emitStreamError(sessionID string, detail string) error {
	return a.publishStreamEvent(sessionID, "failed", errorResponse{Detail: detail})
}

func parseLastEventID(r *http.Request) int64 {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("last_event_id"))
	}
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func (a *App) getStreamSessionState(sessionID string) (map[string]any, bool) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()

	session, ok := a.streamSessions[sessionID]
	if !ok {
		return nil, false
	}

	// Accumulate tokens to build current message content
	var content strings.Builder
	var actions []string
	var hasError bool
	var errorMsg string
	var lastEventID int64

	for _, event := range session.Events {
		lastEventID = event.ID
		switch event.Event {
		case "token":
			var payload map[string]string
			if err := json.Unmarshal(event.Data, &payload); err == nil {
				content.WriteString(payload["v"])
			}
		case "done":
			var payload ChatMessageOut
			if err := json.Unmarshal(event.Data, &payload); err == nil {
				content.Reset()
				content.WriteString(payload.Response)
				actions = payload.Actions
			}
		case "failed":
			var payload errorResponse
			if err := json.Unmarshal(event.Data, &payload); err == nil {
				hasError = true
				errorMsg = payload.Detail
			}
		case "stopped":
			var payload errorResponse
			if err := json.Unmarshal(event.Data, &payload); err == nil {
				hasError = true
				errorMsg = payload.Detail
			}
		}
	}

	return map[string]any{
		"content":       content.String(),
		"actions":       actions,
		"canceled":      session.Canceled,
		"completed":     session.Completed,
		"last_event_id": lastEventID,
		"has_error":     hasError,
		"error":         errorMsg,
	}, true
}

func writeSSEEvent(w io.Writer, event streamEvent) error {
	if event.Event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event.Event); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "id: %d\n", event.ID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", event.Data); err != nil {
		return err
	}
	return nil
}
