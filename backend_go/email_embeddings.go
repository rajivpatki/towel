package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"log"
	"net/http"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	emailEmbeddingDimensions         = 768
	emailEmbeddingMaxTextChars       = 6000
	emailEmbeddingMaxBodyChars       = 4500
	defaultEmailEmbeddingWorkerCount = 4
	defaultEmailEmbeddingQueueSize   = 128
)

var (
	replyHeaderLinePattern = regexp.MustCompile(`(?i)^on .+wrote:$`)
	headerBlockLinePattern = regexp.MustCompile(`(?i)^(from|sent|to|cc|bcc|subject):`)
)

type emailEmbeddingConfig struct {
	Provider   string
	Model      string
	Dimensions int
}

type emailEmbeddingSource struct {
	MessageID        string
	ThreadID         string
	Subject          string
	FromName         string
	FromEmail        string
	ToAddresses      string
	CcAddresses      string
	BccAddresses     string
	ListID           string
	AttachmentNames  string
	Snippet          string
	BodyText         string
	BodyHTML         string
	InternalDateUnix int64
	HasAttachments   bool
	IsInTrash        bool
	IsInSpam         bool
	SyncUpdatedAt    string
}

type emailEmbeddingDocument struct {
	Source            emailEmbeddingSource
	Text              string
	Title             string
	SourceFingerprint string
}

type emailEmbeddingStats struct {
	Count    int
	Provider string
	Model    string
}

type emailEmbeddingJob struct {
	MessageID  string
	Operation  string
	Reason     string
	Generation int64
	EnqueuedAt time.Time
}

type emailEmbeddingExistingState struct {
	EmbeddingID       int64
	ThreadID          string
	EmbeddingText     string
	SourceFingerprint string
	VectorJSON        string
	Subject           string
	FromEmail         string
	InternalDateUnix  int64
	HasAttachments    bool
	IsInTrash         bool
	IsInSpam          bool
	Provider          string
	Model             string
	Dimensions        int
}

type semanticEmailSearchOptions struct {
	Query          string
	TopK           int
	FromEmail      string
	AfterUnix      *int64
	BeforeUnix     *int64
	HasAttachments *bool
	IncludeSpam    bool
	IncludeTrash   bool
}

func resolveEmailEmbeddingConfig(agent AgentDefinition) (emailEmbeddingConfig, bool) {
	switch strings.ToLower(strings.TrimSpace(agent.Provider)) {
	case "openai":
		return emailEmbeddingConfig{
			Provider:   "openai",
			Model:      "text-embedding-3-small",
			Dimensions: emailEmbeddingDimensions,
		}, true
	case "gemini":
		return emailEmbeddingConfig{
			Provider:   "gemini",
			Model:      "gemini-embedding-001",
			Dimensions: emailEmbeddingDimensions,
		}, true
	default:
		return emailEmbeddingConfig{}, false
	}
}

func (a *App) resolveCurrentEmailEmbeddingConfig() (AgentDefinition, emailEmbeddingConfig, string, bool, error) {
	state, err := a.getSetupState()
	if err != nil {
		return AgentDefinition{}, emailEmbeddingConfig{}, "", false, err
	}
	if state.SelectedAgentID == nil || strings.TrimSpace(*state.SelectedAgentID) == "" {
		return AgentDefinition{}, emailEmbeddingConfig{}, "", false, nil
	}
	agent, ok := getAgentDefinition(strings.TrimSpace(*state.SelectedAgentID))
	if !ok {
		return AgentDefinition{}, emailEmbeddingConfig{}, "", false, nil
	}
	config, supported := resolveEmailEmbeddingConfig(agent)
	if !supported {
		return agent, emailEmbeddingConfig{}, "", false, nil
	}
	credential, err := a.getLLMCredential(agent)
	if err != nil {
		return AgentDefinition{}, emailEmbeddingConfig{}, "", false, err
	}
	return agent, config, credential, true, nil
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func appendUniqueStringSet(target map[string]struct{}, values []string) map[string]struct{} {
	if len(values) == 0 {
		return target
	}
	if target == nil {
		target = make(map[string]struct{}, len(values))
	}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		target[trimmed] = struct{}{}
	}
	return target
}

func sortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}

func (a *App) startEmailEmbeddingWorkers() {
	a.emailEmbeddingMu.Lock()
	if a.emailEmbeddingCond == nil {
		a.emailEmbeddingCond = sync.NewCond(&a.emailEmbeddingMu)
	}
	if a.emailEmbeddingPendingJobs == nil {
		a.emailEmbeddingPendingJobs = make(map[string]emailEmbeddingJob)
	}
	if a.emailEmbeddingPendingLimit <= 0 {
		a.emailEmbeddingPendingLimit = defaultEmailEmbeddingQueueSize
	}
	if a.emailEmbeddingWorkersStarted {
		a.emailEmbeddingMu.Unlock()
		return
	}
	a.emailEmbeddingWorkersStarted = true
	a.emailEmbeddingMu.Unlock()

	for workerIndex := 0; workerIndex < defaultEmailEmbeddingWorkerCount; workerIndex++ {
		go a.runEmailEmbeddingWorker(workerIndex + 1)
	}
}

func (a *App) runEmailEmbeddingWorker(workerID int) {
	for {
		job := a.dequeueEmailEmbeddingJob()
		err := a.processEmailEmbeddingJob(job)
		a.finishEmailEmbeddingJob(err == nil)
		if err != nil {
			log.Printf("email embedding failure: worker=%d operation=%s message_id=%s generation=%d reason=%s err=%v", workerID, job.Operation, job.MessageID, job.Generation, job.Reason, err)
		}
	}
}

func (a *App) dequeueEmailEmbeddingJob() emailEmbeddingJob {
	a.emailEmbeddingMu.Lock()
	defer a.emailEmbeddingMu.Unlock()

	for {
		for len(a.emailEmbeddingPendingOrder) > 0 {
			messageID := strings.TrimSpace(a.emailEmbeddingPendingOrder[0])
			a.emailEmbeddingPendingOrder = a.emailEmbeddingPendingOrder[1:]
			job, ok := a.emailEmbeddingPendingJobs[messageID]
			if !ok {
				continue
			}
			delete(a.emailEmbeddingPendingJobs, messageID)
			a.emailEmbeddingQueuedCount = len(a.emailEmbeddingPendingJobs)
			a.emailEmbeddingInFlightCount++
			a.emailEmbeddingCond.Broadcast()
			return job
		}
		a.emailEmbeddingCond.Wait()
	}
}

func (a *App) finishEmailEmbeddingJob(success bool) {
	a.emailEmbeddingMu.Lock()
	defer a.emailEmbeddingMu.Unlock()

	if a.emailEmbeddingInFlightCount > 0 {
		a.emailEmbeddingInFlightCount--
	}
	if success {
		a.emailEmbeddingLastEmbeddedAt = time.Now().UTC().Format(time.RFC3339)
	}
	a.emailEmbeddingCond.Broadcast()
}

