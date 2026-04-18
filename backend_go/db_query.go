package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

type DBQueryRequest struct {
	SQL string `json:"sql"`
}

type DBSyncRequest struct {
	Mode string `json:"mode"`
}

type DBQueryResponse struct {
	SQL       string           `json:"sql"`
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	RowCount  int              `json:"row_count"`
	Truncated bool             `json:"truncated"`
	Sync      EmailSyncStatus  `json:"sync"`
	Notes     []string         `json:"notes,omitempty"`
}

var allowedDBStatementPrefix = regexp.MustCompile(`(?is)^\s*(select|with|pragma|explain)\b`)

func (a *App) handleEmailSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	status, err := a.getEmailSyncStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *App) handleEmailSyncQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload DBQueryRequest
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	response, err := a.executeSafeDBQuery(payload.SQL, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleEmailSyncTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var payload DBSyncRequest
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := strings.ToLower(strings.TrimSpace(payload.Mode))
	if mode == "" {
		mode = "partial"
	}
	if mode != "partial" && mode != "full" {
		writeError(w, http.StatusBadRequest, "mode must be either 'partial' or 'full'")
		return
	}
	a.syncEmailsInBackground(mode, "development_manual_trigger")
	status, err := a.getEmailSyncStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"success": true,
		"mode":    mode,
		"sync":    status,
	})
}

func (a *App) executeQueryDBTool(arguments map[string]any) (string, error) {
	sqlText, _ := arguments["sql"].(string)
	response, err := a.executeSafeDBQuery(sqlText, 100)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"ok":        true,
		"tool":      "query_db",
		"sql":       response.SQL,
		"columns":   response.Columns,
		"rows":      response.Rows,
		"row_count": response.RowCount,
		"truncated": response.Truncated,
		"sync":      response.Sync,
		"notes":     response.Notes,
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func (a *App) executeSafeDBQuery(rawSQL string, maxRows int) (DBQueryResponse, error) {
	normalizedSQL := strings.TrimSpace(rawSQL)
	if normalizedSQL == "" {
		return DBQueryResponse{}, fmt.Errorf("sql is required")
	}
	if err := validateSafeDBQuery(normalizedSQL); err != nil {
		return DBQueryResponse{}, err
	}
	rows, err := a.db.Query(normalizedSQL)
	if err != nil {
		return DBQueryResponse{}, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return DBQueryResponse{}, err
	}
	items := make([]map[string]any, 0)
	truncated := false
	for rows.Next() {
		if len(items) >= maxRows {
			truncated = true
			break
		}
		values := make([]any, len(columns))
		scans := make([]any, len(columns))
		for i := range values {
			scans[i] = &values[i]
		}
		if err := rows.Scan(scans...); err != nil {
			return DBQueryResponse{}, err
		}
		item := make(map[string]any, len(columns))
		for i, column := range columns {
			item[column] = normalizeSQLValue(values[i])
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return DBQueryResponse{}, err
	}
	status, err := a.getEmailSyncStatus()
	if err != nil {
		return DBQueryResponse{}, err
	}
	notes := make([]string, 0, 2)
	if truncated {
		notes = append(notes, fmt.Sprintf("Results were truncated to the first %d rows.", maxRows))
	}
	if strings.TrimSpace(status.LastSyncError) != "" {
		notes = append(notes, "The latest mailbox sync recorded an error. Inspect sync status before trusting freshness-sensitive queries.")
	}
	return DBQueryResponse{
		SQL:       normalizedSQL,
		Columns:   columns,
		Rows:      items,
		RowCount:  len(items),
		Truncated: truncated,
		Sync:      status,
		Notes:     notes,
	}, nil
}

func validateSafeDBQuery(rawSQL string) error {
	value := strings.TrimSpace(rawSQL)
	if value == "" {
		return fmt.Errorf("sql is required")
	}
	if strings.Count(value, ";") > 1 || (strings.Contains(value, ";") && !strings.HasSuffix(value, ";")) {
		return fmt.Errorf("only a single read-only SQL statement is allowed")
	}
	if !allowedDBStatementPrefix.MatchString(value) {
		return fmt.Errorf("only read-only SELECT, WITH, PRAGMA, or EXPLAIN queries are allowed")
	}
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"insert ", "update ", "delete ", "drop ", "alter ", "create ", "replace ", "attach ", "detach ", "vacuum ", "reindex ", "begin ", "commit ", "rollback ", "secret_records", "user_sessions", "conversation_messages", "conversations", "custom_agents", "preferences", "setup_state"} {
		if strings.Contains(lower, forbidden) {
			return fmt.Errorf("query contains a forbidden keyword or table reference: %s", strings.TrimSpace(forbidden))
		}
	}
	return nil
}

func normalizeSQLValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return string(typed)
	case sql.NullString:
		if typed.Valid {
			return typed.String
		}
		return nil
	default:
		return typed
	}
}

