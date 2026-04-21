package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	defaultEmailSyncWindowDays = 365
	emailSyncInterval          = 30 * time.Second
	emailSyncFreshnessMaxAge   = 2 * time.Minute
)

var errEmailHistoryExpired = errors.New("gmail history cursor expired")

type EmailSyncStatus struct {
	MailboxEmail                  string `json:"mailbox_email"`
	SyncStatus                    string `json:"sync_status"`
	SyncMode                      string `json:"sync_mode,omitempty"`
	LastSyncReason                string `json:"last_sync_reason,omitempty"`
	LastSyncStartedAt             string `json:"last_sync_started_at,omitempty"`
	LastSyncCompletedAt           string `json:"last_sync_completed_at,omitempty"`
	LastSuccessfulSyncAt          string `json:"last_successful_sync_at,omitempty"`
	LastFullSyncCompletedAt       string `json:"last_full_sync_completed_at,omitempty"`
	LastPartialSyncCompletedAt    string `json:"last_partial_sync_completed_at,omitempty"`
	SyncCursorHistoryID           string `json:"sync_cursor_history_id,omitempty"`
	LastHistoryID                 string `json:"last_history_id,omitempty"`
	LastSyncError                 string `json:"last_sync_error,omitempty"`
	SyncedWindowDays              int    `json:"synced_window_days"`
	MessageCount                  int    `json:"message_count"`
	EmbeddingCount                int    `json:"embedding_count"`
	EmbeddingProvider             string `json:"embedding_provider,omitempty"`
	EmbeddingModel                string `json:"embedding_model,omitempty"`
	OldestMessageAt               string `json:"oldest_message_at,omitempty"`
	NewestMessageAt               string `json:"newest_message_at,omitempty"`
	OldestMessageInternalDateUnix int64  `json:"oldest_message_internal_date_unix,omitempty"`
	NewestMessageInternalDateUnix int64  `json:"newest_message_internal_date_unix,omitempty"`
}

type emailSyncResult struct {
	StartHistoryID     string
	CursorHistoryID    string
	LastHistoryID      string
	SyncedWindowDays   int
	Metrics            emailSyncMetrics
	UpsertedMessageIDs []string
	DeletedMessageIDs  []string
}

type emailSyncMetrics struct {
	MessageCount                  int
	OldestMessageInternalDateUnix int64
	NewestMessageInternalDateUnix int64
	OldestMessageAt               string
	NewestMessageAt               string
}

func (a *App) startEmailSyncLoop() {
	go func() {
		ticker := time.NewTicker(emailSyncInterval)
		defer ticker.Stop()
		for range ticker.C {
			if err := a.runEmailSync("partial", "background_interval"); err != nil && !errors.Is(err, errEmailSyncAlreadyRunning) {
				log.Printf("email sync background tick failed: %v", err)
			}
		}
	}()
}

func (a *App) syncEmailsInBackground(mode string, reason string) {
	go func() {
		if err := a.runEmailSync(mode, reason); err != nil && !errors.Is(err, errEmailSyncAlreadyRunning) {
			log.Printf("email sync failed: mode=%s reason=%s err=%v", mode, reason, err)
		}
	}()
}

func (a *App) maybeSyncEmailsBeforeChat(userMessage string) {
	if !requestNeedsFreshMailboxData(userMessage) {
		return
	}
	status, err := a.getEmailSyncStatus()
	if err != nil {
		log.Printf("email sync status check failed before chat: %v", err)
		return
	}
	if status.SyncStatus == "running" {
		return
	}
	if strings.TrimSpace(status.LastSuccessfulSyncAt) == "" {
		if err := a.runEmailSync("partial", "before_chat_freshness"); err != nil && !errors.Is(err, errEmailSyncAlreadyRunning) {
			log.Printf("email sync before chat failed: %v", err)
		}
		return
	}
	lastSuccess, err := time.Parse(time.RFC3339, status.LastSuccessfulSyncAt)
	if err != nil || time.Since(lastSuccess) > emailSyncFreshnessMaxAge {
		if err := a.runEmailSync("partial", "before_chat_freshness"); err != nil && !errors.Is(err, errEmailSyncAlreadyRunning) {
			log.Printf("email sync before chat failed: %v", err)
		}
	}
}

