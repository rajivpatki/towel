package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	scheduledTaskTitleMaxChars       = 200
	scheduledTaskInstructionMaxChars = 8000
	scheduledTaskListPageSizeDefault = 50
	scheduledTaskListPageSizeMax     = 50
	scheduledTaskRunTimeout          = 2 * time.Minute
	scheduledTaskToolPageSize        = 10
)

type ScheduledTaskItem struct {
	ID                  int64    `json:"id"`
	Title               string   `json:"title"`
	Instruction         string   `json:"instruction"`
	Enabled             bool     `json:"enabled"`
	RequireInInbox      bool     `json:"require_in_inbox"`
	LabelNames          []string `json:"label_names"`
	LastRunStartedAt    string   `json:"last_run_started_at,omitempty"`
	LastRunCompletedAt  string   `json:"last_run_completed_at,omitempty"`
	LastRunStatus       string   `json:"last_run_status,omitempty"`
	LastRunMessage      string   `json:"last_run_message,omitempty"`
	LastRunError        string   `json:"last_run_error,omitempty"`
	LastRunMatchedCount int      `json:"last_run_matched_count,omitempty"`
	LastRunHistoryStart string   `json:"last_run_history_start,omitempty"`
	LastRunHistoryEnd   string   `json:"last_run_history_end,omitempty"`
	CreatedAt           string   `json:"created_at"`
	UpdatedAt           string   `json:"updated_at"`
}

type ScheduledTasksOut struct {
	Tasks    []ScheduledTaskItem `json:"tasks"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"page_size"`
	HasMore  bool                `json:"has_more"`
	Query    string              `json:"query,omitempty"`
}

type ScheduledTaskCreateIn struct {
	Title          string   `json:"title"`
	Instruction    string   `json:"instruction"`
	Enabled        bool     `json:"enabled"`
	RequireInInbox bool     `json:"require_in_inbox"`
	LabelNames     []string `json:"label_names"`
}

type ScheduledTaskUpdateIn struct {
	Title          string   `json:"title"`
	Instruction    string   `json:"instruction"`
	Enabled        bool     `json:"enabled"`
	RequireInInbox bool     `json:"require_in_inbox"`
	LabelNames     []string `json:"label_names"`
}

type ScheduledTaskDeleteIn struct {
	IDs []int64 `json:"ids"`
}

type ScheduledTaskOut struct {
	Task ScheduledTaskItem `json:"task"`
}

type ScheduledTaskDeleteOut struct {
	Success bool  `json:"success"`
	Deleted int64 `json:"deleted"`
}

type scheduledTaskRunRequest struct {
	TaskID         int64
	TriggerMode    string
	TriggerReason  string
	HistoryStartID string
	HistoryEndID   string
	MessageIDs     []string
}

type scheduledTaskEmailCandidate struct {
	MessageID        string   `json:"message_id"`
	ThreadID         string   `json:"thread_id"`
	HistoryID        string   `json:"history_id,omitempty"`
	Subject          string   `json:"subject,omitempty"`
	Snippet          string   `json:"snippet,omitempty"`
	FromName         string   `json:"from_name,omitempty"`
	FromEmail        string   `json:"from_email,omitempty"`
	InternalDate     string   `json:"internal_date,omitempty"`
	InternalDateUnix int64    `json:"internal_date_unix,omitempty"`
	IsInInbox        bool     `json:"is_in_inbox"`
	LabelNames       []string `json:"label_names,omitempty"`
}

func clampScheduledTaskPageSize(value int) int {
	if value <= 0 {
		return scheduledTaskListPageSizeDefault
	}
	if value > scheduledTaskListPageSizeMax {
		return scheduledTaskListPageSizeMax
	}
	return value
}

func normalizeScheduledTaskTitle(value string) string {
	return truncateString(cleanWhitespace(value), scheduledTaskTitleMaxChars)
}

func normalizeScheduledTaskInstruction(value string) string {
	return truncateString(strings.TrimSpace(value), scheduledTaskInstructionMaxChars)
}

func normalizeScheduledTaskLabelNames(values []string) []string {
	return normalizeStringSlice(values)
}

func encodeScheduledTaskLabelNames(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(labels)
	return string(encoded)
}

func decodeScheduledTaskLabelNames(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var labels []string
	if err := json.Unmarshal([]byte(value), &labels); err != nil {
		return nil
	}
	return normalizeScheduledTaskLabelNames(labels)
}

func scheduledTaskLabelSearchText(labels []string) string {
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		trimmed := strings.ToLower(cleanWhitespace(label))
		if trimmed == "" {
			continue
		}
		parts = append(parts, trimmed)
	}
	return strings.Join(parts, " ")
}

func stringSliceArgument(value any) []string {
	switch typed := value.(type) {
	case []string:
		return normalizeScheduledTaskLabelNames(typed)
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			items = append(items, text)
		}
		return normalizeScheduledTaskLabelNames(items)
	default:
		return nil
	}
}