func (a *App) currentEmailEmbeddingGeneration() int64 {
	a.emailEmbeddingMu.Lock()
	defer a.emailEmbeddingMu.Unlock()
	return a.emailEmbeddingGeneration
}

func (a *App) resetEmailEmbeddingQueueState() int64 {
	a.emailEmbeddingMu.Lock()
	defer a.emailEmbeddingMu.Unlock()

	if a.emailEmbeddingCond == nil {
		a.emailEmbeddingCond = sync.NewCond(&a.emailEmbeddingMu)
	}
	if a.emailEmbeddingPendingJobs == nil {
		a.emailEmbeddingPendingJobs = make(map[string]emailEmbeddingJob)
	}
	a.emailEmbeddingGeneration++
	a.emailEmbeddingPendingOrder = nil
	a.emailEmbeddingPendingJobs = make(map[string]emailEmbeddingJob)
	a.emailEmbeddingQueuedCount = 0
	a.emailEmbeddingCond.Broadcast()
	return a.emailEmbeddingGeneration
}

func (a *App) enqueueEmailEmbeddingJob(job emailEmbeddingJob) error {
	messageID := strings.TrimSpace(job.MessageID)
	if messageID == "" {
		return nil
	}
	a.startEmailEmbeddingWorkers()

	a.emailEmbeddingMu.Lock()
	defer a.emailEmbeddingMu.Unlock()

	if job.Generation == 0 {
		job.Generation = a.emailEmbeddingGeneration
	}
	job.MessageID = messageID
	job.Operation = strings.TrimSpace(job.Operation)
	if job.Operation == "" {
		job.Operation = "upsert"
	}
	job.Reason = strings.TrimSpace(job.Reason)
	if job.EnqueuedAt.IsZero() {
		job.EnqueuedAt = time.Now().UTC()
	}

	for {
		if _, exists := a.emailEmbeddingPendingJobs[messageID]; exists {
			a.emailEmbeddingPendingJobs[messageID] = job
			return nil
		}
		if len(a.emailEmbeddingPendingJobs) < a.emailEmbeddingPendingLimit {
			a.emailEmbeddingPendingJobs[messageID] = job
			a.emailEmbeddingPendingOrder = append(a.emailEmbeddingPendingOrder, messageID)
			a.emailEmbeddingQueuedCount = len(a.emailEmbeddingPendingJobs)
			a.emailEmbeddingCond.Broadcast()
			return nil
		}
		a.emailEmbeddingCond.Wait()
	}
}

func (a *App) enqueueEmailEmbeddingUpsert(messageID string, reason string) error {
	return a.enqueueEmailEmbeddingJob(emailEmbeddingJob{
		MessageID:  messageID,
		Operation:  "upsert",
		Reason:     reason,
		Generation: a.currentEmailEmbeddingGeneration(),
	})
}

func (a *App) waitForEmailEmbeddingDrain() {
	a.startEmailEmbeddingWorkers()
	a.emailEmbeddingMu.Lock()
	defer a.emailEmbeddingMu.Unlock()
	for len(a.emailEmbeddingPendingJobs) > 0 || a.emailEmbeddingInFlightCount > 0 {
		a.emailEmbeddingCond.Wait()
	}
}

func (a *App) getEmailEmbeddingQueueStatus() (int, int, string) {
	a.emailEmbeddingMu.Lock()
	defer a.emailEmbeddingMu.Unlock()
	return a.emailEmbeddingQueuedCount, a.emailEmbeddingInFlightCount, strings.TrimSpace(a.emailEmbeddingLastEmbeddedAt)
}

func (a *App) refreshEmailEmbeddingsInBackground(reason string, reset bool) {
	go func() {
		generation := a.currentEmailEmbeddingGeneration()
		if reset {
			generation = a.resetEmailEmbeddingQueueState()
			if err := a.clearEmailEmbeddings(); err != nil {
				log.Printf("email embedding failure: reason=%s step=clear err=%v", reason, err)
				return
			}
		}
		messageIDs, err := a.listActiveSyncedEmailMessageIDs()
		if err != nil {
			log.Printf("email embedding failure: reason=%s step=list_all_message_ids err=%v", reason, err)
			return
		}
		for _, messageID := range messageIDs {
			if err := a.enqueueEmailEmbeddingJob(emailEmbeddingJob{
				MessageID:  messageID,
				Operation:  "upsert",
				Reason:     reason,
				Generation: generation,
			}); err != nil {
				log.Printf("email embedding failure: reason=%s step=enqueue_full_rescan message_id=%s err=%v", reason, messageID, err)
				return
			}
		}
	}()
}

func (a *App) refreshEmailEmbeddingsForMessageIDsInBackground(reason string, messageIDs []string, deletedMessageIDs []string) {
	go func() {
		for _, messageID := range deletedMessageIDs {
			if err := a.deleteEmailEmbeddingByMessageID(messageID); err != nil {
				log.Printf("email embedding failure: reason=%s step=delete_message message_id=%s err=%v", reason, strings.TrimSpace(messageID), err)
				return
			}
		}
		for _, messageID := range messageIDs {
			if err := a.enqueueEmailEmbeddingUpsert(messageID, reason); err != nil {
				log.Printf("email embedding failure: reason=%s step=enqueue_message message_id=%s err=%v", reason, strings.TrimSpace(messageID), err)
				return
			}
		}
	}()
}

func (a *App) processEmailEmbeddingJob(job emailEmbeddingJob) error {
	if !a.isCurrentEmailEmbeddingGeneration(job.Generation) {
		return nil
	}
	switch job.Operation {
	case "delete":
		return a.deleteEmailEmbeddingByMessageID(job.MessageID)
	case "upsert":
		return a.processEmailEmbeddingUpsertJob(job)
	default:
		return fmt.Errorf("unsupported email embedding job operation: %s", job.Operation)
	}
}