func requestNeedsFreshMailboxData(userMessage string) bool {
	value := strings.ToLower(strings.TrimSpace(userMessage))
	if value == "" {
		return false
	}
	keywords := []string{
		"latest",
		"recent",
		"new email",
		"new emails",
		"today",
		"just now",
		"right now",
		"currently",
		"current",
		"fresh",
		"newest",
		"unread",
		"since this morning",
		"since yesterday",
	}
	for _, keyword := range keywords {
		if strings.Contains(value, keyword) {
			return true
		}
	}
	return false
}

var errEmailSyncAlreadyRunning = errors.New("email sync already running")

func (a *App) runEmailSync(mode string, reason string) error {
	a.emailSyncMu.Lock()
	if a.emailSyncRunning {
		a.emailSyncMu.Unlock()
		log.Printf("email sync skipped: mode=%s reason=%s err=%v", mode, reason, errEmailSyncAlreadyRunning)
		return errEmailSyncAlreadyRunning
	}
	a.emailSyncRunning = true
	a.emailSyncMu.Unlock()
	defer func() {
		a.emailSyncMu.Lock()
		a.emailSyncRunning = false
		a.emailSyncMu.Unlock()
	}()

	state, err := a.getSetupState()
	if err != nil {
		return err
	}
	if !state.GoogleAccountConnected || state.GoogleEmail == nil || strings.TrimSpace(*state.GoogleEmail) == "" {
		log.Printf("email sync skipped: mode=%s reason=%s mailbox not connected", mode, reason)
		return nil
	}
	mailboxEmail := strings.TrimSpace(*state.GoogleEmail)
	log.Printf("email sync starting: mode=%s reason=%s mailbox=%s", mode, reason, mailboxEmail)
	if err := a.markEmailSyncStarted(mode, reason, mailboxEmail); err != nil {
		return err
	}

	var result emailSyncResult
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "partial":
		status, statusErr := a.getEmailSyncStatus()
		if statusErr != nil {
			_ = a.markEmailSyncFailed("partial", statusErr)
			return statusErr
		}
		cursor := strings.TrimSpace(status.SyncCursorHistoryID)
		if cursor == "" {
			log.Printf("email sync warning: partial requested without history cursor; escalating to full sync: reason=%s mailbox=%s", reason, mailboxEmail)
			result, err = a.performFullEmailSync()
			mode = "full"
		} else {
			result, err = a.performPartialEmailSync(cursor)
			if errors.Is(err, errEmailHistoryExpired) {
				log.Printf("email sync warning: partial history cursor expired; retrying full sync: reason=%s mailbox=%s cursor=%s", reason, mailboxEmail, cursor)
				result, err = a.performFullEmailSync()
				mode = "full"
			}
		}
	case "full":
		result, err = a.performFullEmailSync()
	case "window_update":
		result, err = a.performWindowUpdateEmailSync()
	default:
		err = fmt.Errorf("unsupported email sync mode: %s", mode)
	}
	if err != nil {
		log.Printf("email sync failed before completion: mode=%s reason=%s mailbox=%s err=%v", mode, reason, mailboxEmail, err)
		_ = a.markEmailSyncFailed(mode, err)
		return err
	}
	if err := a.markEmailSyncSucceeded(mode, reason, mailboxEmail, result); err != nil {
		return err
	}
	if err := a.syncEmailEmbeddingsForSyncResult("email_sync_"+mode, result); err != nil && !errors.Is(err, errEmailEmbeddingAlreadyRunning) {
		log.Printf("email sync warning: embedding refresh failed after sync: mode=%s reason=%s mailbox=%s err=%v", mode, reason, mailboxEmail, err)
	}
	a.dispatchScheduledTaskRunsInBackground(mode, reason, result)
	log.Printf("email sync completed: mode=%s reason=%s mailbox=%s messages=%d oldest=%s newest=%s cursor=%s", mode, reason, mailboxEmail, result.Metrics.MessageCount, result.Metrics.OldestMessageAt, result.Metrics.NewestMessageAt, result.CursorHistoryID)
	return nil
}

