package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/pubsub"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

const (
	googleChatBotScope           = "https://www.googleapis.com/auth/chat.bot"
	defaultGoogleChatTopicID     = "chat-events"
	googleChatListenerRetryDelay = 5 * time.Second
	googleChatMessageTimeout     = 3 * time.Minute
	googleChatLogPreviewLimit    = 160
)

type googleChatConfig struct {
	Enabled             bool
	ProjectID           string
	TopicID             string
	SubscriptionID      string
	ServiceAccountJSON  string
	ServiceAccountEmail string
}

type googleChatServiceAccountPayload struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
}

type googleChatSpace struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

type googleChatUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

type googleChatThread struct {
	Name string `json:"name"`
}

type googleChatIncomingMessage struct {
	Name         string           `json:"name"`
	Text         string           `json:"text"`
	ArgumentText string           `json:"argumentText"`
	Thread       googleChatThread `json:"thread"`
	Sender       *googleChatUser  `json:"sender,omitempty"`
}

type googleChatEvent struct {
	Type      string                     `json:"type"`
	EventTime string                     `json:"eventTime"`
	Space     googleChatSpace            `json:"space"`
	Message   *googleChatIncomingMessage `json:"message,omitempty"`
	User      *googleChatUser            `json:"user,omitempty"`
}

type googleChatAddOnMessagePayload struct {
	Message                   *googleChatIncomingMessage `json:"message,omitempty"`
	Space                     googleChatSpace            `json:"space"`
	ConfigCompleteRedirectURI string                     `json:"configCompleteRedirectUri"`
}

type googleChatAddOnAddedToSpacePayload struct {
	Space                     googleChatSpace `json:"space"`
	InteractionAdd            bool            `json:"interactionAdd"`
	ConfigCompleteRedirectURI string          `json:"configCompleteRedirectUri"`
}

type googleChatAddOnRemovedFromSpacePayload struct {
	Space googleChatSpace `json:"space"`
}

type googleChatAddOnButtonClickedPayload struct {
	Message *googleChatIncomingMessage `json:"message,omitempty"`
	Space   googleChatSpace            `json:"space"`
}

type googleChatAddOnAppCommandPayload struct {
	AppCommandMetadata map[string]any             `json:"appCommandMetadata,omitempty"`
	Space              googleChatSpace            `json:"space"`
	Thread             *googleChatThread          `json:"thread,omitempty"`
	Message            *googleChatIncomingMessage `json:"message,omitempty"`
}

type googleChatAddOnChatEvent struct {
	User                    *googleChatUser                         `json:"user,omitempty"`
	Space                   googleChatSpace                         `json:"space"`
	EventTime               string                                  `json:"eventTime"`
	MessagePayload          *googleChatAddOnMessagePayload          `json:"messagePayload,omitempty"`
	AddedToSpacePayload     *googleChatAddOnAddedToSpacePayload     `json:"addedToSpacePayload,omitempty"`
	RemovedFromSpacePayload *googleChatAddOnRemovedFromSpacePayload `json:"removedFromSpacePayload,omitempty"`
	ButtonClickedPayload    *googleChatAddOnButtonClickedPayload    `json:"buttonClickedPayload,omitempty"`
	AppCommandPayload       *googleChatAddOnAppCommandPayload       `json:"appCommandPayload,omitempty"`
}

type googleChatAddOnEvent struct {
	CommonEventObject map[string]any            `json:"commonEventObject,omitempty"`
	Chat              *googleChatAddOnChatEvent `json:"chat,omitempty"`
}

type googleChatPubSubResourceRef struct {
	ProjectID   string
	ResourceID  string
	WasFullPath bool
}