func (a *App) processEmailEmbeddingUpsertJob(job emailEmbeddingJob) error {
	agent, config, credential, supported, err := a.resolveCurrentEmailEmbeddingConfig()
	if err != nil {
		return err
	}
	if !supported {
		return nil
	}

	source, ok, err := a.getEmailEmbeddingSourceByMessageID(job.MessageID)
	if err != nil {
		return err
	}
	if !ok {
		return a.deleteEmailEmbeddingByMessageID(job.MessageID)
	}

	document := buildEmailEmbeddingDocument(source)
	if strings.TrimSpace(document.Text) == "" {
		return a.deleteEmailEmbeddingByMessageID(job.MessageID)
	}

	existingState, hasExisting, err := a.getEmailEmbeddingExistingState(job.MessageID)
	if err != nil {
		return err
	}
	if hasExisting &&
		existingState.SourceFingerprint == document.SourceFingerprint &&
		existingState.Provider == config.Provider &&
		existingState.Model == config.Model &&
		existingState.Dimensions == config.Dimensions {
		if !emailEmbeddingMetadataChanged(existingState, document) {
			return nil
		}
		if !a.isCurrentEmailEmbeddingGeneration(job.Generation) {
			return nil
		}
		return a.applyEmailEmbeddingMetadataRefresh(document, existingState)
	}

	vectors, err := a.embedTexts(context.Background(), agent, config, credential, []string{document.Text}, []string{document.Title}, "RETRIEVAL_DOCUMENT")
	if err != nil {
		return err
	}
	if len(vectors) != 1 {
		return fmt.Errorf("embedding provider returned %d vectors for 1 input", len(vectors))
	}
	if !a.isCurrentEmailEmbeddingGeneration(job.Generation) {
		return nil
	}
	return a.applyEmailEmbeddingUpsert(document, vectors[0], config)
}

func (a *App) isCurrentEmailEmbeddingGeneration(generation int64) bool {
	a.emailEmbeddingMu.Lock()
	defer a.emailEmbeddingMu.Unlock()
	return generation == a.emailEmbeddingGeneration
}

func emailEmbeddingMetadataChanged(existing emailEmbeddingExistingState, document emailEmbeddingDocument) bool {
	if existing.ThreadID != strings.TrimSpace(document.Source.ThreadID) {
		return true
	}
	if existing.EmbeddingText != strings.TrimSpace(document.Text) {
		return true
	}
	if cleanWhitespace(existing.Subject) != cleanWhitespace(document.Source.Subject) {
		return true
	}
	if strings.ToLower(cleanWhitespace(existing.FromEmail)) != strings.ToLower(cleanWhitespace(document.Source.FromEmail)) {
		return true
	}
	if existing.InternalDateUnix != document.Source.InternalDateUnix {
		return true
	}
	if existing.HasAttachments != document.Source.HasAttachments {
		return true
	}
	if existing.IsInTrash != document.Source.IsInTrash {
		return true
	}
	if existing.IsInSpam != document.Source.IsInSpam {
		return true
	}
	return false
}

func (a *App) listEmailEmbeddingSources(messageIDs []string) ([]emailEmbeddingSource, error) {
	query := `
		SELECT
			message_id,
			thread_id,
			COALESCE(subject, ''),
			COALESCE(from_name, ''),
			COALESCE(from_email, ''),
			COALESCE(to_addresses, ''),
			COALESCE(cc_addresses, ''),
			COALESCE(bcc_addresses, ''),
			COALESCE(list_id, ''),
			COALESCE(attachment_names, ''),
			COALESCE(snippet, ''),
			COALESCE(body_text, ''),
			COALESCE(body_html, ''),
			internal_date_unix,
			has_attachments,
			is_in_trash,
			is_in_spam,
			COALESCE(sync_updated_at, '')
		FROM synced_emails
		WHERE is_deleted = 0
	`
	args := make([]any, 0)
	if len(messageIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(messageIDs)), ",")
		query += ` AND message_id IN (` + placeholders + `)`
		for _, messageID := range messageIDs {
			args = append(args, messageID)
		}
	}
	query += ` ORDER BY internal_date_unix DESC, message_id DESC`

	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sources := make([]emailEmbeddingSource, 0)
	for rows.Next() {
		var source emailEmbeddingSource
		var hasAttachments int
		var isInTrash int
		var isInSpam int
		if err := rows.Scan(
			&source.MessageID,
			&source.ThreadID,
			&source.Subject,
			&source.FromName,
			&source.FromEmail,
			&source.ToAddresses,
			&source.CcAddresses,
			&source.BccAddresses,
			&source.ListID,
			&source.AttachmentNames,
			&source.Snippet,
			&source.BodyText,
			&source.BodyHTML,
			&source.InternalDateUnix,
			&hasAttachments,
			&isInTrash,
			&isInSpam,
			&source.SyncUpdatedAt,
		); err != nil {
			return nil, err
		}
		source.HasAttachments = hasAttachments != 0
		source.IsInTrash = isInTrash != 0
		source.IsInSpam = isInSpam != 0
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func (a *App) getEmailEmbeddingSourceByMessageID(messageID string) (emailEmbeddingSource, bool, error) {
	sources, err := a.listEmailEmbeddingSources([]string{strings.TrimSpace(messageID)})
	if err != nil {
		return emailEmbeddingSource{}, false, err
	}
	if len(sources) == 0 {
		return emailEmbeddingSource{}, false, nil
	}
	return sources[0], true, nil
}

func (a *App) listActiveSyncedEmailMessageIDs() ([]string, error) {
	rows, err := a.db.Query(`
		SELECT message_id
		FROM synced_emails
		WHERE is_deleted = 0
		ORDER BY internal_date_unix DESC, message_id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messageIDs := make([]string, 0)
	for rows.Next() {
		var messageID string
		if err := rows.Scan(&messageID); err != nil {
			return nil, err
		}
		messageID = strings.TrimSpace(messageID)
		if messageID == "" {
			continue
		}
		messageIDs = append(messageIDs, messageID)
	}
	return messageIDs, rows.Err()
}

func extractEmailEmbeddingSourceIDs(sources []emailEmbeddingSource) []string {
	ids := make([]string, 0, len(sources))
	for _, source := range sources {
		if strings.TrimSpace(source.MessageID) == "" {
			continue
		}
		ids = append(ids, source.MessageID)
	}
	return ids
}

func (a *App) listExistingEmailEmbeddingStates(messageIDs []string) (map[string]emailEmbeddingExistingState, error) {
	states := make(map[string]emailEmbeddingExistingState)
	if len(messageIDs) == 0 {
		return states, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(messageIDs)), ",")
	args := make([]any, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		args = append(args, messageID)
	}
	rows, err := a.db.Query(`
		SELECT
			message_id,
			id,
			thread_id,
			embedding_text,
			COALESCE(source_fingerprint, ''),
			embedding_vector,
			COALESCE(subject, ''),
			COALESCE(from_email, ''),
			internal_date_unix,
			has_attachments,
			is_in_trash,
			is_in_spam,
			embedding_provider,
			embedding_model,
			embedding_dimensions
		FROM email_embeddings
		WHERE chunk_index = 0
			AND message_id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var messageID string
		var state emailEmbeddingExistingState
		var hasAttachments int
		var isInTrash int
		var isInSpam int
		if err := rows.Scan(
			&messageID,
			&state.EmbeddingID,
			&state.ThreadID,
			&state.EmbeddingText,
			&state.SourceFingerprint,
			&state.VectorJSON,
			&state.Subject,
			&state.FromEmail,
			&state.InternalDateUnix,
			&hasAttachments,
			&isInTrash,
			&isInSpam,
			&state.Provider,
			&state.Model,
			&state.Dimensions,
		); err != nil {
			return nil, err
		}
		state.HasAttachments = hasAttachments != 0
		state.IsInTrash = isInTrash != 0
		state.IsInSpam = isInSpam != 0
		states[messageID] = state
	}
	return states, rows.Err()
}

func (a *App) getEmailEmbeddingExistingState(messageID string) (emailEmbeddingExistingState, bool, error) {
	states, err := a.listExistingEmailEmbeddingStates([]string{strings.TrimSpace(messageID)})
	if err != nil {
		return emailEmbeddingExistingState{}, false, err
	}
	state, ok := states[strings.TrimSpace(messageID)]
	return state, ok, nil
}

func buildEmailEmbeddingDocument(source emailEmbeddingSource) emailEmbeddingDocument {
	parts := make([]string, 0, 7)
	subject := cleanWhitespace(source.Subject)
	if subject != "" {
		parts = append(parts, "Subject: "+subject)
	}

	fromPieces := make([]string, 0, 2)
	if fromName := cleanWhitespace(source.FromName); fromName != "" {
		fromPieces = append(fromPieces, fromName)
	}
	if fromEmail := strings.ToLower(cleanWhitespace(source.FromEmail)); fromEmail != "" {
		fromPieces = append(fromPieces, "<"+fromEmail+">")
	}
	fromLine := strings.TrimSpace(strings.Join(fromPieces, " "))
	if fromLine != "" {
		parts = append(parts, "From: "+fromLine)
	}

	if recipients := summarizeRecipientHeaders(source.ToAddresses, source.CcAddresses, source.BccAddresses); recipients != "" {
		parts = append(parts, "Recipients: "+recipients)
	}
	if listID := cleanWhitespace(source.ListID); listID != "" {
		parts = append(parts, "Mailing list: "+listID)
	}
	if attachments := summarizeAttachmentNames(source.AttachmentNames); attachments != "" {
		parts = append(parts, "Attachments: "+attachments)
	}
	if snippet := cleanWhitespace(source.Snippet); snippet != "" {
		parts = append(parts, "Snippet: "+snippet)
	}
	if body := cleanEmailBodyText(source.BodyText, source.BodyHTML); body != "" {
		parts = append(parts, "Body:\n"+body)
	}

	text := strings.TrimSpace(strings.Join(parts, "\n"))
	text = truncateTextPreservingBoundaries(text, emailEmbeddingMaxTextChars)

	title := subject
	if title == "" {
		title = cleanWhitespace(strings.TrimSpace(strings.Join([]string{source.FromName, source.FromEmail}, " ")))
	}

	return emailEmbeddingDocument{
		Source:            source,
		Text:              text,
		Title:             title,
		SourceFingerprint: computeEmailEmbeddingFingerprint(source, text),
	}
}

func computeEmailEmbeddingFingerprint(source emailEmbeddingSource, text string) string {
	payload, _ := json.Marshal(struct {
		ThreadID string `json:"thread_id"`
		Title    string `json:"title"`
		Text     string `json:"text"`
	}{
		ThreadID: strings.TrimSpace(source.ThreadID),
		Title:    cleanWhitespace(source.Subject),
		Text:     strings.TrimSpace(text),
	})
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum[:])
}