func scanScheduledTaskItem(scanner interface {
	Scan(dest ...any) error
}) (ScheduledTaskItem, error) {
	var item ScheduledTaskItem
	var enabled int
	var requireInInbox int
	var labelNamesJSON sql.NullString
	if err := scanner.Scan(
		&item.ID,
		&item.Title,
		&item.Instruction,
		&enabled,
		&requireInInbox,
		&labelNamesJSON,
		&item.LastRunStartedAt,
		&item.LastRunCompletedAt,
		&item.LastRunStatus,
		&item.LastRunMessage,
		&item.LastRunError,
		&item.LastRunMatchedCount,
		&item.LastRunHistoryStart,
		&item.LastRunHistoryEnd,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return ScheduledTaskItem{}, err
	}
	item.Enabled = enabled != 0
	item.RequireInInbox = requireInInbox != 0
	item.LabelNames = decodeScheduledTaskLabelNames(labelNamesJSON.String)
	return item, nil
}

func (a *App) getScheduledTaskByID(id int64) (ScheduledTaskItem, error) {
	row := a.db.QueryRow(`
		SELECT
			id,
			title,
			instruction,
			enabled,
			require_in_inbox,
			COALESCE(label_names_json, ''),
			COALESCE(last_run_started_at, ''),
			COALESCE(last_run_completed_at, ''),
			COALESCE(last_run_status, ''),
			COALESCE(last_run_message, ''),
			COALESCE(last_run_error, ''),
			COALESCE(last_run_matched_messages, 0),
			COALESCE(last_run_history_start_id, ''),
			COALESCE(last_run_history_end_id, ''),
			created_at,
			updated_at
		FROM scheduled_tasks
		WHERE id = ?
	`, id)
	return scanScheduledTaskItem(row)
}

func (a *App) listEnabledScheduledTasks() ([]ScheduledTaskItem, error) {
	rows, err := a.db.Query(`
		SELECT
			id,
			title,
			instruction,
			enabled,
			require_in_inbox,
			COALESCE(label_names_json, ''),
			COALESCE(last_run_started_at, ''),
			COALESCE(last_run_completed_at, ''),
			COALESCE(last_run_status, ''),
			COALESCE(last_run_message, ''),
			COALESCE(last_run_error, ''),
			COALESCE(last_run_matched_messages, 0),
			COALESCE(last_run_history_start_id, ''),
			COALESCE(last_run_history_end_id, ''),
			created_at,
			updated_at
		FROM scheduled_tasks
		WHERE enabled = 1
		ORDER BY updated_at DESC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ScheduledTaskItem, 0)
	for rows.Next() {
		item, err := scanScheduledTaskItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *App) listScheduledTasks(page int, pageSize int) ([]ScheduledTaskItem, bool, error) {
	if page <= 0 {
		page = 1
	}
	pageSize = clampScheduledTaskPageSize(pageSize)
	offset := (page - 1) * pageSize
	rows, err := a.db.Query(`
		SELECT
			id,
			title,
			instruction,
			enabled,
			require_in_inbox,
			COALESCE(label_names_json, ''),
			COALESCE(last_run_started_at, ''),
			COALESCE(last_run_completed_at, ''),
			COALESCE(last_run_status, ''),
			COALESCE(last_run_message, ''),
			COALESCE(last_run_error, ''),
			COALESCE(last_run_matched_messages, 0),
			COALESCE(last_run_history_start_id, ''),
			COALESCE(last_run_history_end_id, ''),
			created_at,
			updated_at
		FROM scheduled_tasks
		ORDER BY updated_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, pageSize+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]ScheduledTaskItem, 0)
	for rows.Next() {
		item, err := scanScheduledTaskItem(rows)
		if err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	return items, hasMore, nil
}

func tokenizeScheduledTaskSearchQuery(query string) []string {
	return strings.Fields(strings.ToLower(cleanWhitespace(query)))
}

func (a *App) searchScheduledTaskItems(query string) ([]ScheduledTaskItem, error) {
	tokens := tokenizeScheduledTaskSearchQuery(query)
	if len(tokens) == 0 {
		return nil, errors.New("query is required")
	}
	baseQuery := `
		SELECT
			id,
			title,
			instruction,
			enabled,
			require_in_inbox,
			COALESCE(label_names_json, ''),
			COALESCE(last_run_started_at, ''),
			COALESCE(last_run_completed_at, ''),
			COALESCE(last_run_status, ''),
			COALESCE(last_run_message, ''),
			COALESCE(last_run_error, ''),
			COALESCE(last_run_matched_messages, 0),
			COALESCE(last_run_history_start_id, ''),
			COALESCE(last_run_history_end_id, ''),
			created_at,
			updated_at
		FROM scheduled_tasks
		WHERE `
	conditions := make([]string, 0, len(tokens))
	args := make([]any, 0, len(tokens)*3)
	for _, token := range tokens {
		like := "%" + token + "%"
		conditions = append(conditions, `(LOWER(title) LIKE ? OR LOWER(instruction) LIKE ? OR LOWER(COALESCE(label_names_search_text, '')) LIKE ?)`)
		args = append(args, like, like, like)
	}
	rows, err := a.db.Query(baseQuery+strings.Join(conditions, ` AND `)+` ORDER BY updated_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ScheduledTaskItem, 0)
	for rows.Next() {
		item, err := scanScheduledTaskItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *App) searchScheduledTaskItemsPage(query string, page int, pageSize int) ([]ScheduledTaskItem, bool, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = scheduledTaskToolPageSize
	}
	offset := (page - 1) * pageSize
	tokens := tokenizeScheduledTaskSearchQuery(query)
	if len(tokens) == 0 {
		return a.listScheduledTasks(page, pageSize)
	}
	baseQuery := `
		SELECT
			id,
			title,
			instruction,
			enabled,
			require_in_inbox,
			COALESCE(label_names_json, ''),
			COALESCE(last_run_started_at, ''),
			COALESCE(last_run_completed_at, ''),
			COALESCE(last_run_status, ''),
			COALESCE(last_run_message, ''),
			COALESCE(last_run_error, ''),
			COALESCE(last_run_matched_messages, 0),
			COALESCE(last_run_history_start_id, ''),
			COALESCE(last_run_history_end_id, ''),
			created_at,
			updated_at
		FROM scheduled_tasks
		WHERE `
	conditions := make([]string, 0, len(tokens))
	args := make([]any, 0, len(tokens)*3+2)
	for _, token := range tokens {
		like := "%" + token + "%"
		conditions = append(conditions, `(LOWER(title) LIKE ? OR LOWER(instruction) LIKE ? OR LOWER(COALESCE(label_names_search_text, '')) LIKE ?)`)
		args = append(args, like, like, like)
	}
	args = append(args, pageSize+1, offset)
	rows, err := a.db.Query(baseQuery+strings.Join(conditions, ` AND `)+` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]ScheduledTaskItem, 0)
	for rows.Next() {
		item, err := scanScheduledTaskItem(rows)
		if err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	return items, hasMore, nil
}