func (a *App) performFullEmailSync() (emailSyncResult, error) {
	if err := a.fetchAndCacheGmailLabels(); err != nil {
		log.Printf("email sync warning: failed to refresh gmail labels during full sync: %v", err)
	}
	windowDays, err := a.getEmailSyncWindowDays()
	if err != nil {
		return emailSyncResult{}, err
	}
	messageIDs, cursorHistoryID, err := a.syncRecentMessagesWindow(windowDays)
	if err != nil {
		return emailSyncResult{}, err
	}
	deletedMessageIDs, err := a.reconcileSyncedEmailsWithinWindow(messageIDs, emailSyncCutoffUnixMillis(windowDays))
	if err != nil {
		return emailSyncResult{}, err
	}
	metrics, err := a.getEmailSyncMetrics()
	if err != nil {
		return emailSyncResult{}, err
	}
	return emailSyncResult{
		StartHistoryID:     "",
		CursorHistoryID:    cursorHistoryID,
		LastHistoryID:      cursorHistoryID,
		SyncedWindowDays:   windowDays,
		Metrics:            metrics,
		UpsertedMessageIDs: append([]string(nil), messageIDs...),
		DeletedMessageIDs:  append([]string(nil), deletedMessageIDs...),
	}, nil
}

func (a *App) performPartialEmailSync(cursor string) (emailSyncResult, error) {
	if strings.TrimSpace(cursor) == "" {
		return a.performFullEmailSync()
	}
	changes, lastHistoryID, err := a.listGmailHistoryChanges(cursor)
	if err != nil {
		return emailSyncResult{}, err
	}
	pendingEmbeddingMessageIDs := make([]string, 0, emailEmbeddingBatchSize)
	flushPendingEmbeddingMessages := func() {
		if len(pendingEmbeddingMessageIDs) == 0 {
			return
		}
		a.refreshEmailEmbeddingsForMessageIDsInBackground("email_sync_partial_incremental", pendingEmbeddingMessageIDs, nil)
		pendingEmbeddingMessageIDs = pendingEmbeddingMessageIDs[:0]
	}
	upsertIDs := changes.UpsertIDs()
	for _, messageID := range upsertIDs {
		message, fetchErr := a.fetchGmailMessage(messageID)
		if fetchErr != nil {
			if errors.Is(fetchErr, errGmailMessageNotFound) {
				log.Printf("email sync warning: changed message missing during partial refetch; marking deleted: message_id=%s", messageID)
				if err := a.markSyncedEmailDeleted(messageID); err != nil {
					return emailSyncResult{}, err
				}
				changes.Deleted[messageID] = struct{}{}
				continue
			}
			return emailSyncResult{}, fetchErr
		}
		if _, upsertErr := a.upsertSyncedEmail(message); upsertErr != nil {
			return emailSyncResult{}, upsertErr
		}
		delete(changes.Deleted, messageID)
		pendingEmbeddingMessageIDs = append(pendingEmbeddingMessageIDs, messageID)
		if len(pendingEmbeddingMessageIDs) >= emailEmbeddingBatchSize {
			flushPendingEmbeddingMessages()
		}
	}
	flushPendingEmbeddingMessages()
	for _, messageID := range changes.DeletedIDs() {
		if err := a.markSyncedEmailDeleted(messageID); err != nil {
			return emailSyncResult{}, err
		}
	}
	metrics, err := a.getEmailSyncMetrics()
	if err != nil {
		return emailSyncResult{}, err
	}
	if strings.TrimSpace(lastHistoryID) == "" {
		lastHistoryID = strings.TrimSpace(cursor)
	}
	return emailSyncResult{
		StartHistoryID:     strings.TrimSpace(cursor),
		CursorHistoryID:    lastHistoryID,
		LastHistoryID:      lastHistoryID,
		SyncedWindowDays:   0,
		Metrics:            metrics,
		UpsertedMessageIDs: upsertIDs,
		DeletedMessageIDs:  changes.DeletedIDs(),
	}, nil
}