func summarizeRecipientHeaders(toHeader string, ccHeader string, bccHeader string) string {
	segments := make([]string, 0, 3)
	if summary := summarizeAddressHeader("to", toHeader); summary != "" {
		segments = append(segments, summary)
	}
	if summary := summarizeAddressHeader("cc", ccHeader); summary != "" {
		segments = append(segments, summary)
	}
	if summary := summarizeAddressHeader("bcc", bccHeader); summary != "" {
		segments = append(segments, summary)
	}
	return strings.Join(segments, "; ")
}

func summarizeAddressHeader(label string, raw string) string {
	addresses := parseAddressHeader(raw)
	if len(addresses) == 0 {
		return ""
	}
	preview := make([]string, 0, 3)
	for _, address := range addresses {
		if len(preview) >= 3 {
			break
		}
		name := cleanWhitespace(address.Name)
		email := strings.ToLower(cleanWhitespace(address.Address))
		switch {
		case name != "" && email != "":
			preview = append(preview, fmt.Sprintf("%s <%s>", name, email))
		case email != "":
			preview = append(preview, email)
		case name != "":
			preview = append(preview, name)
		}
	}
	summary := fmt.Sprintf("%s %s", label, strings.Join(preview, ", "))
	if remaining := len(addresses) - len(preview); remaining > 0 {
		summary += fmt.Sprintf(" +%d more", remaining)
	}
	return strings.TrimSpace(summary)
}

func parseAddressHeader(raw string) []*mail.Address {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	addresses, err := mail.ParseAddressList(trimmed)
	if err == nil && len(addresses) > 0 {
		return addresses
	}
	parts := strings.Split(trimmed, ",")
	fallback := make([]*mail.Address, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		fallback = append(fallback, &mail.Address{Address: value})
	}
	return fallback
}

func summarizeAttachmentNames(rawJSON string) string {
	names := parseJSONStringArray(rawJSON)
	if len(names) == 0 {
		return ""
	}
	preview := names
	if len(preview) > 5 {
		preview = preview[:5]
	}
	summary := strings.Join(preview, ", ")
	if remaining := len(names) - len(preview); remaining > 0 {
		summary += fmt.Sprintf(" +%d more", remaining)
	}
	return summary
}

func parseJSONStringArray(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	values := make([]string, 0)
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil
	}
	return normalizeStringSlice(values)
}

func cleanEmailBodyText(plainText string, htmlText string) string {
	candidate := normalizeEmailBodySource(plainText)
	if candidate == "" {
		candidate = htmlToPlainText(htmlText)
	}
	if candidate == "" {
		return ""
	}

	lines := strings.Split(candidate, "\n")
	cleaned := make([]string, 0, len(lines))
	consecutiveHeaders := 0
	seenLineCounts := make(map[string]int)
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(cleaned) == 0 || cleaned[len(cleaned)-1] == "" {
				continue
			}
			cleaned = append(cleaned, "")
			continue
		}
		if shouldStopForQuotedReply(trimmed, consecutiveHeaders) {
			break
		}
		if headerBlockLinePattern.MatchString(trimmed) {
			consecutiveHeaders++
		} else {
			consecutiveHeaders = 0
		}
		if shouldStopForSignature(trimmed, index, len(lines)) || shouldStopForBoilerplate(trimmed, index, len(lines)) {
			break
		}
		if looksLikeJunkLine(trimmed) {
			continue
		}
		normalizedLine := cleanWhitespace(trimmed)
		seenLineCounts[normalizedLine]++
		if seenLineCounts[normalizedLine] > 2 {
			continue
		}
		cleaned = append(cleaned, normalizedLine)
	}

	body := strings.TrimSpace(strings.Join(cleaned, "\n"))
	if body == "" {
		return ""
	}
	return truncateTextPreservingBoundaries(body, emailEmbeddingMaxBodyChars)
}