func (a *App) createScheduledTask(title string, instruction string, enabled bool, requireInInbox bool, labelNames []string, reason string) (ScheduledTaskItem, error) {
	normalizedTitle := normalizeScheduledTaskTitle(title)
	normalizedInstruction := normalizeScheduledTaskInstruction(instruction)
	normalizedLabels := normalizeScheduledTaskLabelNames(labelNames)
	if normalizedTitle == "" {
		return ScheduledTaskItem{}, errors.New("title is required")
	}
	if normalizedInstruction == "" {
		return ScheduledTaskItem{}, errors.New("instruction is required")
	}
	log.Printf("scheduled task create: reason=%s title=%q enabled=%t inbox=%t labels=%d", reason, normalizedTitle, enabled, requireInInbox, len(normalizedLabels))
	result, err := a.db.Exec(`
		INSERT INTO scheduled_tasks (
			title,
			instruction,
			enabled,
			require_in_inbox,
			label_names_json,
			label_names_search_text
		) VALUES (?, ?, ?, ?, ?, ?)
	`, normalizedTitle, normalizedInstruction, boolToInt(enabled), boolToInt(requireInInbox), nullIfEmpty(encodeScheduledTaskLabelNames(normalizedLabels)), nullIfEmpty(scheduledTaskLabelSearchText(normalizedLabels)))
	if err != nil {
		return ScheduledTaskItem{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return ScheduledTaskItem{}, err
	}
	item, err := a.getScheduledTaskByID(id)
	if err != nil {
		return ScheduledTaskItem{}, err
	}
	_ = a.logActionHistory("create", "Created scheduled task: "+truncateString(item.Title, 120), "")
	return item, nil
}

func (a *App) updateScheduledTask(id int64, title string, instruction string, enabled bool, requireInInbox bool, labelNames []string, reason string) (ScheduledTaskItem, error) {
	if id <= 0 {
		return ScheduledTaskItem{}, errors.New("scheduled_task_id is invalid")
	}
	normalizedTitle := normalizeScheduledTaskTitle(title)
	normalizedInstruction := normalizeScheduledTaskInstruction(instruction)
	normalizedLabels := normalizeScheduledTaskLabelNames(labelNames)
	if normalizedTitle == "" {
		return ScheduledTaskItem{}, errors.New("title is required")
	}
	if normalizedInstruction == "" {
		return ScheduledTaskItem{}, errors.New("instruction is required")
	}
	log.Printf("scheduled task update: reason=%s task_id=%d title=%q enabled=%t inbox=%t labels=%d", reason, id, normalizedTitle, enabled, requireInInbox, len(normalizedLabels))
	result, err := a.db.Exec(`
		UPDATE scheduled_tasks
		SET
			title = ?,
			instruction = ?,
			enabled = ?,
			require_in_inbox = ?,
			label_names_json = ?,
			label_names_search_text = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, normalizedTitle, normalizedInstruction, boolToInt(enabled), boolToInt(requireInInbox), nullIfEmpty(encodeScheduledTaskLabelNames(normalizedLabels)), nullIfEmpty(scheduledTaskLabelSearchText(normalizedLabels)), id)
	if err != nil {
		return ScheduledTaskItem{}, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return ScheduledTaskItem{}, err
	}
	if rowsAffected == 0 {
		return ScheduledTaskItem{}, sql.ErrNoRows
	}
	item, err := a.getScheduledTaskByID(id)
	if err != nil {
		return ScheduledTaskItem{}, err
	}
	_ = a.logActionHistory("update", "Updated scheduled task: "+truncateString(item.Title, 120), "")
	return item, nil
}

func (a *App) deleteScheduledTasks(ids []int64, reason string) (int64, error) {
	if len(ids) == 0 {
		return 0, errors.New("ids are required")
	}
	deduped := sortedInt64Keys(appendUniqueInt64Set(nil, ids))
	placeholders := strings.TrimRight(strings.Repeat("?,", len(deduped)), ",")
	args := make([]any, 0, len(deduped))
	for _, id := range deduped {
		args = append(args, id)
	}
	log.Printf("scheduled task delete: reason=%s task_ids=%d", reason, len(deduped))
	result, err := a.db.Exec(`DELETE FROM scheduled_tasks WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	_ = a.logActionHistory("delete", fmt.Sprintf("Deleted %d scheduled task(s)", deleted), "")
	return deleted, nil
}

func buildCreateScheduledTaskToolDefinition() GmailToolDefinition {
	return GmailToolDefinition{
		Name:         "create_scheduled_task",
		GmailActions: []string{"scheduled_task.create"},
		SafetyModel:  "safe_write",
		Description: strings.Join([]string{
			"Create a scheduled email task that runs automatically whenever mailbox refresh sync detects new or modified emails.",
			"Each run only sees the emails updated in that sync tick, then applies optional task filters such as requiring Inbox or matching any configured label name.",
			"Use search_scheduled_tasks first when you suspect a similar task already exists.",
		}, " "),
		Parameters: gmailObjectSchema(
			"Parameters for `create_scheduled_task`. The task will run on future email refresh ticks only.",
			map[string]any{
				"title": gmailStringSchema(gmailDescription(
					"Short human-readable title for the scheduled task.",
					`{"title":"Triage recruiter mail"}`,
					`{"title":"Auto-label build alerts"}`,
				)),
				"instruction": gmailStringSchema(gmailDescription(
					"Autonomous instruction the scheduled-task agent should execute whenever matching updated emails appear. The task runs without UI confirmation, so write direct action-oriented instructions.",
					`{"instruction":"For each matching recruiter email, add the label Hiring/Recruiters and mark it starred."}`,
					`{"instruction":"If the updated email is a CI failure alert, inspect it and archive low-severity repeats while leaving critical failures in Inbox."}`,
				)),
				"enabled": gmailBooleanSchema(gmailDescription(
					"Whether the task should start active immediately. Defaults to true when omitted.",
					`{"enabled":true}`,
					`{"enabled":false}`,
				)),
				"require_in_inbox": gmailBooleanSchema(gmailDescription(
					"If true, only run this task for updated emails that are currently in Inbox.",
					`{"require_in_inbox":true}`,
					`{"require_in_inbox":false}`,
				)),
				"label_names": gmailStringArraySchema(gmailDescription(
					"Optional label-name filter. When provided, the task only runs on updated emails that match at least one of these label names. Matching is case-insensitive and works with Gmail system labels like `INBOX` as well as user label names.",
					`{"label_names":["Finance","Invoices"]}`,
					`{"label_names":["CATEGORY_UPDATES","Build Alerts"]}`,
				)),
			},
			"title",
			"instruction",
		),
	}
}

func buildUpdateScheduledTaskToolDefinition() GmailToolDefinition {
	return GmailToolDefinition{
		Name:         "update_scheduled_task",
		GmailActions: []string{"scheduled_task.update"},
		SafetyModel:  "safe_write",
		Description:  "Update an existing scheduled task by id. Any provided field replaces the current value; omitted fields keep their existing values.",
		Parameters: gmailObjectSchema(
			"Parameters for `update_scheduled_task`. Supply the task id and any fields you want to change.",
			map[string]any{
				"id": gmailIntegerSchema(gmailDescription(
					"Scheduled task id to update.",
					`{"id":12,"enabled":false}`,
					`{"id":7,"label_names":["INBOX"]}`,
				)),
				"title":            gmailStringSchema("Optional replacement title."),
				"instruction":      gmailStringSchema("Optional replacement instruction."),
				"enabled":          gmailBooleanSchema("Optional replacement enabled state."),
				"require_in_inbox": gmailBooleanSchema("Optional replacement Inbox filter."),
				"label_names":      gmailStringArraySchema("Optional replacement label-name filter list. Provide an empty array to clear the label filter."),
			},
			"id",
		),
	}
}

func buildSearchScheduledTasksToolDefinition() GmailToolDefinition {
	return GmailToolDefinition{
		Name:         "search_scheduled_tasks",
		GmailActions: []string{"scheduled_task.search"},
		SafetyModel:  "read_only",
		Description: strings.Join([]string{
			"Keyword-search scheduled tasks to inspect existing automation before creating or changing one.",
			"Passing an empty query lists all scheduled tasks.",
			"Search is basic keyword matching only, not semantic search.",
			fmt.Sprintf("Matches task title, instruction, and configured label names; results are paginated with a fixed page size of %d.", scheduledTaskToolPageSize),
		}, " "),
		Parameters: gmailObjectSchema(
			fmt.Sprintf("Parameters for `search_scheduled_tasks`. Use short keywords, task names, or label names. Set `query` to an empty string to list all scheduled tasks. Results always use page size %d.", scheduledTaskToolPageSize),
			map[string]any{
				"query": gmailStringSchema(gmailDescription(
					"Basic keyword query. Every keyword must match somewhere in the task title, instruction, or label names. Use an empty string to list all tasks.",
					`{"query":"recruiter inbox"}`,
					`{"query":"invoice finance"}`,
					`{"query":"","page":1}`,
				)),
				"page": gmailIntegerSchema(gmailDescription(
					fmt.Sprintf("1-based results page. Omit to use page 1. Each page returns up to %d tasks.", scheduledTaskToolPageSize),
					`{"query":"recruiter inbox","page":2}`,
					`{"query":"","page":3}`,
				)),
			},
		),
	}
}

func buildDeleteScheduledTaskToolDefinition() GmailToolDefinition {
	return GmailToolDefinition{
		Name:         "delete_scheduled_task",
		GmailActions: []string{"scheduled_task.delete"},
		SafetyModel:  "safe_write",
		Description:  "Delete one scheduled task by id.",
		Parameters: gmailObjectSchema(
			"Parameters for `delete_scheduled_task`.",
			map[string]any{
				"id": gmailIntegerSchema(gmailDescription(
					"Scheduled task id to delete.",
					`{"id":12}`,
					`{"id":3}`,
				)),
			},
			"id",
		),
	}
}

func (a *App) searchScheduledTasks(query string, page int) (map[string]any, error) {
	if page <= 0 {
		page = 1
	}
	items, hasMore, err := a.searchScheduledTaskItemsPage(query, page, scheduledTaskToolPageSize)
	if err != nil {
		return nil, err
	}
	searchMode := "basic_keyword"
	if cleanWhitespace(query) == "" {
		searchMode = "list_all"
	}
	return map[string]any{
		"ok":           true,
		"tool":         "search_scheduled_tasks",
		"query":        cleanWhitespace(query),
		"search_mode":  searchMode,
		"query_fields": []string{"title", "instruction", "label_names"},
		"page":         page,
		"page_size":    scheduledTaskToolPageSize,
		"has_more":     hasMore,
		"result_count": len(items),
		"results":      items,
	}, nil
}

func (a *App) executeCreateScheduledTaskTool(arguments map[string]any) (string, error) {
	title := strings.TrimSpace(stringArgument(arguments["title"]))
	instruction := strings.TrimSpace(stringArgument(arguments["instruction"]))
	enabled, ok := optionalBoolArgument(arguments["enabled"])
	if !ok {
		enabled = true
	}
	requireInInbox, _ := optionalBoolArgument(arguments["require_in_inbox"])
	labelNames := stringSliceArgument(arguments["label_names"])
	item, err := a.createScheduledTask(title, instruction, enabled, requireInInbox, labelNames, "tool")
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(map[string]any{
		"ok":   true,
		"tool": "create_scheduled_task",
		"task": item,
		"run_semantics": map[string]any{
			"trigger":              "email_refresh_tick",
			"updated_email_scope":  "new_or_modified_emails_detected_in_each_sync_tick",
			"filter_application":   "after_updated_email_selection",
			"basic_label_matching": "case_insensitive_any_match",
		},
	})
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func (a *App) executeUpdateScheduledTaskTool(arguments map[string]any) (string, error) {
	id, ok := optionalInt64Argument(arguments["id"])
	if !ok || id <= 0 {
		return "", errors.New("id is required")
	}
	existing, err := a.getScheduledTaskByID(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("scheduled task not found")
		}
		return "", err
	}
	title := existing.Title
	if raw, exists := arguments["title"]; exists {
		title = strings.TrimSpace(stringArgument(raw))
	}
	instruction := existing.Instruction
	if raw, exists := arguments["instruction"]; exists {
		instruction = strings.TrimSpace(stringArgument(raw))
	}
	enabled := existing.Enabled
	if raw, exists := arguments["enabled"]; exists {
		parsed, parsedOK := optionalBoolArgument(raw)
		if !parsedOK {
			return "", errors.New("enabled must be a boolean")
		}
		enabled = parsed
	}
	requireInInbox := existing.RequireInInbox
	if raw, exists := arguments["require_in_inbox"]; exists {
		parsed, parsedOK := optionalBoolArgument(raw)
		if !parsedOK {
			return "", errors.New("require_in_inbox must be a boolean")
		}
		requireInInbox = parsed
	}
	labelNames := existing.LabelNames
	if raw, exists := arguments["label_names"]; exists {
		labelNames = stringSliceArgument(raw)
	}
	item, err := a.updateScheduledTask(id, title, instruction, enabled, requireInInbox, labelNames, "tool")
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(map[string]any{
		"ok":   true,
		"tool": "update_scheduled_task",
		"task": item,
	})
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func (a *App) executeSearchScheduledTasksTool(arguments map[string]any) (string, error) {
	query := strings.TrimSpace(stringArgument(arguments["query"]))
	page, ok := optionalInt64Argument(arguments["page"])
	if !ok || page <= 0 {
		page = 1
	}
	payload, err := a.searchScheduledTasks(query, int(page))
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func (a *App) executeDeleteScheduledTaskTool(arguments map[string]any) (string, error) {
	id, ok := optionalInt64Argument(arguments["id"])
	if !ok || id <= 0 {
		return "", errors.New("id is required")
	}
	deleted, err := a.deleteScheduledTasks([]int64{id}, "tool")
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(map[string]any{
		"ok":      true,
		"tool":    "delete_scheduled_task",
		"success": true,
		"deleted": deleted,
	})
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func (a *App) handleScheduledTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		page := parsePositiveIntQuery(r.URL.Query().Get("page"), 1)
		pageSize := clampScheduledTaskPageSize(parsePositiveIntQuery(r.URL.Query().Get("page_size"), scheduledTaskListPageSizeDefault))
		items, hasMore, err := a.searchScheduledTaskItemsPage(query, page, pageSize)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ScheduledTasksOut{
			Tasks:    items,
			Page:     page,
			PageSize: pageSize,
			HasMore:  hasMore,
			Query:    query,
		})
	case http.MethodPost:
		var payload ScheduledTaskCreateIn
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item, err := a.createScheduledTask(payload.Title, payload.Instruction, payload.Enabled, payload.RequireInInbox, payload.LabelNames, "api")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ScheduledTaskOut{Task: item})
	case http.MethodDelete:
		var payload ScheduledTaskDeleteIn
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		deleted, err := a.deleteScheduledTasks(payload.IDs, "api")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ScheduledTaskDeleteOut{Success: true, Deleted: deleted})
	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (a *App) handleScheduledTaskItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	prefix := a.config.APIPrefix + "/scheduled-tasks/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	idText := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, prefix))
	if idText == "" {
		writeError(w, http.StatusBadRequest, "scheduled_task_id is required")
		return
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "scheduled_task_id is invalid")
		return
	}
	if r.Method == http.MethodDelete {
		deleted, err := a.deleteScheduledTasks([]int64{id}, "api_single")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ScheduledTaskDeleteOut{Success: true, Deleted: deleted})
		return
	}
	var payload ScheduledTaskUpdateIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	item, err := a.updateScheduledTask(id, payload.Title, payload.Instruction, payload.Enabled, payload.RequireInInbox, payload.LabelNames, "api")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "scheduled task not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ScheduledTaskOut{Task: item})
}