func (a *App) performWindowUpdateEmailSync() (emailSyncResult, error) {
	if err := a.fetchAndCacheGmailLabels(); err != nil {
		log.Printf("email sync warning: failed to refresh gmail labels during window update sync: %v", err)
	}
	status, err := a.getEmailSyncStatus()
	if err != nil {
		return emailSyncResult{}, err
	}
	windowDays := normalizeEmailSyncWindowDays(status.SyncedWindowDays)
	if strings.TrimSpace(status.SyncCursorHistoryID) == "" {
		return a.performFullEmailSync()
	}
	partialResult, err := a.performPartialEmailSync(status.SyncCursorHistoryID)
	if err != nil {
		if errors.Is(err, errEmailHistoryExpired) {
			return a.performFullEmailSync()
		}
		return emailSyncResult{}, err
	}
	backfillResult, err := a.backfillConfiguredEmailSyncWindow(windowDays)
	if err != nil {
		return emailSyncResult{}, err
	}
	merged := mergeEmailSyncResults(partialResult, backfillResult)
	merged.SyncedWindowDays = windowDays
	if strings.TrimSpace(merged.CursorHistoryID) == "" {
		merged.CursorHistoryID = strings.TrimSpace(status.SyncCursorHistoryID)
	}
	if strings.TrimSpace(merged.LastHistoryID) == "" {
		merged.LastHistoryID = strings.TrimSpace(status.LastHistoryID)
	}
	return merged, nil
}

func (a *App) backfillConfiguredEmailSyncWindow(windowDays int) (emailSyncResult, error) {
	windowDays = normalizeEmailSyncWindowDays(windowDays)
	metrics, err := a.getEmailSyncMetrics()
	if err != nil {
		return emailSyncResult{}, err
	}
	if metrics.MessageCount == 0 || metrics.OldestMessageInternalDateUnix <= 0 {
		return a.performFullEmailSync()
	}
	cutoffUnixMillis := emailSyncCutoffUnixMillis(windowDays)
	if metrics.OldestMessageInternalDateUnix <= cutoffUnixMillis {
		return emailSyncResult{
			SyncedWindowDays: windowDays,
			Metrics:          metrics,
		}, nil
	}
	messageIDs, err := a.syncOlderMessagesBeforeDate(metrics.OldestMessageInternalDateUnix, cutoffUnixMillis)
	if err != nil {
		return emailSyncResult{}, err
	}
	updatedMetrics, err := a.getEmailSyncMetrics()
	if err != nil {
		return emailSyncResult{}, err
	}
	return emailSyncResult{
		SyncedWindowDays:   windowDays,
		Metrics:            updatedMetrics,
		UpsertedMessageIDs: append([]string(nil), messageIDs...),
		DeletedMessageIDs:  nil,
	}, nil
}

func planEmailSyncWindowUpdate(status EmailSyncStatus, windowDays int) string {
	windowDays = normalizeEmailSyncWindowDays(windowDays)
	if strings.TrimSpace(status.SyncCursorHistoryID) == "" || status.MessageCount == 0 || status.OldestMessageInternalDateUnix <= 0 {
		return "initial_sync_started"
	}
	if status.OldestMessageInternalDateUnix > emailSyncCutoffUnixMillis(windowDays) {
		return "backfill_started"
	}
	return "no_sync_needed"
}