func normalizeEmailBodySource(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"\r\n", "\n",
		"\r", "\n",
		"\u00a0", " ",
		"\u200b", "",
		"\u200c", "",
		"\u200d", "",
		"\ufeff", "",
	)
	normalized := replacer.Replace(trimmed)
	return strings.TrimSpace(normalized)
}

func htmlToPlainText(source string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return ""
	}

	var builder strings.Builder
	tokenizer := xhtml.NewTokenizer(strings.NewReader(trimmed))
	skipDepth := 0
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case xhtml.ErrorToken:
			if errors.Is(tokenizer.Err(), io.EOF) {
				return normalizeEmailBodySource(builder.String())
			}
			return normalizeEmailBodySource(builder.String())
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			token := tokenizer.Token()
			switch token.DataAtom {
			case atom.Script, atom.Style, atom.Head, atom.Noscript:
				if tokenType == xhtml.StartTagToken {
					skipDepth++
				}
				continue
			case atom.Br, atom.P, atom.Div, atom.Li, atom.Tr, atom.Table, atom.Section, atom.Article, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				appendBodyNewline(&builder)
			}
		case xhtml.EndTagToken:
			token := tokenizer.Token()
			switch token.DataAtom {
			case atom.Script, atom.Style, atom.Head, atom.Noscript:
				if skipDepth > 0 {
					skipDepth--
				}
			case atom.P, atom.Div, atom.Li, atom.Tr, atom.Table, atom.Section, atom.Article, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				appendBodyNewline(&builder)
			}
		case xhtml.TextToken:
			if skipDepth > 0 {
				continue
			}
			text := cleanWhitespace(stdhtml.UnescapeString(string(tokenizer.Text())))
			if text == "" {
				continue
			}
			if builder.Len() > 0 {
				last := builder.String()[builder.Len()-1]
				if last != '\n' && last != ' ' {
					builder.WriteByte(' ')
				}
			}
			builder.WriteString(text)
		}
	}
}

func appendBodyNewline(builder *strings.Builder) {
	if builder.Len() == 0 {
		return
	}
	current := builder.String()
	if strings.HasSuffix(current, "\n\n") {
		return
	}
	if strings.HasSuffix(current, "\n") {
		builder.WriteByte('\n')
		return
	}
	builder.WriteString("\n\n")
}

func shouldStopForQuotedReply(trimmed string, consecutiveHeaders int) bool {
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(trimmed, ">"):
		return true
	case replyHeaderLinePattern.MatchString(trimmed):
		return true
	case strings.Contains(lower, "forwarded message"):
		return true
	case strings.Contains(lower, "original message"):
		return true
	case consecutiveHeaders >= 3:
		return true
	default:
		return false
	}
}

func shouldStopForSignature(trimmed string, index int, total int) bool {
	lower := strings.ToLower(trimmed)
	if index < total/3 {
		return false
	}
	switch {
	case trimmed == "--", trimmed == "--_", trimmed == "__":
		return true
	case strings.HasPrefix(lower, "sent from my "):
		return true
	case strings.HasPrefix(lower, "get outlook for "):
		return true
	default:
		return false
	}
}

func shouldStopForBoilerplate(trimmed string, index int, total int) bool {
	lower := strings.ToLower(trimmed)
	if index < total/2 {
		return false
	}
	for _, marker := range []string{
		"unsubscribe",
		"manage preferences",
		"view in browser",
		"privacy policy",
		"terms of service",
		"update your preferences",
		"mailing address",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeJunkLine(trimmed string) bool {
	if len(trimmed) > 500 && !strings.Contains(trimmed, " ") {
		return true
	}
	if strings.Count(trimmed, "=") > 20 && strings.Count(trimmed, " ") <= 2 {
		return true
	}
	if strings.HasPrefix(trimmed, "http") && len(trimmed) > 180 {
		return true
	}
	return false
}

func truncateTextPreservingBoundaries(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if limit <= 0 || trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= limit {
		return trimmed
	}

	paragraphs := strings.Split(trimmed, "\n\n")
	var builder strings.Builder
	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		candidate := paragraph
		if builder.Len() > 0 {
			candidate = "\n\n" + candidate
		}
		if len([]rune(builder.String()+candidate)) > limit {
			break
		}
		builder.WriteString(candidate)
	}
	if builder.Len() > 0 {
		return builder.String()
	}

	cut := string(runes[:limit])
	for _, separator := range []string{"\n\n", ". ", "! ", "? ", "; ", ", "} {
		if position := strings.LastIndex(cut, separator); position >= limit/2 {
			return strings.TrimSpace(cut[:position+len(separator)-1])
		}
	}
	return strings.TrimSpace(cut)
}

func (a *App) embedTexts(ctx context.Context, agent AgentDefinition, config emailEmbeddingConfig, credential string, texts []string, titles []string, taskType string) ([][]float32, error) {
	switch config.Provider {
	case "openai":
		return a.embedTextsOpenAI(ctx, agent, config, credential, texts)
	case "gemini":
		return a.embedTextsGemini(ctx, agent, config, credential, texts, titles, taskType)
	default:
		return nil, fmt.Errorf("unsupported embedding provider: %s", config.Provider)
	}
}

func (a *App) embedTextsOpenAI(ctx context.Context, agent AgentDefinition, config emailEmbeddingConfig, credential string, texts []string) ([][]float32, error) {
	requestPayload := map[string]any{
		"model":           config.Model,
		"input":           texts,
		"dimensions":      config.Dimensions,
		"encoding_format": "float",
	}
	body, err := json.Marshal(requestPayload)
	if err != nil {
		return nil, err
	}

	requestURL := strings.TrimRight(agent.BaseURL, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("openai embeddings request failed: %s", strings.TrimSpace(string(responseBody)))
	}

	var payload struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, err
	}
	sort.Slice(payload.Data, func(i int, j int) bool {
		return payload.Data[i].Index < payload.Data[j].Index
	})
	vectors := make([][]float32, 0, len(payload.Data))
	for _, item := range payload.Data {
		vectors = append(vectors, convertFloat64Slice(item.Embedding))
	}
	return vectors, nil
}