func mergeScheduledTaskRunRequests(existing scheduledTaskRunRequest, incoming scheduledTaskRunRequest) scheduledTaskRunRequest {
	merged := scheduledTaskRunRequest{
		TaskID:         incoming.TaskID,
		TriggerMode:    defaultString(incoming.TriggerMode, existing.TriggerMode),
		TriggerReason:  defaultString(incoming.TriggerReason, existing.TriggerReason),
		HistoryStartID: defaultString(existing.HistoryStartID, incoming.HistoryStartID),
		HistoryEndID:   defaultString(incoming.HistoryEndID, existing.HistoryEndID),
		MessageIDs:     sortedKeys(appendUniqueStringSet(appendUniqueStringSet(nil, existing.MessageIDs), incoming.MessageIDs)),
	}
	if merged.HistoryStartID == "" {
		merged.HistoryStartID = incoming.HistoryStartID
	}
	return merged
}

func (a *App) beginScheduledTaskRun(taskID int64) bool {
	a.scheduledTaskMu.Lock()
	defer a.scheduledTaskMu.Unlock()
	if a.scheduledTaskRunning == nil {
		a.scheduledTaskRunning = make(map[int64]bool)
	}
	if a.scheduledTaskRunning[taskID] {
		return false
	}
	a.scheduledTaskRunning[taskID] = true
	return true
}