func normalizeEmailSyncWindowDays(windowDays int) int {
	if windowDays <= 0 {
		return defaultEmailSyncWindowDays
	}
	return windowDays
}

func emailSyncCutoffUnixMillis(windowDays int) int64 {
	windowDays = normalizeEmailSyncWindowDays(windowDays)
	return time.Now().Add(-time.Duration(windowDays) * 24 * time.Hour).UnixMilli()
}

func mergeEmailSyncResults(primary emailSyncResult, secondary emailSyncResult) emailSyncResult {
	merged := primary
	merged.StartHistoryID = firstNonEmptyString(primary.StartHistoryID, secondary.StartHistoryID)
	merged.CursorHistoryID = firstNonEmptyString(secondary.CursorHistoryID, primary.CursorHistoryID)
	merged.LastHistoryID = firstNonEmptyString(secondary.LastHistoryID, primary.LastHistoryID)
	merged.SyncedWindowDays = normalizeEmailSyncWindowDays(firstNonZeroInt(secondary.SyncedWindowDays, primary.SyncedWindowDays))
	merged.Metrics = secondary.Metrics
	merged.UpsertedMessageIDs = appendUniqueStringIDs(primary.UpsertedMessageIDs, secondary.UpsertedMessageIDs)
	merged.DeletedMessageIDs = appendUniqueStringIDs(primary.DeletedMessageIDs, secondary.DeletedMessageIDs)
	return merged
}

