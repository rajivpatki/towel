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
	emailSyncRecentWindowDays = 7
	emailSyncInterval         = 5 * time.Minute
	emailSyncFreshnessMaxAge  = 2 * time.Minute
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
	OldestMessageAt               string `json:"oldest_message_at,omitempty"`
	NewestMessageAt               string `json:"newest_message_at,omitempty"`
	OldestMessageInternalDateUnix int64  `json:"oldest_message_internal_date_unix,omitempty"`
	NewestMessageInternalDateUnix int64  `json:"newest_message_internal_date_unix,omitempty"`
}

type emailSyncResult struct {
	CursorHistoryID string
	LastHistoryID   string
	Metrics         emailSyncMetrics
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
		log.Printf("email sync resolving mode: requested=%s reason=%s", defaultString(strings.TrimSpace(mode), "partial"), reason)
		status, statusErr := a.getEmailSyncStatus()
		if statusErr != nil {
			_ = a.markEmailSyncFailed("partial", statusErr)
			return statusErr
		}
		cursor := strings.TrimSpace(status.SyncCursorHistoryID)
		if cursor == "" {
			log.Printf("email partial sync escalated to full: reason=%s mailbox has no history cursor", reason)
			result, err = a.performFullEmailSync()
			mode = "full"
		} else {
			log.Printf("email partial sync starting: cursor=%s", cursor)
			result, err = a.performPartialEmailSync(cursor)
			if errors.Is(err, errEmailHistoryExpired) {
				log.Printf("email partial sync cursor expired; retrying full sync: cursor=%s", cursor)
				result, err = a.performFullEmailSync()
				mode = "full"
			}
		}
	case "full":
		log.Printf("email full sync requested explicitly: reason=%s", reason)
		result, err = a.performFullEmailSync()
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
	log.Printf("email sync completed: mode=%s reason=%s mailbox=%s messages=%d oldest=%s newest=%s cursor=%s", mode, reason, mailboxEmail, result.Metrics.MessageCount, result.Metrics.OldestMessageAt, result.Metrics.NewestMessageAt, result.CursorHistoryID)
	return nil
}

func (a *App) performFullEmailSync() (emailSyncResult, error) {
	windowStart := time.Now().Add(-time.Duration(emailSyncRecentWindowDays) * 24 * time.Hour).UTC().Format(time.RFC3339)
	log.Printf("email full sync running: fetching messages for dates between %s and %s", windowStart, time.Now().UTC().Format(time.RFC3339))
	messageIDs, cursorHistoryID, err := a.syncRecentMessagesWindow(emailSyncRecentWindowDays)
	if err != nil {
		return emailSyncResult{}, err
	}
	log.Printf("email full sync enumerated candidate messages: count=%d", len(messageIDs))
	if err := a.pruneSyncedEmailsOutsideWindow(messageIDs); err != nil {
		return emailSyncResult{}, err
	}
	log.Printf("email full sync prune successful: retained_messages=%d", len(messageIDs))
	metrics, err := a.getEmailSyncMetrics()
	if err != nil {
		return emailSyncResult{}, err
	}
	return emailSyncResult{
		CursorHistoryID: cursorHistoryID,
		LastHistoryID:   cursorHistoryID,
		Metrics:         metrics,
	}, nil
}

func (a *App) performPartialEmailSync(cursor string) (emailSyncResult, error) {
	if strings.TrimSpace(cursor) == "" {
		return a.performFullEmailSync()
	}
	log.Printf("email partial sync running: start_history_id=%s", cursor)
	changes, lastHistoryID, err := a.listGmailHistoryChanges(cursor)
	if err != nil {
		return emailSyncResult{}, err
	}
	log.Printf("email partial sync change set loaded: upserts=%d deletes=%d last_history_id=%s", len(changes.Upsert), len(changes.Deleted), lastHistoryID)
	for _, messageID := range changes.UpsertIDs() {
		log.Printf("email partial sync fetching changed message: message_id=%s", messageID)
		message, fetchErr := a.fetchGmailMessage(messageID)
		if fetchErr != nil {
			return emailSyncResult{}, fetchErr
		}
		log.Printf("email partial sync fetch successful: message_id=%s internal_date=%s history_id=%s", message.ID, message.InternalDate, message.HistoryID)
		if _, upsertErr := a.upsertSyncedEmail(message); upsertErr != nil {
			return emailSyncResult{}, upsertErr
		}
		log.Printf("email partial sync upsert successful: message_id=%s", message.ID)
		delete(changes.Deleted, messageID)
	}
	for _, messageID := range changes.DeletedIDs() {
		log.Printf("email partial sync marking deleted message: message_id=%s", messageID)
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
		CursorHistoryID: lastHistoryID,
		LastHistoryID:   lastHistoryID,
		Metrics:         metrics,
	}, nil
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
	metrics, err := a.getEmailSyncMetrics()
	if err != nil {
		return EmailSyncStatus{}, err
	}
	status.MessageCount = metrics.MessageCount
	status.OldestMessageAt = metrics.OldestMessageAt
	status.NewestMessageAt = metrics.NewestMessageAt
	status.OldestMessageInternalDateUnix = metrics.OldestMessageInternalDateUnix
	status.NewestMessageInternalDateUnix = metrics.NewestMessageInternalDateUnix
	return status, nil
}

func (a *App) resetEmailSyncStore() error {
	a.emailSyncMu.Lock()
	defer a.emailSyncMu.Unlock()
	stmts := []string{
		`DELETE FROM synced_email_labels;`,
		`DELETE FROM synced_email_attachments;`,
		`DELETE FROM synced_emails;`,
		`UPDATE email_sync_state SET mailbox_email = NULL, sync_status = 'idle', sync_mode = NULL, last_sync_reason = NULL, last_sync_started_at = NULL, last_sync_completed_at = NULL, last_successful_sync_at = NULL, last_full_sync_completed_at = NULL, last_partial_sync_completed_at = NULL, sync_cursor_history_id = NULL, last_history_id = NULL, last_sync_error = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = 1;`,
	}
	for _, stmt := range stmts {
		if _, err := a.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) markEmailSyncStarted(mode string, reason string, mailboxEmail string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := a.db.Exec(`UPDATE email_sync_state SET mailbox_email = ?, sync_status = 'running', sync_mode = ?, last_sync_reason = ?, last_sync_started_at = ?, last_sync_error = NULL, last_full_sync_completed_at = CASE WHEN ? = 'full' THEN last_full_sync_completed_at ELSE last_full_sync_completed_at END, last_partial_sync_completed_at = CASE WHEN ? = 'partial' THEN last_partial_sync_completed_at ELSE last_partial_sync_completed_at END, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, mailboxEmail, mode, reason, now, mode, mode)
	return err
}

func (a *App) markEmailSyncSucceeded(mode string, reason string, mailboxEmail string, result emailSyncResult) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := a.db.Exec(`UPDATE email_sync_state SET mailbox_email = ?, sync_status = 'idle', sync_mode = NULL, last_sync_reason = ?, last_sync_completed_at = ?, last_successful_sync_at = ?, last_full_sync_completed_at = CASE WHEN ? = 'full' THEN ? ELSE last_full_sync_completed_at END, last_partial_sync_completed_at = CASE WHEN ? = 'partial' THEN ? ELSE last_partial_sync_completed_at END, sync_cursor_history_id = ?, last_history_id = ?, last_sync_error = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, mailboxEmail, reason, now, now, mode, now, mode, now, nullIfEmpty(result.CursorHistoryID), nullIfEmpty(result.LastHistoryID))
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