func (a *App) queueScheduledTaskRun(request scheduledTaskRunRequest) {
	a.scheduledTaskMu.Lock()
	defer a.scheduledTaskMu.Unlock()
	if a.scheduledTaskPending == nil {
		a.scheduledTaskPending = make(map[int64]scheduledTaskRunRequest)
	}
	if existing, ok := a.scheduledTaskPending[request.TaskID]; ok {
		a.scheduledTaskPending[request.TaskID] = mergeScheduledTaskRunRequests(existing, request)
		return
	}
	request.MessageIDs = sortedKeys(appendUniqueStringSet(nil, request.MessageIDs))
	a.scheduledTaskPending[request.TaskID] = request
}

func (a *App) finishScheduledTaskRun(taskID int64) (scheduledTaskRunRequest, bool) {
	a.scheduledTaskMu.Lock()
	defer a.scheduledTaskMu.Unlock()
	if a.scheduledTaskRunning != nil {
		delete(a.scheduledTaskRunning, taskID)
	}
	if a.scheduledTaskPending == nil {
		return scheduledTaskRunRequest{}, false
	}
	next, ok := a.scheduledTaskPending[taskID]
	if ok {
		delete(a.scheduledTaskPending, taskID)
	}
	return next, ok
}

func (a *App) startOrQueueScheduledTaskRun(request scheduledTaskRunRequest) {
	request.MessageIDs = sortedKeys(appendUniqueStringSet(nil, request.MessageIDs))
	if request.TaskID <= 0 || len(request.MessageIDs) == 0 {
		return
	}
	if !a.beginScheduledTaskRun(request.TaskID) {
		a.queueScheduledTaskRun(request)
		log.Printf("scheduled task warning: run already active; queued follow-up: task_id=%d updated_messages=%d", request.TaskID, len(request.MessageIDs))
		return
	}
	go func() {
		defer func() {
			next, ok := a.finishScheduledTaskRun(request.TaskID)
			if ok {
				log.Printf("scheduled task info: dispatching queued follow-up: task_id=%d updated_messages=%d", next.TaskID, len(next.MessageIDs))
				a.startOrQueueScheduledTaskRun(next)
			}
		}()
		if err := a.runScheduledTask(request); err != nil {
			log.Printf("scheduled task failure: task_id=%d err=%v", request.TaskID, err)
		}
	}()
}