func (a *App) embedTextsGemini(ctx context.Context, agent AgentDefinition, config emailEmbeddingConfig, credential string, texts []string, titles []string, taskType string) ([][]float32, error) {
	requests := make([]map[string]any, 0, len(texts))
	for index, text := range texts {
		request := map[string]any{
			"model": "models/" + config.Model,
			"content": map[string]any{
				"parts": []map[string]any{{
					"text": text,
				}},
			},
			"taskType":             taskType,
			"outputDimensionality": config.Dimensions,
		}
		if taskType == "RETRIEVAL_DOCUMENT" && index < len(titles) && strings.TrimSpace(titles[index]) != "" {
			request["title"] = strings.TrimSpace(titles[index])
		}
		requests = append(requests, request)
	}

	requestPayload := map[string]any{"requests": requests}
	body, err := json.Marshal(requestPayload)
	if err != nil {
		return nil, err
	}

	requestURL := strings.TrimRight(agent.BaseURL, "/") + "/models/" + config.Model + ":batchEmbedContents"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if agentUsesGoogleOAuth(agent) {
		req.Header.Set("Authorization", "Bearer "+credential)
		if quotaProject := a.googleQuotaProjectHint(); quotaProject != "" {
			req.Header.Set("X-Goog-User-Project", quotaProject)
		}
	} else {
		req.Header.Set("x-goog-api-key", credential)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("gemini embeddings request failed: %s", strings.TrimSpace(string(responseBody)))
	}

	var payload struct {
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, err
	}
	vectors := make([][]float32, 0, len(payload.Embeddings))
	for _, item := range payload.Embeddings {
		vectors = append(vectors, convertFloat64Slice(item.Values))
	}
	return vectors, nil
}

func convertFloat64Slice(values []float64) []float32 {
	converted := make([]float32, 0, len(values))
	for _, value := range values {
		converted = append(converted, float32(value))
	}
	return converted
}

func isIgnorableSQLiteVecDeleteError(err error) bool {
	if err == nil {
		return false
	}
	value := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(value, "no more rows available (101)")
}