func appendUniqueStringIDs(groups ...[]string) []string {
	seen := make(map[string]struct{})
	merged := make([]string, 0)
	for _, group := range groups {
		for _, item := range group {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			merged = append(merged, item)
		}
	}
	return merged
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (a *App) getEmailSyncStatus() (EmailSyncStatus, error) {
	row := a.db.QueryRow(`SELECT mailbox_email, sync_status, sync_mode, last_sync_reason, last_sync_started_at, last_sync_completed_at, last_successful_sync_at, last_full_sync_completed_at, last_partial_sync_completed_at, sync_cursor_history_id, last_history_id, last_sync_error, synced_window_days FROM email_sync_state WHERE id = 1`)
	var status EmailSyncStatus
	var mailboxEmail sql.NullString
	var syncStatus sql.NullString
	var syncMode sql.NullString
	var lastSyncReason sql.NullString
	var lastSyncStartedAt sql.NullString
	var lastSyncCompletedAt sql.NullString
	var lastSuccessfulSyncAt sql.NullString
	var lastFullSyncCompletedAt sql.NullString
	var lastPartialSyncCompletedAt sql.NullString
	var syncCursorHistoryID sql.NullString
	var lastHistoryID sql.NullString
	var lastSyncError sql.NullString
	if err := row.Scan(&mailboxEmail, &syncStatus, &syncMode, &lastSyncReason, &lastSyncStartedAt, &lastSyncCompletedAt, &lastSuccessfulSyncAt, &lastFullSyncCompletedAt, &lastPartialSyncCompletedAt, &syncCursorHistoryID, &lastHistoryID, &lastSyncError, &status.SyncedWindowDays); err != nil {
		return EmailSyncStatus{}, err
	}
	status.MailboxEmail = mailboxEmail.String
	status.SyncStatus = defaultString(syncStatus.String, "idle")
	status.SyncMode = syncMode.String
	status.LastSyncReason = lastSyncReason.String
	status.LastSyncStartedAt = lastSyncStartedAt.String
	status.LastSyncCompletedAt = lastSyncCompletedAt.String
	status.LastSuccessfulSyncAt = lastSuccessfulSyncAt.String
	status.LastFullSyncCompletedAt = lastFullSyncCompletedAt.String
	status.LastPartialSyncCompletedAt = lastPartialSyncCompletedAt.String
	status.SyncCursorHistoryID = syncCursorHistoryID.String
	status.LastHistoryID = lastHistoryID.String
	status.LastSyncError = lastSyncError.String
	status.SyncedWindowDays = normalizeEmailSyncWindowDays(status.SyncedWindowDays)
	metrics, err := a.getEmailSyncMetrics()
	if err != nil {
		return EmailSyncStatus{}, err
	}
	status.MessageCount = metrics.MessageCount
	status.OldestMessageAt = metrics.OldestMessageAt
	status.NewestMessageAt = metrics.NewestMessageAt
	status.OldestMessageInternalDateUnix = metrics.OldestMessageInternalDateUnix
	status.NewestMessageInternalDateUnix = metrics.NewestMessageInternalDateUnix
	embeddingStats, err := a.getEmailEmbeddingStats()
	if err != nil {
		return EmailSyncStatus{}, err
	}
	status.EmbeddingCount = embeddingStats.Count
	status.EmbeddingProvider = embeddingStats.Provider
	status.EmbeddingModel = embeddingStats.Model
	return status, nil
}

func (a *App) resetEmailSyncStore() error {
	a.emailSyncMu.Lock()
	defer a.emailSyncMu.Unlock()
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
	stmts := []string{
		`DELETE FROM email_embeddings;`,
		`DELETE FROM synced_email_labels;`,
		`DELETE FROM synced_email_attachments;`,
		`DELETE FROM synced_emails;`,
		`UPDATE email_sync_state SET mailbox_email = NULL, sync_status = 'idle', sync_mode = NULL, last_sync_reason = NULL, last_sync_started_at = NULL, last_sync_completed_at = NULL, last_successful_sync_at = NULL, last_full_sync_completed_at = NULL, last_partial_sync_completed_at = NULL, sync_cursor_history_id = NULL, last_history_id = NULL, last_sync_error = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = 1;`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) markEmailSyncStarted(mode string, reason string, mailboxEmail string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := a.db.Exec(`UPDATE email_sync_state SET mailbox_email = ?, sync_status = 'running', sync_mode = ?, last_sync_reason = ?, last_sync_started_at = ?, last_sync_error = NULL, last_full_sync_completed_at = CASE WHEN ? = 'full' THEN last_full_sync_completed_at ELSE last_full_sync_completed_at END, last_partial_sync_completed_at = CASE WHEN ? = 'partial' THEN last_partial_sync_completed_at ELSE last_partial_sync_completed_at END, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, mailboxEmail, mode, reason, now, mode, mode)
	return err
}

func (a *App) markEmailSyncSucceeded(mode string, reason string, mailboxEmail string, result emailSyncResult) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var syncedWindowDays any
	if mode == "full" || mode == "window_update" {
		syncedWindowDays = normalizeEmailSyncWindowDays(result.SyncedWindowDays)
	}
	_, err := a.db.Exec(`UPDATE email_sync_state SET mailbox_email = ?, sync_status = 'idle', sync_mode = NULL, last_sync_reason = ?, last_sync_completed_at = ?, last_successful_sync_at = ?, last_full_sync_completed_at = CASE WHEN ? = 'full' THEN ? ELSE last_full_sync_completed_at END, last_partial_sync_completed_at = CASE WHEN ? = 'partial' THEN ? ELSE last_partial_sync_completed_at END, synced_window_days = COALESCE(?, synced_window_days), sync_cursor_history_id = ?, last_history_id = ?, last_sync_error = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, mailboxEmail, reason, now, now, mode, now, mode, now, syncedWindowDays, nullIfEmpty(result.CursorHistoryID), nullIfEmpty(result.LastHistoryID))
	return err
}

func (a *App) markEmailSyncFailed(mode string, syncErr error) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := a.db.Exec(`UPDATE email_sync_state SET sync_status = 'idle', sync_mode = NULL, last_sync_completed_at = ?, last_sync_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, now, truncateString(syncErr.Error(), 1000))
	return err
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

var _ = sync.Mutex{}