func parseGoogleChatServiceAccountJSON(raw string) (googleChatServiceAccountPayload, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return googleChatServiceAccountPayload{}, errors.New("service account JSON is required")
	}
	var payload googleChatServiceAccountPayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return googleChatServiceAccountPayload{}, fmt.Errorf("invalid service account JSON: %w", err)
	}
	if strings.TrimSpace(payload.ClientEmail) == "" {
		return googleChatServiceAccountPayload{}, errors.New("service account JSON is missing client_email")
	}
	if payload.Type != "" && payload.Type != "service_account" {
		return googleChatServiceAccountPayload{}, errors.New("service account JSON must be for a service_account")
	}
	return payload, nil
}

func googleChatConfigConfigured(cfg googleChatConfig) bool {
	return strings.TrimSpace(cfg.ProjectID) != "" &&
		strings.TrimSpace(cfg.SubscriptionID) != "" &&
		strings.TrimSpace(cfg.ServiceAccountJSON) != ""
}

func googleChatEnabled(raw string) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func parseGoogleChatPubSubResource(resourceKind string, raw string) (googleChatPubSubResourceRef, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return googleChatPubSubResourceRef{}, nil
	}
	expectedCollection := resourceKind + "s"
	if !strings.Contains(trimmed, "/") {
		return googleChatPubSubResourceRef{
			ResourceID: trimmed,
		}, nil
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) != 4 || parts[0] != "projects" || strings.TrimSpace(parts[1]) == "" || parts[2] != expectedCollection || strings.TrimSpace(parts[3]) == "" {
		return googleChatPubSubResourceRef{}, fmt.Errorf("google_chat.%s_id must be either the short %s name or projects/{project}/%s/{name}", resourceKind, resourceKind, expectedCollection)
	}
	return googleChatPubSubResourceRef{
		ProjectID:   strings.TrimSpace(parts[1]),
		ResourceID:  strings.TrimSpace(parts[3]),
		WasFullPath: true,
	}, nil
}

func googleChatLogPreview(raw string) string {
	trimmed := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if trimmed == "" {
		return ""
	}
	return truncateString(trimmed, googleChatLogPreviewLimit)
}

func formatGoogleChatPubSubResourcePath(projectID string, resourceKind string, resourceID string) string {
	projectID = strings.TrimSpace(projectID)
	resourceID = strings.TrimSpace(resourceID)
	if projectID == "" || resourceID == "" {
		return resourceID
	}
	return fmt.Sprintf("projects/%s/%ss/%s", projectID, resourceKind, resourceID)
}

func googleChatSenderDisplayName(event googleChatEvent) string {
	if event.User != nil && strings.TrimSpace(event.User.DisplayName) != "" {
		return strings.TrimSpace(event.User.DisplayName)
	}
	if event.Message != nil && event.Message.Sender != nil && strings.TrimSpace(event.Message.Sender.DisplayName) != "" {
		return strings.TrimSpace(event.Message.Sender.DisplayName)
	}
	return ""
}

func googleChatThreadName(event googleChatEvent) string {
	if event.Message == nil {
		return ""
	}
	return strings.TrimSpace(event.Message.Thread.Name)
}