func (a *App) dispatchScheduledTaskRunsInBackground(syncMode string, syncReason string, result emailSyncResult) {
	if strings.TrimSpace(syncMode) != "partial" || strings.TrimSpace(result.StartHistoryID) == "" {
		return
	}
	updatedMessageIDs := sortedKeys(appendUniqueStringSet(nil, result.UpsertedMessageIDs))
	if len(updatedMessageIDs) == 0 {
		return
	}
	go func() {
		tasks, err := a.listEnabledScheduledTasks()
		if err != nil {
			log.Printf("scheduled task failure: list enabled tasks after sync failed: %v", err)
			return
		}
		if len(tasks) == 0 {
			return
		}
		candidates, err := a.listScheduledTaskEmailCandidates(updatedMessageIDs)
		if err != nil {
			log.Printf("scheduled task failure: loading updated email candidates failed: %v", err)
			return
		}
		for _, task := range tasks {
			matched := filterScheduledTaskCandidates(task, candidates)
			if len(matched) == 0 {
				continue
			}
			messageIDs := make([]string, 0, len(matched))
			for _, candidate := range matched {
				messageIDs = append(messageIDs, candidate.MessageID)
			}
			a.startOrQueueScheduledTaskRun(scheduledTaskRunRequest{
				TaskID:         task.ID,
				TriggerMode:    syncMode,
				TriggerReason:  syncReason,
				HistoryStartID: result.StartHistoryID,
				HistoryEndID:   result.LastHistoryID,
				MessageIDs:     messageIDs,
			})
		}
	}()
}