func recreateEmailEmbeddingIndex(tx *sql.Tx) error {
	statements := []string{
		`DROP TABLE IF EXISTS email_embedding_index;`,
		`CREATE VIRTUAL TABLE email_embedding_index USING vec0(
			embedding_id integer primary key,
			embedding_vector float[768] distance_metric=cosine,
			internal_date_unix integer,
			has_attachments boolean,
			is_in_trash boolean,
			is_in_spam boolean,
			from_email text,
			+message_id text,
			+thread_id text,
			+subject text,
			+embedding_text text
		);`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) upsertEmailEmbeddingBatch(documents []emailEmbeddingDocument, vectors [][]float32, config emailEmbeddingConfig) error {
	for index, document := range documents {
		if index >= len(vectors) {
			return fmt.Errorf("missing vector for email embedding document at index %d", index)
		}
		if err := a.applyEmailEmbeddingUpsert(document, vectors[index], config); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) applyEmailEmbeddingUpsert(document emailEmbeddingDocument, vector []float32, config emailEmbeddingConfig) error {
	a.emailEmbeddingDBWriteMu.Lock()
	defer a.emailEmbeddingDBWriteMu.Unlock()

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if err := upsertEmailEmbeddingRowTx(tx, document, vector, config, true); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) applyEmailEmbeddingMetadataRefresh(document emailEmbeddingDocument, existingState emailEmbeddingExistingState) error {
	a.emailEmbeddingDBWriteMu.Lock()
	defer a.emailEmbeddingDBWriteMu.Unlock()

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.Exec(`
		UPDATE email_embeddings
		SET
			thread_id = ?,
			embedding_text = ?,
			subject = ?,
			from_email = ?,
			internal_date_unix = ?,
			has_attachments = ?,
			is_in_trash = ?,
			is_in_spam = ?,
			source_sync_updated_at = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`,
		document.Source.ThreadID,
		document.Text,
		nullIfEmpty(document.Source.Subject),
		nullIfEmpty(strings.ToLower(document.Source.FromEmail)),
		document.Source.InternalDateUnix,
		boolToInt(document.Source.HasAttachments),
		boolToInt(document.Source.IsInTrash),
		boolToInt(document.Source.IsInSpam),
		nullIfEmpty(document.Source.SyncUpdatedAt),
		existingState.EmbeddingID,
	); err != nil {
		return err
	}
	if err := replaceEmailEmbeddingIndexRowTx(tx, existingState.EmbeddingID, existingState.VectorJSON, document); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertEmailEmbeddingRowTx(tx *sql.Tx, document emailEmbeddingDocument, vector []float32, config emailEmbeddingConfig, refreshIndexedAt bool) error {
	vectorJSONBytes, err := json.Marshal(vector)
	if err != nil {
		return err
	}
	vectorJSON := string(vectorJSONBytes)
	if _, err := tx.Exec(`
		INSERT INTO email_embeddings (
			message_id,
			thread_id,
			chunk_index,
			embedding_text,
			source_fingerprint,
			embedding_vector,
			subject,
			from_email,
			internal_date_unix,
			has_attachments,
			is_in_trash,
			is_in_spam,
			embedding_provider,
			embedding_model,
			embedding_dimensions,
			source_sync_updated_at,
			indexed_at,
			updated_at
		) VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(message_id, chunk_index) DO UPDATE SET
			thread_id = excluded.thread_id,
			embedding_text = excluded.embedding_text,
			source_fingerprint = excluded.source_fingerprint,
			embedding_vector = excluded.embedding_vector,
			subject = excluded.subject,
			from_email = excluded.from_email,
			internal_date_unix = excluded.internal_date_unix,
			has_attachments = excluded.has_attachments,
			is_in_trash = excluded.is_in_trash,
			is_in_spam = excluded.is_in_spam,
			embedding_provider = excluded.embedding_provider,
			embedding_model = excluded.embedding_model,
			embedding_dimensions = excluded.embedding_dimensions,
			source_sync_updated_at = excluded.source_sync_updated_at,
			indexed_at = CASE WHEN ? THEN CURRENT_TIMESTAMP ELSE indexed_at END,
			updated_at = CURRENT_TIMESTAMP
	`,
		document.Source.MessageID,
		document.Source.ThreadID,
		document.Text,
		document.SourceFingerprint,
		vectorJSON,
		nullIfEmpty(document.Source.Subject),
		nullIfEmpty(strings.ToLower(document.Source.FromEmail)),
		document.Source.InternalDateUnix,
		boolToInt(document.Source.HasAttachments),
		boolToInt(document.Source.IsInTrash),
		boolToInt(document.Source.IsInSpam),
		config.Provider,
		config.Model,
		config.Dimensions,
		nullIfEmpty(document.Source.SyncUpdatedAt),
		refreshIndexedAt,
	); err != nil {
		return err
	}

	var embeddingID int64
	var storedVectorJSON string
	if err := tx.QueryRow(`
		SELECT id, embedding_vector
		FROM email_embeddings
		WHERE message_id = ? AND chunk_index = 0
	`,
		document.Source.MessageID,
	).Scan(&embeddingID, &storedVectorJSON); err != nil {
		return err
	}
	return replaceEmailEmbeddingIndexRowTx(tx, embeddingID, storedVectorJSON, document)
}

func replaceEmailEmbeddingIndexRowTx(tx *sql.Tx, embeddingID int64, vectorJSON string, document emailEmbeddingDocument) error {
	if _, err := tx.Exec(`
		DELETE FROM email_embedding_index
		WHERE embedding_id = ?
	`, embeddingID); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		INSERT INTO email_embedding_index (
			embedding_id,
			embedding_vector,
			internal_date_unix,
			has_attachments,
			is_in_trash,
			is_in_spam,
			from_email,
			message_id,
			thread_id,
			subject,
			embedding_text
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		embeddingID,
		vectorJSON,
		document.Source.InternalDateUnix,
		boolToInt(document.Source.HasAttachments),
		boolToInt(document.Source.IsInTrash),
		boolToInt(document.Source.IsInSpam),
		strings.ToLower(strings.TrimSpace(document.Source.FromEmail)),
		document.Source.MessageID,
		document.Source.ThreadID,
		strings.TrimSpace(document.Source.Subject),
		document.Text,
	); err != nil {
		return err
	}
	return nil
}

func deleteEmailEmbeddingByMessageTx(tx *sql.Tx, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil
	}
	if _, err := tx.Exec(`
		DELETE FROM email_embedding_index
		WHERE embedding_id IN (
			SELECT id
			FROM email_embeddings
			WHERE message_id = ?
		)
	`, messageID); err != nil && !isIgnorableSQLiteVecDeleteError(err) {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM email_embeddings WHERE message_id = ?`, messageID); err != nil {
		return err
	}
	return nil
}

func (a *App) deleteEmailEmbeddingByMessageID(messageID string) error {
	a.emailEmbeddingDBWriteMu.Lock()
	defer a.emailEmbeddingDBWriteMu.Unlock()

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := deleteEmailEmbeddingByMessageTx(tx, messageID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) deleteEmailEmbeddingsByMessageIDs(messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(messageIDs)), ",")
	args := make([]any, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		args = append(args, messageID)
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	rows, err := tx.Query(`SELECT message_id FROM email_embeddings WHERE message_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	idsToDelete := make([]string, 0, len(messageIDs))
	for rows.Next() {
		var messageID string
		if err := rows.Scan(&messageID); err != nil {
			return err
		}
		idsToDelete = append(idsToDelete, messageID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, messageID := range idsToDelete {
		if err := deleteEmailEmbeddingByMessageTx(tx, messageID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) clearEmailEmbeddings() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := recreateEmailEmbeddingIndex(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM email_embeddings`); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) cleanupOrphanedEmailEmbeddingIndex() error {
	return a.rebuildEmailEmbeddingIndex()
}

func (a *App) rebuildEmailEmbeddingIndex() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := recreateEmailEmbeddingIndex(tx); err != nil {
		return err
	}
	rows, err := tx.Query(`
		SELECT
			id,
			embedding_vector,
			internal_date_unix,
			has_attachments,
			is_in_trash,
			is_in_spam,
			COALESCE(from_email, ''),
			message_id,
			thread_id,
			COALESCE(subject, ''),
			embedding_text
		FROM email_embeddings
		ORDER BY id
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var embeddingID int64
		var vectorJSON string
		var internalDateUnix int64
		var hasAttachments int
		var isInTrash int
		var isInSpam int
		var fromEmail string
		var messageID string
		var threadID string
		var subject string
		var embeddingText string
		if err := rows.Scan(
			&embeddingID,
			&vectorJSON,
			&internalDateUnix,
			&hasAttachments,
			&isInTrash,
			&isInSpam,
			&fromEmail,
			&messageID,
			&threadID,
			&subject,
			&embeddingText,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO email_embedding_index (
				embedding_id,
				embedding_vector,
				internal_date_unix,
				has_attachments,
				is_in_trash,
				is_in_spam,
				from_email,
				message_id,
				thread_id,
				subject,
				embedding_text
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			embeddingID,
			vectorJSON,
			internalDateUnix,
			hasAttachments,
			isInTrash,
			isInSpam,
			strings.ToLower(strings.TrimSpace(fromEmail)),
			messageID,
			threadID,
			strings.TrimSpace(subject),
			embeddingText,
		); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) getEmailEmbeddingStats() (emailEmbeddingStats, error) {
	row := a.db.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE((
				SELECT embedding_provider
				FROM email_embeddings
				ORDER BY indexed_at DESC, id DESC
				LIMIT 1
			), ''),
			COALESCE((
				SELECT embedding_model
				FROM email_embeddings
				ORDER BY indexed_at DESC, id DESC
				LIMIT 1
			), '')
		FROM email_embeddings
	`)
	var stats emailEmbeddingStats
	if err := row.Scan(&stats.Count, &stats.Provider, &stats.Model); err != nil {
		return emailEmbeddingStats{}, err
	}
	return stats, nil
}

func (a *App) buildSemanticEmailSearchToolDefinition() (GmailToolDefinition, bool) {
	_, config, _, supported, err := a.resolveCurrentEmailEmbeddingConfig()
	if err != nil || !supported {
		return GmailToolDefinition{}, false
	}
	stats, statsErr := a.getEmailEmbeddingStats()
	indexSummary := fmt.Sprintf("embedding_provider=%s; embedding_model=%s; dimensions=%d; indexed_rows=%d", config.Provider, config.Model, config.Dimensions, stats.Count)
	if statsErr != nil {
		indexSummary = fmt.Sprintf("embedding_provider=%s; embedding_model=%s; dimensions=%d; indexed_rows=unknown", config.Provider, config.Model, config.Dimensions)
	}

	return GmailToolDefinition{
		Name:         "semantic_email_search",
		GmailActions: []string{"sqlite.vec.search"},
		SafetyModel:  "read_only",
		Description: strings.Join([]string{
			"Run semantic search over the local Gmail cache using provider-matched text embeddings instead of exact SQL predicates.",
			"Use this for concept search, topic recall, fuzzy wording, or when the user describes an email without exact senders, dates, or subject text.",
			"Use query_db for exact counts, deterministic filtering, or aggregation after you identify candidate emails semantically.",
			"Current embedding index context: " + indexSummary + ".",
		}, " "),
		Parameters: gmailObjectSchema(
			"Parameters for `semantic_email_search`. Keep filters narrow and use query_db when you need exact SQL logic or totals.",
			map[string]any{
				"query": gmailStringSchema(gmailDescription(
					"Natural-language semantic search query describing the topic, request, or gist of the email you want to find.",
					`{"query":"vendor emails about renewing the analytics contract"}`,
					`{"query":"messages mentioning travel reimbursement policy changes"}`,
				)),
				"top_k": gmailIntegerSchema(gmailDescription(
					"Maximum number of semantic matches to return. Use small values for focused grounding and larger values only when the user explicitly wants a broader recall set.",
					`{"top_k":5}`,
					`{"top_k":12}`,
				)),
				"from_email": gmailStringSchema(gmailDescription(
					"Optional exact sender filter applied after semantic matching. Use the normalized sender email address when you know it.",
					`{"from_email":"alerts@example.com"}`,
					`{"from_email":"billing@vendor.com"}`,
				)),
				"after_unix": gmailIntegerSchema(gmailDescription(
					"Optional lower bound on internal Gmail message time in Unix milliseconds.",
					`{"after_unix":1735689600000}`,
					`{"after_unix":1743465600000}`,
				)),
				"before_unix": gmailIntegerSchema(gmailDescription(
					"Optional upper bound on internal Gmail message time in Unix milliseconds.",
					`{"before_unix":1746057600000}`,
					`{"before_unix":1748736000000}`,
				)),
				"has_attachments": gmailBooleanSchema(gmailDescription(
					"Optional exact attachment filter. Use when the user explicitly mentions attachments or files.",
					`{"has_attachments":true}`,
					`{"has_attachments":false}`,
				)),
				"include_spam": gmailBooleanSchema(gmailDescription(
					"Whether semantic search may return Spam messages. Default is false and should stay false unless the user explicitly wants Spam searched.",
					`{"include_spam":true}`,
					`{"include_spam":false}`,
				)),
				"include_trash": gmailBooleanSchema(gmailDescription(
					"Whether semantic search may return Trash messages. Default is false and should stay false unless the user explicitly wants Trash searched.",
					`{"include_trash":true}`,
					`{"include_trash":false}`,
				)),
			},
			"query",
		),
	}, true
}

func (a *App) executeSemanticEmailSearchTool(arguments map[string]any) (string, error) {
	query, _ := arguments["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	topK := int64Argument(arguments["top_k"], 8)
	if topK < 1 {
		topK = 1
	}
	if topK > 20 {
		topK = 20
	}

	options := semanticEmailSearchOptions{
		Query:        query,
		TopK:         topK,
		FromEmail:    strings.ToLower(strings.TrimSpace(stringArgument(arguments["from_email"]))),
		IncludeSpam:  boolArgument(arguments["include_spam"]),
		IncludeTrash: boolArgument(arguments["include_trash"]),
	}
	if afterUnix, ok := optionalInt64Argument(arguments["after_unix"]); ok {
		options.AfterUnix = &afterUnix
	}
	if beforeUnix, ok := optionalInt64Argument(arguments["before_unix"]); ok {
		options.BeforeUnix = &beforeUnix
	}
	if hasAttachments, ok := optionalBoolArgument(arguments["has_attachments"]); ok {
		options.HasAttachments = &hasAttachments
	}

	payload, err := a.searchEmailEmbeddings(options)
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func (a *App) searchEmailEmbeddings(options semanticEmailSearchOptions) (map[string]any, error) {
	agent, config, credential, supported, err := a.resolveCurrentEmailEmbeddingConfig()
	if err != nil {
		return nil, err
	}
	if !supported {
		return nil, errors.New("semantic email search is unavailable for the selected agent provider")
	}

	queryVectors, err := a.embedTexts(context.Background(), agent, config, credential, []string{options.Query}, []string{""}, "RETRIEVAL_QUERY")
	if err != nil {
		return nil, err
	}
	if len(queryVectors) != 1 {
		return nil, errors.New("embedding provider did not return exactly one query vector")
	}
	queryVectorJSON, err := json.Marshal(queryVectors[0])
	if err != nil {
		return nil, err
	}

	sqlBuilder := strings.Builder{}
	sqlBuilder.WriteString(`
		SELECT
			embedding_id,
			distance,
			message_id,
			thread_id,
			subject,
			from_email,
			internal_date_unix,
			has_attachments,
			is_in_trash,
			is_in_spam,
			embedding_text
		FROM email_embedding_index
		WHERE embedding_vector MATCH ?
			AND k = ?
	`)
	args := []any{string(queryVectorJSON), options.TopK}
	if !options.IncludeSpam {
		sqlBuilder.WriteString(` AND is_in_spam = 0`)
	}
	if !options.IncludeTrash {
		sqlBuilder.WriteString(` AND is_in_trash = 0`)
	}
	if options.FromEmail != "" {
		sqlBuilder.WriteString(` AND from_email = ?`)
		args = append(args, options.FromEmail)
	}
	if options.AfterUnix != nil {
		sqlBuilder.WriteString(` AND internal_date_unix >= ?`)
		args = append(args, *options.AfterUnix)
	}
	if options.BeforeUnix != nil {
		sqlBuilder.WriteString(` AND internal_date_unix <= ?`)
		args = append(args, *options.BeforeUnix)
	}
	if options.HasAttachments != nil {
		sqlBuilder.WriteString(` AND has_attachments = ?`)
		args = append(args, boolToInt(*options.HasAttachments))
	}
	sqlBuilder.WriteString(` ORDER BY distance ASC`)

	rows, err := a.db.Query(sqlBuilder.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]map[string]any, 0)
	for rows.Next() {
		var embeddingID int64
		var distance float64
		var messageID string
		var threadID string
		var subject sql.NullString
		var fromEmail sql.NullString
		var internalDateUnix int64
		var hasAttachments int
		var isInTrash int
		var isInSpam int
		var embeddingText sql.NullString
		if err := rows.Scan(
			&embeddingID,
			&distance,
			&messageID,
			&threadID,
			&subject,
			&fromEmail,
			&internalDateUnix,
			&hasAttachments,
			&isInTrash,
			&isInSpam,
			&embeddingText,
		); err != nil {
			return nil, err
		}
		results = append(results, map[string]any{
			"embedding_id":       embeddingID,
			"distance":           distance,
			"message_id":         messageID,
			"thread_id":          threadID,
			"subject":            subject.String,
			"from_email":         fromEmail.String,
			"internal_date_unix": internalDateUnix,
			"internal_date":      unixMillisToRFC3339(internalDateUnix),
			"has_attachments":    hasAttachments != 0,
			"is_in_trash":        isInTrash != 0,
			"is_in_spam":         isInSpam != 0,
			"embedding_text":     truncateTextPreservingBoundaries(embeddingText.String, 1200),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	status, err := a.getEmailSyncStatus()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":                 true,
		"tool":               "semantic_email_search",
		"query":              options.Query,
		"top_k":              options.TopK,
		"embedding_provider": config.Provider,
		"embedding_model":    config.Model,
		"results":            results,
		"result_count":       len(results),
		"sync":               status,
	}, nil
}

func stringArgument(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func boolArgument(value any) bool {
	parsed, ok := optionalBoolArgument(value)
	return ok && parsed
}

func optionalBoolArgument(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	default:
		return false, false
	}
}

func int64Argument(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	default:
		return fallback
	}
}

func optionalInt64Argument(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	default:
		return 0, false
	}
}