func parseGoogleChatInteractionEvent(raw []byte) (googleChatEvent, error) {
	var event googleChatAddOnEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return googleChatEvent{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if event.Chat == nil {
		return googleChatEvent{}, errors.New("expected Google Chat add-on event with top-level field \"chat\"")
	}
	if strings.TrimSpace(event.Chat.Space.Name) == "" &&
		(event.Chat.MessagePayload == nil || strings.TrimSpace(event.Chat.MessagePayload.Space.Name) == "") &&
		(event.Chat.AddedToSpacePayload == nil || strings.TrimSpace(event.Chat.AddedToSpacePayload.Space.Name) == "") &&
		(event.Chat.RemovedFromSpacePayload == nil || strings.TrimSpace(event.Chat.RemovedFromSpacePayload.Space.Name) == "") &&
		(event.Chat.AppCommandPayload == nil || strings.TrimSpace(event.Chat.AppCommandPayload.Space.Name) == "") &&
		(event.Chat.ButtonClickedPayload == nil || strings.TrimSpace(event.Chat.ButtonClickedPayload.Space.Name) == "") {
		return googleChatEvent{}, errors.New("expected Google Chat add-on event with chat.space or payload space")
	}
	base := googleChatEvent{
		EventTime: strings.TrimSpace(event.Chat.EventTime),
		Space:     event.Chat.Space,
		User:      event.Chat.User,
	}
	switch {
	case event.Chat.MessagePayload != nil:
		base.Type = "MESSAGE"
		base.Message = event.Chat.MessagePayload.Message
		if strings.TrimSpace(base.Space.Name) == "" {
			base.Space = event.Chat.MessagePayload.Space
		}
		if base.Message == nil {
			return googleChatEvent{}, errors.New("expected Google Chat add-on messagePayload.message")
		}
		return base, nil
	case event.Chat.AppCommandPayload != nil:
		base.Type = "MESSAGE"
		base.Message = event.Chat.AppCommandPayload.Message
		if strings.TrimSpace(base.Space.Name) == "" {
			base.Space = event.Chat.AppCommandPayload.Space
		}
		if base.Message == nil {
			return googleChatEvent{}, errors.New("expected Google Chat add-on appCommandPayload.message")
		}
		if event.Chat.AppCommandPayload.Thread != nil && strings.TrimSpace(base.Message.Thread.Name) == "" {
			base.Message.Thread = *event.Chat.AppCommandPayload.Thread
		}
		return base, nil
	case event.Chat.AddedToSpacePayload != nil:
		base.Type = "ADDED_TO_SPACE"
		if strings.TrimSpace(base.Space.Name) == "" {
			base.Space = event.Chat.AddedToSpacePayload.Space
		}
		return base, nil
	case event.Chat.RemovedFromSpacePayload != nil:
		base.Type = "REMOVED_FROM_SPACE"
		if strings.TrimSpace(base.Space.Name) == "" {
			base.Space = event.Chat.RemovedFromSpacePayload.Space
		}
		return base, nil
	case event.Chat.ButtonClickedPayload != nil:
		base.Type = "CARD_CLICKED"
		base.Message = event.Chat.ButtonClickedPayload.Message
		if strings.TrimSpace(base.Space.Name) == "" {
			base.Space = event.Chat.ButtonClickedPayload.Space
		}
		return base, nil
	default:
		return googleChatEvent{}, errors.New("unsupported Google Chat add-on payload: expected one of messagePayload, appCommandPayload, addedToSpacePayload, removedFromSpacePayload, or buttonClickedPayload")
	}
}

func googleChatPayloadSummary(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{"keys=" + strings.Join(keys, ",")}
	if value, ok := payload["type"].(string); ok && strings.TrimSpace(value) != "" {
		parts = append(parts, fmt.Sprintf("type=%q", strings.TrimSpace(value)))
	}
	if value, ok := payload["eventType"].(string); ok && strings.TrimSpace(value) != "" {
		parts = append(parts, fmt.Sprintf("eventType=%q", strings.TrimSpace(value)))
	}
	return strings.Join(parts, " ")
}

func (a *App) getGoogleChatConfig() (googleChatConfig, error) {
	enabledRaw, err := a.getSecret("google_chat_enabled")
	if err != nil {
		return googleChatConfig{}, err
	}
	projectID, err := a.getSecret("google_chat_project_id")
	if err != nil {
		return googleChatConfig{}, err
	}
	topicID, err := a.getSecret("google_chat_topic_id")
	if err != nil {
		return googleChatConfig{}, err
	}
	subscriptionID, err := a.getSecret("google_chat_subscription_id")
	if err != nil {
		return googleChatConfig{}, err
	}
	serviceAccountJSON, err := a.getSecret("google_chat_service_account_json")
	if err != nil {
		return googleChatConfig{}, err
	}
	serviceAccountEmail, err := a.getSecret("google_chat_service_account_email")
	if err != nil {
		return googleChatConfig{}, err
	}
	if strings.TrimSpace(topicID) == "" {
		topicID = defaultGoogleChatTopicID
	}
	topicRef, err := parseGoogleChatPubSubResource("topic", topicID)
	if err != nil {
		return googleChatConfig{}, err
	}
	subscriptionRef, err := parseGoogleChatPubSubResource("subscription", subscriptionID)
	if err != nil {
		return googleChatConfig{}, err
	}
	return googleChatConfig{
		Enabled:             googleChatEnabled(enabledRaw),
		ProjectID:           strings.TrimSpace(projectID),
		TopicID:             strings.TrimSpace(topicRef.ResourceID),
		SubscriptionID:      strings.TrimSpace(subscriptionRef.ResourceID),
		ServiceAccountJSON:  strings.TrimSpace(serviceAccountJSON),
		ServiceAccountEmail: strings.TrimSpace(serviceAccountEmail),
	}, nil
}

func (a *App) snapshotGoogleChatRuntimeState() GoogleChatRuntimeState {
	a.googleChatMu.Lock()
	defer a.googleChatMu.Unlock()
	return a.googleChatState
}

func (a *App) updateGoogleChatState(mutator func(*GoogleChatRuntimeState)) {
	a.googleChatMu.Lock()
	defer a.googleChatMu.Unlock()
	mutator(&a.googleChatState)
}

func (a *App) updateGoogleChatStateForRun(runID int64, mutator func(*GoogleChatRuntimeState)) {
	a.googleChatMu.Lock()
	defer a.googleChatMu.Unlock()
	if a.googleChatRunID != runID {
		return
	}
	mutator(&a.googleChatState)
}

func (a *App) buildGoogleChatSettingsOut() (GoogleChatSettingsOut, error) {
	cfg, err := a.getGoogleChatConfig()
	if err != nil {
		return GoogleChatSettingsOut{}, err
	}
	state := a.snapshotGoogleChatRuntimeState()
	return GoogleChatSettingsOut{
		Enabled:               cfg.Enabled,
		Configured:            googleChatConfigConfigured(cfg),
		Running:               state.Running,
		ProjectID:             cfg.ProjectID,
		TopicID:               formatGoogleChatPubSubResourcePath(cfg.ProjectID, "topic", cfg.TopicID),
		SubscriptionID:        formatGoogleChatPubSubResourcePath(cfg.ProjectID, "subscription", cfg.SubscriptionID),
		HasServiceAccountJSON: strings.TrimSpace(cfg.ServiceAccountJSON) != "",
		ServiceAccountEmail:   cfg.ServiceAccountEmail,
		LastError:             state.LastError,
		LastEventType:         state.LastEventType,
		LastEventAt:           state.LastEventAt,
		LastReplyAt:           state.LastReplyAt,
	}, nil
}

func (a *App) saveGoogleChatSettings(payload *GoogleChatSettingsIn) error {
	if payload == nil {
		return nil
	}

	cfg, err := a.getGoogleChatConfig()
	if err != nil {
		return err
	}

	projectID := strings.TrimSpace(payload.ProjectID)
	topicID := strings.TrimSpace(payload.TopicID)
	if topicID == "" {
		topicID = defaultGoogleChatTopicID
	}
	subscriptionID := strings.TrimSpace(payload.SubscriptionID)
	serviceAccountJSON := strings.TrimSpace(payload.ServiceAccountJSON)
	serviceAccountEmail := cfg.ServiceAccountEmail
	parsedServiceAccount := googleChatServiceAccountPayload{}
	topicRef, err := parseGoogleChatPubSubResource("topic", topicID)
	if err != nil {
		return err
	}
	subscriptionRef, err := parseGoogleChatPubSubResource("subscription", subscriptionID)
	if err != nil {
		return err
	}

	if serviceAccountJSON != "" {
		parsedServiceAccount, err = parseGoogleChatServiceAccountJSON(serviceAccountJSON)
		if err != nil {
			return err
		}
		serviceAccountEmail = strings.TrimSpace(parsedServiceAccount.ClientEmail)
		if projectID == "" && strings.TrimSpace(parsedServiceAccount.ProjectID) != "" {
			projectID = strings.TrimSpace(parsedServiceAccount.ProjectID)
		}
	} else {
		serviceAccountJSON = cfg.ServiceAccountJSON
	}
	if projectID == "" && strings.TrimSpace(topicRef.ProjectID) != "" {
		projectID = strings.TrimSpace(topicRef.ProjectID)
	}
	if projectID == "" && strings.TrimSpace(subscriptionRef.ProjectID) != "" {
		projectID = strings.TrimSpace(subscriptionRef.ProjectID)
	}
	if projectID != "" && topicRef.ProjectID != "" && !strings.EqualFold(topicRef.ProjectID, projectID) {
		return fmt.Errorf("google_chat.topic_id project %q does not match google_chat.project_id %q", topicRef.ProjectID, projectID)
	}
	if projectID != "" && subscriptionRef.ProjectID != "" && !strings.EqualFold(subscriptionRef.ProjectID, projectID) {
		return fmt.Errorf("google_chat.subscription_id project %q does not match google_chat.project_id %q", subscriptionRef.ProjectID, projectID)
	}
	topicID = strings.TrimSpace(topicRef.ResourceID)
	if topicID == "" {
		topicID = defaultGoogleChatTopicID
	}
	subscriptionID = strings.TrimSpace(subscriptionRef.ResourceID)

	if payload.Enabled {
		if strings.TrimSpace(projectID) == "" {
			return errors.New("google_chat.project_id is required when Google Chat is enabled")
		}
		if strings.TrimSpace(subscriptionID) == "" {
			return errors.New("google_chat.subscription_id is required when Google Chat is enabled")
		}
		if strings.TrimSpace(serviceAccountJSON) == "" {
			return errors.New("google_chat.service_account_json is required when Google Chat is enabled")
		}
	}

	if err := a.upsertSecret("google_chat_enabled", fmt.Sprintf("%t", payload.Enabled)); err != nil {
		return err
	}
	if err := a.upsertSecret("google_chat_project_id", projectID); err != nil {
		return err
	}
	if err := a.upsertSecret("google_chat_topic_id", topicID); err != nil {
		return err
	}
	if err := a.upsertSecret("google_chat_subscription_id", subscriptionID); err != nil {
		return err
	}
	if strings.TrimSpace(serviceAccountJSON) != "" {
		if err := a.upsertSecret("google_chat_service_account_json", serviceAccountJSON); err != nil {
			return err
		}
	}
	if err := a.upsertSecret("google_chat_service_account_email", serviceAccountEmail); err != nil {
		return err
	}

	return a.restartGoogleChatListener()
}

func (a *App) restartGoogleChatListener() error {
	cfg, err := a.getGoogleChatConfig()
	if err != nil {
		a.updateGoogleChatState(func(state *GoogleChatRuntimeState) {
			state.Running = false
			state.LastError = truncateString(err.Error(), 1000)
		})
		return err
	}

	a.googleChatMu.Lock()
	oldCancel := a.googleChatCancel
	a.googleChatCancel = nil
	a.googleChatRunID++
	runID := a.googleChatRunID
	if !cfg.Enabled || !googleChatConfigConfigured(cfg) {
		a.googleChatState.Running = false
		if !cfg.Enabled {
			a.googleChatState.LastError = ""
		}
	}
	a.googleChatMu.Unlock()

	if oldCancel != nil {
		log.Printf("google chat warning: stopping existing listener before restart")
		oldCancel()
	}

	if !cfg.Enabled || !googleChatConfigConfigured(cfg) {
		if !cfg.Enabled {
			log.Printf("google chat warning: listener is disabled")
		} else {
			log.Printf("google chat warning: listener is not fully configured; project=%q subscription=%q service_account_json=%t", cfg.ProjectID, cfg.SubscriptionID, strings.TrimSpace(cfg.ServiceAccountJSON) != "")
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.googleChatMu.Lock()
	a.googleChatCancel = cancel
	a.googleChatState.Running = true
	a.googleChatState.LastError = ""
	a.googleChatMu.Unlock()

	log.Printf("google chat success: listener starting for project=%q subscription=%q topic=%q service_account=%q", cfg.ProjectID, cfg.SubscriptionID, cfg.TopicID, cfg.ServiceAccountEmail)
	go a.runGoogleChatListener(ctx, runID, cfg)
	return nil
}

func (a *App) runGoogleChatListener(ctx context.Context, runID int64, cfg googleChatConfig) {
	for {
		err := a.receiveGoogleChatEvents(ctx, cfg)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			a.updateGoogleChatStateForRun(runID, func(state *GoogleChatRuntimeState) {
				state.Running = false
			})
			return
		}
		if err != nil {
			log.Printf("google chat listener error: %v", err)
			a.updateGoogleChatStateForRun(runID, func(state *GoogleChatRuntimeState) {
				state.Running = true
				state.LastError = truncateString(err.Error(), 1000)
			})
		}
		select {
		case <-ctx.Done():
			a.updateGoogleChatStateForRun(runID, func(state *GoogleChatRuntimeState) {
				state.Running = false
			})
			return
		case <-time.After(googleChatListenerRetryDelay):
		}
	}
}

func buildGoogleChatHTTPClient(ctx context.Context, serviceAccountJSON string) (*http.Client, error) {
	jwtConfig, err := google.JWTConfigFromJSON([]byte(serviceAccountJSON), googleChatBotScope)
	if err != nil {
		return nil, fmt.Errorf("failed to build Chat API credentials: %w", err)
	}
	client := jwtConfig.Client(ctx)
	client.Timeout = 60 * time.Second
	return client, nil
}

func (a *App) receiveGoogleChatEvents(ctx context.Context, cfg googleChatConfig) error {
	client, err := pubsub.NewClient(ctx, cfg.ProjectID, option.WithCredentialsJSON([]byte(cfg.ServiceAccountJSON)))
	if err != nil {
		return fmt.Errorf("failed to create Pub/Sub client: %w", err)
	}
	defer client.Close()

	httpClient, err := buildGoogleChatHTTPClient(ctx, cfg.ServiceAccountJSON)
	if err != nil {
		return err
	}

	subscription := client.Subscription(cfg.SubscriptionID)
	log.Printf("google chat success: subscribed to Pub/Sub pull subscription=%q in project=%q", cfg.SubscriptionID, cfg.ProjectID)
	return subscription.Receive(ctx, func(messageCtx context.Context, msg *pubsub.Message) {
		ack, handleErr := a.processGoogleChatPubSubMessage(messageCtx, httpClient, msg)
		if handleErr != nil {
			a.updateGoogleChatState(func(state *GoogleChatRuntimeState) {
				state.LastError = truncateString(handleErr.Error(), 1000)
			})
			log.Printf("google chat failure: event handling failed for pubsub_message_id=%q: %v", msg.ID, handleErr)
		}
		if ack {
			log.Printf("google chat success: acked Pub/Sub message id=%q", msg.ID)
			msg.Ack()
			return
		}
		log.Printf("google chat warning: nacking Pub/Sub message id=%q for retry", msg.ID)
		msg.Nack()
	})
}

func (a *App) processGoogleChatPubSubMessage(ctx context.Context, httpClient *http.Client, msg *pubsub.Message) (bool, error) {
	event, err := parseGoogleChatInteractionEvent(msg.Data)
	if err != nil {
		log.Printf("google chat failure: invalid interaction event payload message_id=%q attributes=%v summary=%q raw=%q err=%v", msg.ID, msg.Attributes, googleChatPayloadSummary(msg.Data), googleChatLogPreview(string(msg.Data)), err)
		return true, fmt.Errorf("invalid Google Chat interaction event payload for message %q: %w", msg.ID, err)
	}

	eventAt := strings.TrimSpace(event.EventTime)
	if eventAt == "" {
		eventAt = time.Now().UTC().Format(time.RFC3339)
	}
	log.Printf(
		"google chat success: received event type=%q pubsub_message_id=%q space=%q thread=%q sender=%q preview=%q",
		strings.TrimSpace(event.Type),
		msg.ID,
		strings.TrimSpace(event.Space.Name),
		googleChatThreadName(event),
		googleChatSenderDisplayName(event),
		googleChatLogPreview(buildGoogleChatPrompt(event)),
	)
	a.updateGoogleChatState(func(state *GoogleChatRuntimeState) {
		state.LastEventType = strings.TrimSpace(event.Type)
		state.LastEventAt = eventAt
	})

	if err := a.handleGoogleChatEvent(ctx, httpClient, event, msg.ID); err != nil {
		return false, err
	}
	return true, nil
}

func (a *App) handleGoogleChatEvent(ctx context.Context, httpClient *http.Client, event googleChatEvent, deliveryID string) error {
	eventType := strings.TrimSpace(event.Type)
	switch eventType {
	case "REMOVED_FROM_SPACE":
		log.Printf("google chat warning: removed from space=%q", strings.TrimSpace(event.Space.Name))
		return nil
	case "ADDED_TO_SPACE":
		if event.Message == nil {
			log.Printf("google chat success: added to space=%q without initial message; sending welcome reply", strings.TrimSpace(event.Space.Name))
			return a.sendGoogleChatMessage(ctx, httpClient, event.Space.Name, "Thanks for adding me. I'm ready to help from Google Chat.", "", deliveryID+":welcome")
		}
		log.Printf("google chat success: added to space=%q with initial message; continuing through message handler", strings.TrimSpace(event.Space.Name))
		fallthrough
	case "MESSAGE":
		if googleChatMessageFromBot(event.Message) {
			log.Printf("google chat warning: ignoring bot-authored message in space=%q thread=%q", strings.TrimSpace(event.Space.Name), strings.TrimSpace(event.Message.Thread.Name))
			return nil
		}
		prompt := buildGoogleChatPrompt(event)
		if strings.TrimSpace(prompt) == "" {
			log.Printf("google chat warning: empty message text for space=%q thread=%q", strings.TrimSpace(event.Space.Name), strings.TrimSpace(event.Message.Thread.Name))
			return a.sendGoogleChatMessage(ctx, httpClient, event.Space.Name, "I can respond to text messages, but I couldn't find any message text to process.", event.Message.Thread.Name, deliveryID+":empty")
		}
		conversationID := googleChatConversationID(event.Space.Name, event.Message.Thread.Name)
		log.Printf("google chat success: dispatching message to model conversation_id=%q space=%q thread=%q sender=%q", conversationID, strings.TrimSpace(event.Space.Name), strings.TrimSpace(event.Message.Thread.Name), googleChatSenderDisplayName(event))
		requestCtx, cancel := context.WithTimeout(ctx, googleChatMessageTimeout)
		defer cancel()
		response, err := a.processChatMessage(requestCtx, conversationID, prompt, nil)
		if err != nil {
			log.Printf("google chat failure: model processing failed for conversation_id=%q: %v", conversationID, err)
			fallbackErr := a.sendGoogleChatMessage(ctx, httpClient, event.Space.Name, "I couldn't process that right now. Please check Towel's Google Chat settings and model configuration.", event.Message.Thread.Name, deliveryID+":error")
			if fallbackErr == nil {
				log.Printf("google chat success: sent fallback error reply for conversation_id=%q", conversationID)
				return nil
			}
			return fmt.Errorf("chat processing failed: %w; fallback reply also failed: %v", err, fallbackErr)
		}
		log.Printf("google chat success: model response ready for conversation_id=%q chars=%d", conversationID, len(strings.TrimSpace(response.Response)))
		return a.sendGoogleChatMessage(ctx, httpClient, event.Space.Name, response.Response, event.Message.Thread.Name, deliveryID+":reply")
	default:
		log.Printf("google chat warning: ignoring unsupported event type=%q", eventType)
		return nil
	}
}

func googleChatMessageFromBot(message *googleChatIncomingMessage) bool {
	if message == nil || message.Sender == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(message.Sender.Type), "BOT")
}

func buildGoogleChatPrompt(event googleChatEvent) string {
	if event.Message == nil {
		return ""
	}
	messageText := strings.TrimSpace(event.Message.ArgumentText)
	if messageText == "" {
		messageText = strings.TrimSpace(event.Message.Text)
	}
	if messageText == "" {
		return ""
	}
	parts := make([]string, 0, 4)
	parts = append(parts, "This message came from Google Chat.")
	if sender := googleChatActorDisplayName(event); sender != "" {
		parts = append(parts, "Sender: "+sender)
	}
	if spaceName := strings.TrimSpace(event.Space.DisplayName); spaceName != "" {
		parts = append(parts, "Space: "+spaceName)
	}
	parts = append(parts, "User message:", messageText)
	return strings.Join(parts, "\n")
}

func googleChatActorDisplayName(event googleChatEvent) string {
	return googleChatSenderDisplayName(event)
}

func googleChatConversationID(spaceName string, threadName string) string {
	seed := strings.TrimSpace(spaceName) + "\n" + strings.TrimSpace(threadName)
	if strings.TrimSpace(seed) == "" {
		seed = "google-chat"
	}
	sum := sha256.Sum256([]byte(seed))
	return "gchat-" + hex.EncodeToString(sum[:])[:32]
}

func googleChatRequestID(seed string) string {
	trimmed := strings.TrimSpace(seed)
	if trimmed == "" {
		trimmed = time.Now().UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(trimmed))
	return "gchat-" + hex.EncodeToString(sum[:])[:24]
}

func (a *App) sendGoogleChatMessage(ctx context.Context, httpClient *http.Client, spaceName string, text string, threadName string, requestSeed string) error {
	spaceName = strings.TrimSpace(spaceName)
	text = strings.TrimSpace(text)
	if spaceName == "" {
		return errors.New("google chat space name is required")
	}
	if text == "" {
		return errors.New("google chat reply text is empty")
	}
	payload := map[string]any{
		"text": truncateString(text, 32000),
	}
	if strings.TrimSpace(threadName) != "" {
		payload["thread"] = map[string]any{"name": strings.TrimSpace(threadName)}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	query := url.Values{}
	query.Set("requestId", googleChatRequestID(requestSeed))
	if strings.TrimSpace(threadName) != "" {
		query.Set("messageReplyOption", "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD")
	}
	endpoint := fmt.Sprintf("https://chat.googleapis.com/v1/%s/messages?%s", spaceName, query.Encode())
	log.Printf("google chat success: sending reply space=%q thread=%q chars=%d preview=%q", spaceName, strings.TrimSpace(threadName), len(text), googleChatLogPreview(text))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("google chat reply failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	a.updateGoogleChatState(func(state *GoogleChatRuntimeState) {
		state.LastReplyAt = time.Now().UTC().Format(time.RFC3339)
		state.LastError = ""
	})
	log.Printf("google chat success: reply sent space=%q thread=%q", spaceName, strings.TrimSpace(threadName))
	return nil
}