func (a *App) listScheduledTaskEmailCandidates(messageIDs []string) ([]scheduledTaskEmailCandidate, error) {
	uniqueIDs := sortedKeys(appendUniqueStringSet(nil, messageIDs))
	if len(uniqueIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(uniqueIDs)), ",")
	args := make([]any, 0, len(uniqueIDs))
	for _, messageID := range uniqueIDs {
		args = append(args, messageID)
	}
	rows, err := a.db.Query(`
		SELECT
			se.message_id,
			se.thread_id,
			COALESCE(se.history_id, ''),
			COALESCE(se.subject, ''),
			COALESCE(se.snippet, ''),
			COALESCE(se.from_name, ''),
			COALESCE(se.from_email, ''),
			COALESCE(se.internal_date, ''),
			COALESCE(se.internal_date_unix, 0),
			COALESCE(se.is_in_inbox, 0),
			COALESCE(sel.label_name, '')
		FROM synced_emails se
		LEFT JOIN synced_email_labels_with_names sel ON sel.message_id = se.message_id
		WHERE se.is_deleted = 0 AND se.message_id IN (`+placeholders+`)
		ORDER BY se.internal_date_unix DESC, se.message_id, sel.label_name
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidateByID := make(map[string]*scheduledTaskEmailCandidate, len(uniqueIDs))
	ordered := make([]string, 0, len(uniqueIDs))
	for rows.Next() {
		var messageID string
		var threadID string
		var historyID string
		var subject string
		var snippet string
		var fromName string
		var fromEmail string
		var internalDate string
		var internalDateUnix int64
		var isInInbox int
		var labelName string
		if err := rows.Scan(&messageID, &threadID, &historyID, &subject, &snippet, &fromName, &fromEmail, &internalDate, &internalDateUnix, &isInInbox, &labelName); err != nil {
			return nil, err
		}
		candidate, ok := candidateByID[messageID]
		if !ok {
			candidate = &scheduledTaskEmailCandidate{
				MessageID:        strings.TrimSpace(messageID),
				ThreadID:         strings.TrimSpace(threadID),
				HistoryID:        strings.TrimSpace(historyID),
				Subject:          strings.TrimSpace(subject),
				Snippet:          strings.TrimSpace(snippet),
				FromName:         strings.TrimSpace(fromName),
				FromEmail:        strings.TrimSpace(fromEmail),
				InternalDate:     strings.TrimSpace(internalDate),
				InternalDateUnix: internalDateUnix,
				IsInInbox:        isInInbox != 0,
			}
			candidateByID[messageID] = candidate
			ordered = append(ordered, messageID)
		}
		if strings.TrimSpace(labelName) != "" {
			candidate.LabelNames = append(candidate.LabelNames, labelName)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]scheduledTaskEmailCandidate, 0, len(ordered))
	for _, messageID := range ordered {
		candidate := candidateByID[messageID]
		candidate.LabelNames = normalizeScheduledTaskLabelNames(candidate.LabelNames)
		items = append(items, *candidate)
	}
	return items, nil
}

func filterScheduledTaskCandidates(task ScheduledTaskItem, candidates []scheduledTaskEmailCandidate) []scheduledTaskEmailCandidate {
	matched := make([]scheduledTaskEmailCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if task.RequireInInbox && !candidate.IsInInbox && !hasLabel(candidate.LabelNames, "INBOX") {
			continue
		}
		if len(task.LabelNames) > 0 {
			labelMatched := false
			for _, labelName := range task.LabelNames {
				if hasLabel(candidate.LabelNames, labelName) {
					labelMatched = true
					break
				}
			}
			if !labelMatched {
				continue
			}
		}
		matched = append(matched, candidate)
	}
	return matched
}

func buildScheduledTaskUserPrompt(task ScheduledTaskItem, request scheduledTaskRunRequest, candidates []scheduledTaskEmailCandidate) string {
	triggerRange := strings.TrimSpace(request.HistoryStartID)
	if triggerRange == "" {
		triggerRange = "full_refresh_or_initial_sync"
	} else {
		triggerRange = triggerRange + " -> " + defaultString(strings.TrimSpace(request.HistoryEndID), "current")
	}
	candidateJSON, _ := json.Marshal(candidates)
	lines := []string{
		"Scheduled task execution context:",
		fmt.Sprintf("- Task ID: %d", task.ID),
		fmt.Sprintf("- Task title: %s", task.Title),
		fmt.Sprintf("- Trigger mode: %s", defaultString(request.TriggerMode, "partial")),
		fmt.Sprintf("- Trigger reason: %s", defaultString(request.TriggerReason, "email_refresh_tick")),
		fmt.Sprintf("- History range: %s", triggerRange),
		fmt.Sprintf("- Matched updated emails: %d", len(candidates)),
		"",
		"Autonomous instruction:",
		task.Instruction,
		"",
		"Operate only on the matched updated emails provided below unless you need adjacent context from Gmail or the synced cache to complete the task correctly.",
		"Prefer bulk Gmail actions when appropriate.",
		"Matched updated emails JSON:",
		string(candidateJSON),
	}
	return strings.Join(lines, "\n")
}

func (a *App) markScheduledTaskRunStarted(taskID int64, request scheduledTaskRunRequest, matchedCount int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	message := fmt.Sprintf("Triggered by %s sync on %d matched updated email(s).", defaultString(request.TriggerMode, "partial"), matchedCount)
	_, err := a.db.Exec(`
		UPDATE scheduled_tasks
		SET
			last_run_started_at = ?,
			last_run_completed_at = NULL,
			last_run_status = 'running',
			last_run_message = ?,
			last_run_error = NULL,
			last_run_matched_messages = ?,
			last_run_history_start_id = ?,
			last_run_history_end_id = ?
		WHERE id = ?
	`, now, truncateString(message, 500), matchedCount, nullIfEmpty(request.HistoryStartID), nullIfEmpty(request.HistoryEndID), taskID)
	return err
}

func (a *App) markScheduledTaskRunSucceeded(taskID int64, request scheduledTaskRunRequest, matchedCount int, summary string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	trimmedSummary := strings.TrimSpace(summary)
	if trimmedSummary == "" {
		trimmedSummary = fmt.Sprintf("Completed scheduled task on %d matched updated email(s).", matchedCount)
	}
	_, err := a.db.Exec(`
		UPDATE scheduled_tasks
		SET
			last_run_completed_at = ?,
			last_run_status = 'success',
			last_run_message = ?,
			last_run_error = NULL,
			last_run_matched_messages = ?,
			last_run_history_start_id = ?,
			last_run_history_end_id = ?
		WHERE id = ?
	`, now, truncateString(trimmedSummary, 1000), matchedCount, nullIfEmpty(request.HistoryStartID), nullIfEmpty(request.HistoryEndID), taskID)
	return err
}

func (a *App) markScheduledTaskRunSkipped(taskID int64, request scheduledTaskRunRequest, message string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	trimmedMessage := strings.TrimSpace(message)
	if trimmedMessage == "" {
		trimmedMessage = "No matching updated emails remained after applying the current task filters."
	}
	_, err := a.db.Exec(`
		UPDATE scheduled_tasks
		SET
			last_run_completed_at = ?,
			last_run_status = 'skipped',
			last_run_message = ?,
			last_run_error = NULL,
			last_run_matched_messages = 0,
			last_run_history_start_id = ?,
			last_run_history_end_id = ?
		WHERE id = ?
	`, now, truncateString(trimmedMessage, 1000), nullIfEmpty(request.HistoryStartID), nullIfEmpty(request.HistoryEndID), taskID)
	return err
}

func (a *App) markScheduledTaskRunFailed(taskID int64, request scheduledTaskRunRequest, matchedCount int, runErr error) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := a.db.Exec(`
		UPDATE scheduled_tasks
		SET
			last_run_completed_at = ?,
			last_run_status = 'failed',
			last_run_error = ?,
			last_run_matched_messages = ?,
			last_run_history_start_id = ?,
			last_run_history_end_id = ?
		WHERE id = ?
	`, now, truncateString(runErr.Error(), 1000), matchedCount, nullIfEmpty(request.HistoryStartID), nullIfEmpty(request.HistoryEndID), taskID)
	return err
}

func (a *App) runScheduledTask(request scheduledTaskRunRequest) error {
	task, err := a.getScheduledTaskByID(request.TaskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if !task.Enabled {
		return nil
	}
	candidates, err := a.listScheduledTaskEmailCandidates(request.MessageIDs)
	if err != nil {
		_ = a.markScheduledTaskRunFailed(task.ID, request, 0, err)
		return err
	}
	matched := filterScheduledTaskCandidates(task, candidates)
	if len(matched) == 0 {
		return a.markScheduledTaskRunSkipped(task.ID, request, "No matching updated emails remained after applying the current task filters.")
	}
	if err := a.markScheduledTaskRunStarted(task.ID, request, len(matched)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), scheduledTaskRunTimeout)
	defer cancel()
	history := []ConversationMessage{{
		Role:    "user",
		Content: buildScheduledTaskUserPrompt(task, request, matched),
	}}
	response, actions, err := a.runAgentTurn(ctx, buildScheduledTaskSystemPrompt(), history, nil)
	if err != nil {
		_ = a.markScheduledTaskRunFailed(task.ID, request, len(matched), err)
		return err
	}
	summary := strings.TrimSpace(response)
	if len(actions) > 0 {
		summary = strings.TrimSpace(summary + "\n\nActions:\n- " + strings.Join(actions, "\n- "))
	}
	if err := a.markScheduledTaskRunSucceeded(task.ID, request, len(matched), summary); err != nil {
		return err
	}
	return nil
}