func (a *App) allToolDefinitions() []GmailToolDefinition {
	definitions := make([]GmailToolDefinition, 0, len(gmailToolDefinitions)+1)
	definitions = append(definitions, gmailToolDefinitions...)
	definitions = append(definitions, a.buildQueryDBToolDefinition())
	return definitions
}

func allToolDefinitionsSnapshot() []GmailToolDefinition {
	if appInstance != nil {
		return appInstance.allToolDefinitions()
	}
	definitions := make([]GmailToolDefinition, 0, len(gmailToolDefinitions))
	definitions = append(definitions, gmailToolDefinitions...)
	return definitions
}

func (a *App) buildQueryDBToolDefinition() GmailToolDefinition {
	status, err := a.getEmailSyncStatus()
	statusSummary := "Mailbox sync status is currently unavailable."
	if err == nil {
		parts := []string{
			fmt.Sprintf("mailbox=%s", defaultString(status.MailboxEmail, "not_connected")),
			fmt.Sprintf("state=%s", defaultString(status.SyncStatus, "idle")),
			fmt.Sprintf("window_days=%d", status.SyncedWindowDays),
			fmt.Sprintf("message_count=%d", status.MessageCount),
		}
		if strings.TrimSpace(status.LastSuccessfulSyncAt) != "" {
			parts = append(parts, "last_successful_sync_at="+status.LastSuccessfulSyncAt)
		}
		if strings.TrimSpace(status.SyncCursorHistoryID) != "" {
			parts = append(parts, "history_cursor_present=yes")
		} else {
			parts = append(parts, "history_cursor_present=no")
		}
		if strings.TrimSpace(status.OldestMessageAt) != "" {
			parts = append(parts, "oldest_cached_message_at="+status.OldestMessageAt)
		}
		if strings.TrimSpace(status.NewestMessageAt) != "" {
			parts = append(parts, "newest_cached_message_at="+status.NewestMessageAt)
		}
		if strings.TrimSpace(status.LastSyncError) != "" {
			parts = append(parts, "last_sync_error="+status.LastSyncError)
		}
		sort.Strings(parts)
		statusSummary = strings.Join(parts, "; ")
	}
	return GmailToolDefinition{
		Name:         "query_db",
		GmailActions: []string{"sqlite.read"},
		SafetyModel:  "read_only",
		Description: strings.Join([]string{
			"Query the local SQLite Gmail cache instead of calling Gmail APIs when cached data is sufficient.",
			"The cache is populated by a full sync of roughly the last 30 days of mail using users.messages.list plus users.messages.get(format=full), then kept updated by incremental users.history.list(startHistoryId=...) syncs every 5 minutes and before freshness-sensitive chat requests; if Gmail returns history 404, the app falls back to a fresh 30-day full sync.",
			"Current sync context: " + statusSummary + ".",
			"Allowed tables: email_sync_state (sync cursor/freshness metadata), synced_emails (one row per cached Gmail message with headers, normalized subject, body text/html, labels JSON, attachment names JSON, sizes, sender and unsubscribe metadata), synced_email_attachments (one row per attachment name with mime type and size metadata).",
			"Allowed views: synced_email_labels_with_names (joins message-label pairs with human-readable label names - use this for label queries instead of raw tables).",
			"Key synced_emails columns: message_id, thread_id, history_id, subject, subject_normalized, snippet, from_name, from_email, from_raw, reply_to, to_addresses, cc_addresses, bcc_addresses, delivered_to, date_header, internal_date_unix, internal_date, size_estimate, body_text, body_html, body_size_estimate, attachment_count, attachment_names, attachment_total_size, label_ids, list_unsubscribe, list_unsubscribe_post, list_id, precedence_header, auto_submitted_header, feedback_id, in_reply_to, references_header, is_unread, is_starred, is_important, is_in_inbox, is_in_spam, is_in_trash, has_attachments, is_deleted, deleted_at, sync_updated_at.",
			"Keep queries read-only and relevant to the synced email cache. Prefer WHERE clauses and explicit LIMITs for large result sets.",
		}, " "),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"sql": map[string]any{
					"type":        "string",
					"description": "A single read-only SQL query against email_sync_state, synced_emails, synced_email_attachments, or synced_email_labels_with_names.",
				},
			},
			"required":             []string{"sql"},
			"additionalProperties": false,
		},
	}
}
