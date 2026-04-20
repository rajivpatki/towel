package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	memoryEmbeddingBatchSize    = 24
	memoryEmbeddingMaxTextChars = 3000
	memorySearchResultLimit     = 10
	memoryListPageSizeDefault   = 50
	memoryListPageSizeMax       = 50
)

var errMemoryEmbeddingAlreadyRunning = errors.New("memory embedding sync already running")

type memoryEmbeddingSource struct {
	ID        int64
	Content   string
	CreatedAt string
	UpdatedAt string
}

type memoryEmbeddingDocument struct {
	Source            memoryEmbeddingSource
	Text              string
	Title             string
	SourceFingerprint string
}

type memoryEmbeddingExistingState struct {
	SourceFingerprint string
	Provider          string
	Model             string
	Dimensions        int
}

type memoryEmbeddingRunRequest struct {
	Reason           string
	MemoryIDs        []int64
	DeletedMemoryIDs []int64
	Reset            bool
	FullRescan       bool
}

type memorySearchMatch struct {
	MemoryID  int64
	Distance  float64
	Text      string
	CreatedAt string
	UpdatedAt string
}

func normalizeMemoryText(value string) string {
	return truncateTextPreservingBoundaries(cleanWhitespace(value), memoryEmbeddingMaxTextChars)
}

func cloneInt64Slice(values []int64) []int64 {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]int64, len(values))
	copy(cloned, values)
	return cloned
}

func appendUniqueInt64Set(target map[int64]struct{}, values []int64) map[int64]struct{} {
	if len(values) == 0 {
		return target
	}
	if target == nil {
		target = make(map[int64]struct{}, len(values))
	}
	for _, value := range values {
		if value <= 0 {
			continue
		}
		target[value] = struct{}{}
	}
	return target
}

func sortedInt64Keys(values map[int64]struct{}) []int64 {
	if len(values) == 0 {
		return nil
	}
	keys := make([]int64, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Slice(keys, func(i int, j int) bool {
		return keys[i] < keys[j]
	})
	return keys
}

func buildCreateMemoryToolDefinition() GmailToolDefinition {
	return GmailToolDefinition{
		Name:         "create_memory",
		GmailActions: []string{"memory.create"},
		SafetyModel:  "safe_write",
		Description: strings.Join([]string{
			"Persist one high-signal user memory for future conversations in Towel.",
			"Use this only after the conversation has progressed enough to reveal a durable preference, recurring workflow choice, standing constraint, identity fact, or other stable context that is likely to matter later.",
			"Do not use this on every turn, and do not store one-off requests, speculative inferences, mailbox contents, or secrets.",
			"If there is a reasonable chance the memory already exists, call search_memories first.",
			"After a successful create_memory call, explicitly say in your assistant message that you saved it as a memory so the thread history records that it was already captured.",
		}, " "),
		Parameters: gmailObjectSchema(
			"Parameters for `create_memory`. Save one concise durable memory at a time.",
			map[string]any{
				"text": gmailStringSchema(gmailDescription(
					"The memory text to save. Write it as a clear durable fact or preference in natural language.",
					`{"text":"The user prefers concise answers with the recommendation first."}`,
					`{"text":"The user wants reversible Gmail cleanup actions prioritized over permanent deletion."}`,
				)),
			},
			"text",
		),
	}
}

func buildStaticSearchMemoriesToolDefinition() GmailToolDefinition {
	return GmailToolDefinition{
		Name:         "search_memories",
		GmailActions: []string{"memory.search"},
		SafetyModel:  "read_only",
		Description: strings.Join([]string{
			"Semantically search saved user memories that may be relevant to the current conversation.",
			"Use this when personalization, response style, standing constraints, or recurring workflow context may matter.",
			fmt.Sprintf("Returns at most %d memories per search.", memorySearchResultLimit),
		}, " "),
		Parameters: gmailObjectSchema(
			"Parameters for `search_memories`. Use a short natural-language query describing the preference or context you want to recover.",
			map[string]any{
				"query": gmailStringSchema(gmailDescription(
					"Natural-language query describing the preference, fact, workflow, or standing constraint you want to retrieve from memory.",
					`{"query":"user preferences for answer style"}`,
					`{"query":"standing Gmail cleanup preferences and constraints"}`,
				)),
			},
			"query",
		),
	}
}

func (a *App) buildSearchMemoriesToolDefinition() GmailToolDefinition {
	definition := buildStaticSearchMemoriesToolDefinition()
	_, config, _, supported, err := a.resolveCurrentEmailEmbeddingConfig()
	if err == nil && supported {
		row := a.db.QueryRow(`SELECT COUNT(*) FROM memory_embeddings`)
		indexedRows := "unknown"
		var count int
		if scanErr := row.Scan(&count); scanErr == nil {
			indexedRows = strconv.Itoa(count)
		}
		definition.Description += " Current memory embedding index context: " + fmt.Sprintf("embedding_provider=%s; embedding_model=%s; dimensions=%d; indexed_rows=%s.", config.Provider, config.Model, config.Dimensions, indexedRows)
	} else {
		definition.Description += " Memory semantic retrieval depends on the currently selected embedding-capable agent configuration."
	}
	return definition
}

func parsePositiveIntQuery(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func clampMemoryPageSize(value int) int {
	if value <= 0 {
		return memoryListPageSizeDefault
	}
	if value > memoryListPageSizeMax {
		return memoryListPageSizeMax
	}
	return value
}

func (a *App) listMemories(page int, pageSize int) ([]MemoryItem, bool, error) {
	if page <= 0 {
		page = 1
	}
	pageSize = clampMemoryPageSize(pageSize)
	offset := (page - 1) * pageSize

	rows, err := a.db.Query(`
		SELECT id, content, created_at, updated_at
		FROM memories
		ORDER BY updated_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, pageSize+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	items := make([]MemoryItem, 0)
	for rows.Next() {
		var item MemoryItem
		if err := rows.Scan(&item.ID, &item.Text, &item.CreatedAt, &item.UpdatedAt); err != nil {
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

func (a *App) getMemoryByID(id int64) (MemoryItem, error) {
	row := a.db.QueryRow(`SELECT id, content, created_at, updated_at FROM memories WHERE id = ?`, id)
	var item MemoryItem
	if err := row.Scan(&item.ID, &item.Text, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return MemoryItem{}, err
	}
	return item, nil
}

func (a *App) getMemoriesByID(ids []int64) (map[int64]MemoryItem, error) {
	items := make(map[int64]MemoryItem)
	if len(ids) == 0 {
		return items, nil
	}

	uniqueIDs := sortedInt64Keys(appendUniqueInt64Set(nil, ids))
	placeholders := strings.TrimRight(strings.Repeat("?,", len(uniqueIDs)), ",")
	args := make([]any, 0, len(uniqueIDs))
	for _, id := range uniqueIDs {
		args = append(args, id)
	}

	rows, err := a.db.Query(`
		SELECT id, content, created_at, updated_at
		FROM memories
		WHERE id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var item MemoryItem
		if err := rows.Scan(&item.ID, &item.Text, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items[item.ID] = item
	}
	return items, rows.Err()
}

func (a *App) createMemory(text string, reason string) (MemoryItem, error) {
	normalized := normalizeMemoryText(text)
	if normalized == "" {
		log.Printf("memory warning: create rejected: reason=%s detail=empty_text", reason)
		return MemoryItem{}, errors.New("text is required")
	}

	log.Printf("memory create: reason=%s chars=%d", reason, len(normalized))
	result, err := a.db.Exec(`INSERT INTO memories (content) VALUES (?)`, normalized)
	if err != nil {
		log.Printf("memory failure: action=create reason=%s err=%v", reason, err)
		return MemoryItem{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		log.Printf("memory failure: action=create_last_insert_id reason=%s err=%v", reason, err)
		return MemoryItem{}, err
	}
	item, err := a.getMemoryByID(id)
	if err != nil {
		log.Printf("memory failure: action=create_fetch reason=%s memory_id=%d err=%v", reason, id, err)
		return MemoryItem{}, err
	}
	log.Printf("memory success: action=create reason=%s memory_id=%d", reason, item.ID)
	_ = a.logActionHistory("create", "Created memory: "+truncateString(item.Text, 120), "")
	a.refreshMemoryEmbeddingsForIDsInBackground("memory_created:"+reason, []int64{item.ID}, nil)
	return item, nil
}

func (a *App) updateMemory(id int64, text string, reason string) (MemoryItem, error) {
	normalized := normalizeMemoryText(text)
	if id <= 0 {
		log.Printf("memory warning: update rejected: reason=%s detail=invalid_id", reason)
		return MemoryItem{}, errors.New("memory_id is invalid")
	}
	if normalized == "" {
		log.Printf("memory warning: update rejected: reason=%s memory_id=%d detail=empty_text", reason, id)
		return MemoryItem{}, errors.New("text is required")
	}

	log.Printf("memory update: reason=%s memory_id=%d chars=%d", reason, id, len(normalized))
	result, err := a.db.Exec(`UPDATE memories SET content = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, normalized, id)
	if err != nil {
		log.Printf("memory failure: action=update reason=%s memory_id=%d err=%v", reason, id, err)
		return MemoryItem{}, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		log.Printf("memory failure: action=update_rows_affected reason=%s memory_id=%d err=%v", reason, id, err)
		return MemoryItem{}, err
	}
	if rowsAffected == 0 {
		log.Printf("memory warning: update missed: reason=%s memory_id=%d", reason, id)
		return MemoryItem{}, sql.ErrNoRows
	}
	item, err := a.getMemoryByID(id)
	if err != nil {
		log.Printf("memory failure: action=update_fetch reason=%s memory_id=%d err=%v", reason, id, err)
		return MemoryItem{}, err
	}
	log.Printf("memory success: action=update reason=%s memory_id=%d", reason, item.ID)
	_ = a.logActionHistory("update", "Updated memory: "+truncateString(item.Text, 120), "")
	a.refreshMemoryEmbeddingsForIDsInBackground("memory_updated:"+reason, []int64{item.ID}, nil)
	return item, nil
}

func (a *App) deleteMemories(ids []int64, reason string) (int64, error) {
	if len(ids) == 0 {
		log.Printf("memory warning: delete rejected: reason=%s detail=no_ids", reason)
		return 0, errors.New("ids are required")
	}

	deduped := sortedInt64Keys(appendUniqueInt64Set(nil, ids))
	placeholders := strings.TrimRight(strings.Repeat("?,", len(deduped)), ",")
	args := make([]any, 0, len(deduped))
	for _, id := range deduped {
		args = append(args, id)
	}

	log.Printf("memory delete: reason=%s memory_ids=%d", reason, len(deduped))
	tx, err := a.db.Begin()
	if err != nil {
		log.Printf("memory failure: action=delete_begin reason=%s err=%v", reason, err)
		return 0, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	rows, err := tx.Query(`SELECT id FROM memory_embeddings WHERE memory_id IN (`+placeholders+`)`, args...)
	if err != nil {
		log.Printf("memory failure: action=delete_list_embeddings reason=%s err=%v", reason, err)
		return 0, err
	}
	embeddingIDs := make([]int64, 0)
	for rows.Next() {
		var embeddingID int64
		if err := rows.Scan(&embeddingID); err != nil {
			rows.Close()
			log.Printf("memory failure: action=delete_scan_embedding_ids reason=%s err=%v", reason, err)
			return 0, err
		}
		embeddingIDs = append(embeddingIDs, embeddingID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		log.Printf("memory failure: action=delete_iterate_embedding_ids reason=%s err=%v", reason, err)
		return 0, err
	}
	rows.Close()

	for _, embeddingID := range embeddingIDs {
		if _, err := tx.Exec(`DELETE FROM memory_embedding_index WHERE embedding_id = ?`, embeddingID); err != nil {
			log.Printf("memory failure: action=delete_memory_embedding_index reason=%s embedding_id=%d err=%v", reason, embeddingID, err)
			return 0, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM memory_embeddings WHERE memory_id IN (`+placeholders+`)`, args...); err != nil {
		log.Printf("memory failure: action=delete_memory_embeddings reason=%s err=%v", reason, err)
		return 0, err
	}
	result, err := tx.Exec(`DELETE FROM memories WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		log.Printf("memory failure: action=delete_memories reason=%s err=%v", reason, err)
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		log.Printf("memory failure: action=delete_rows_affected reason=%s err=%v", reason, err)
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		log.Printf("memory failure: action=delete_commit reason=%s err=%v", reason, err)
		return 0, err
	}
	log.Printf("memory success: action=delete reason=%s deleted=%d", reason, deleted)
	_ = a.logActionHistory("delete", fmt.Sprintf("Deleted %d memory item(s)", deleted), "")
	return deleted, nil
}

func (a *App) handleMemories(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if query != "" {
			items, err := a.searchMemoryItems(query)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, MemoriesOut{
				Memories: items,
				Page:     1,
				PageSize: len(items),
				HasMore:  false,
				Query:    query,
			})
			return
		}

		page := parsePositiveIntQuery(r.URL.Query().Get("page"), 1)
		pageSize := clampMemoryPageSize(parsePositiveIntQuery(r.URL.Query().Get("page_size"), memoryListPageSizeDefault))
		items, hasMore, err := a.listMemories(page, pageSize)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, MemoriesOut{
			Memories: items,
			Page:     page,
			PageSize: pageSize,
			HasMore:  hasMore,
		})
	case http.MethodPost:
		var payload MemoryCreateIn
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item, err := a.createMemory(payload.Text, "api")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, MemoryOut{Memory: item})
	case http.MethodDelete:
		var payload MemoryDeleteIn
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		deleted, err := a.deleteMemories(payload.IDs, "api")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, MemoryDeleteOut{Success: true, Deleted: deleted})
	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (a *App) handleMemoryItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	prefix := a.config.APIPrefix + "/memories/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	idText := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, prefix))
	if idText == "" {
		writeError(w, http.StatusBadRequest, "memory_id is required")
		return
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "memory_id is invalid")
		return
	}

	if r.Method == http.MethodDelete {
		deleted, err := a.deleteMemories([]int64{id}, "api_single")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, MemoryDeleteOut{Success: true, Deleted: deleted})
		return
	}

	var payload MemoryUpdateIn
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	item, err := a.updateMemory(id, payload.Text, "api")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "memory not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, MemoryOut{Memory: item})
}

func (a *App) beginMemoryEmbeddingRun() bool {
	a.memoryEmbeddingMu.Lock()
	defer a.memoryEmbeddingMu.Unlock()
	if a.memoryEmbeddingRunning {
		return false
	}
	a.memoryEmbeddingRunning = true
	return true
}

func (a *App) queuePendingMemoryEmbeddingRun(request memoryEmbeddingRunRequest) {
	a.memoryEmbeddingMu.Lock()
	defer a.memoryEmbeddingMu.Unlock()
	a.memoryEmbeddingPending = true
	if strings.TrimSpace(request.Reason) != "" {
		a.memoryEmbeddingPendingReason = request.Reason
	}
	a.memoryEmbeddingPendingReset = a.memoryEmbeddingPendingReset || request.Reset
	a.memoryEmbeddingPendingFullRescan = a.memoryEmbeddingPendingFullRescan || request.FullRescan
	if !a.memoryEmbeddingPendingFullRescan {
		a.memoryEmbeddingPendingMemoryIDs = appendUniqueInt64Set(a.memoryEmbeddingPendingMemoryIDs, request.MemoryIDs)
	}
	a.memoryEmbeddingPendingDeletedIDs = appendUniqueInt64Set(a.memoryEmbeddingPendingDeletedIDs, request.DeletedMemoryIDs)
}

func (a *App) finishMemoryEmbeddingRun() (memoryEmbeddingRunRequest, bool) {
	a.memoryEmbeddingMu.Lock()
	defer a.memoryEmbeddingMu.Unlock()
	a.memoryEmbeddingRunning = false
	if !a.memoryEmbeddingPending {
		return memoryEmbeddingRunRequest{}, false
	}
	request := memoryEmbeddingRunRequest{
		Reason:           strings.TrimSpace(a.memoryEmbeddingPendingReason),
		DeletedMemoryIDs: sortedInt64Keys(a.memoryEmbeddingPendingDeletedIDs),
		Reset:            a.memoryEmbeddingPendingReset,
		FullRescan:       a.memoryEmbeddingPendingFullRescan,
	}
	if !request.FullRescan {
		request.MemoryIDs = sortedInt64Keys(a.memoryEmbeddingPendingMemoryIDs)
	}
	a.memoryEmbeddingPending = false
	a.memoryEmbeddingPendingReason = ""
	a.memoryEmbeddingPendingReset = false
	a.memoryEmbeddingPendingFullRescan = false
	a.memoryEmbeddingPendingMemoryIDs = nil
	a.memoryEmbeddingPendingDeletedIDs = nil
	return request, true
}

func (a *App) refreshMemoryEmbeddingsInBackground(reason string, reset bool) {
	a.refreshMemoryEmbeddingRequestInBackground(memoryEmbeddingRunRequest{
		Reason:     reason,
		Reset:      reset,
		FullRescan: true,
	})
}

func (a *App) refreshMemoryEmbeddingsForIDsInBackground(reason string, memoryIDs []int64, deletedMemoryIDs []int64) {
	a.refreshMemoryEmbeddingRequestInBackground(memoryEmbeddingRunRequest{
		Reason:           reason,
		MemoryIDs:        cloneInt64Slice(memoryIDs),
		DeletedMemoryIDs: cloneInt64Slice(deletedMemoryIDs),
	})
}

func (a *App) refreshMemoryEmbeddingRequestInBackground(request memoryEmbeddingRunRequest) {
	go func() {
		if err := a.syncMemoryEmbeddings(request); err != nil && !errors.Is(err, errMemoryEmbeddingAlreadyRunning) {
			log.Printf("memory embedding error: async refresh failed: reason=%s reset=%t full_rescan=%t memory_ids=%d deleted_memory_ids=%d err=%v", request.Reason, request.Reset, request.FullRescan, len(request.MemoryIDs), len(request.DeletedMemoryIDs), err)
		}
	}()
}

func (a *App) syncMemoryEmbeddings(request memoryEmbeddingRunRequest) error {
	request.Reason = strings.TrimSpace(request.Reason)
	request.MemoryIDs = cloneInt64Slice(request.MemoryIDs)
	request.DeletedMemoryIDs = cloneInt64Slice(request.DeletedMemoryIDs)
	if !request.FullRescan && !request.Reset && len(request.MemoryIDs) == 0 && len(request.DeletedMemoryIDs) == 0 {
		return nil
	}
	if !a.beginMemoryEmbeddingRun() {
		a.queuePendingMemoryEmbeddingRun(request)
		log.Printf("memory embedding warning: run already active; queued follow-up refresh: reason=%s reset=%t full_rescan=%t memory_ids=%d deleted_memory_ids=%d", request.Reason, request.Reset, request.FullRescan, len(request.MemoryIDs), len(request.DeletedMemoryIDs))
		return errMemoryEmbeddingAlreadyRunning
	}
	startedAt := time.Now()
	outcome := "success"
	defer func() {
		nextRequest, hasPending := a.finishMemoryEmbeddingRun()
		log.Printf("memory embedding stop: reason=%s outcome=%s duration=%s reset=%t full_rescan=%t memory_ids=%d deleted_memory_ids=%d", request.Reason, outcome, time.Since(startedAt).Round(time.Millisecond), request.Reset, request.FullRescan, len(request.MemoryIDs), len(request.DeletedMemoryIDs))
		if hasPending {
			log.Printf("memory embedding warning: dispatching queued follow-up refresh: reason=%s reset=%t full_rescan=%t memory_ids=%d deleted_memory_ids=%d", nextRequest.Reason, nextRequest.Reset, nextRequest.FullRescan, len(nextRequest.MemoryIDs), len(nextRequest.DeletedMemoryIDs))
			a.refreshMemoryEmbeddingRequestInBackground(nextRequest)
		}
	}()
	log.Printf("memory embedding start: reason=%s reset=%t full_rescan=%t memory_ids=%d deleted_memory_ids=%d", request.Reason, request.Reset, request.FullRescan, len(request.MemoryIDs), len(request.DeletedMemoryIDs))

	if request.Reset {
		if err := a.clearMemoryEmbeddings(); err != nil {
			outcome = "failure"
			log.Printf("memory embedding failure: reason=%s step=clear err=%v", request.Reason, err)
			return err
		}
	}
	if len(request.DeletedMemoryIDs) > 0 {
		if err := a.deleteMemoryEmbeddingsByMemoryIDs(request.DeletedMemoryIDs); err != nil {
			outcome = "failure"
			log.Printf("memory embedding failure: reason=%s step=delete deleted_memory_ids=%d err=%v", request.Reason, len(request.DeletedMemoryIDs), err)
			return err
		}
	}

	agent, config, credential, supported, err := a.resolveCurrentEmailEmbeddingConfig()
	if err != nil {
		outcome = "failure"
		log.Printf("memory embedding failure: reason=%s step=resolve_config err=%v", request.Reason, err)
		return err
	}
	if !supported {
		log.Printf("memory embedding warning: unsupported provider for selected agent; cleaning orphaned index: reason=%s provider=%s", request.Reason, agent.Provider)
		return a.cleanupOrphanedMemoryEmbeddingIndex()
	}

	memoryIDs := request.MemoryIDs
	if request.FullRescan {
		memoryIDs = nil
	}
	sources, err := a.listMemoryEmbeddingSources(memoryIDs)
	if err != nil {
		outcome = "failure"
		log.Printf("memory embedding failure: reason=%s step=list_sources memory_ids=%d full_rescan=%t err=%v", request.Reason, len(memoryIDs), request.FullRescan, err)
		return err
	}
	existingStates, err := a.listExistingMemoryEmbeddingStates(extractMemoryEmbeddingSourceIDs(sources))
	if err != nil {
		outcome = "failure"
		log.Printf("memory embedding failure: reason=%s step=list_existing_states source_count=%d err=%v", request.Reason, len(sources), err)
		return err
	}

	documents := make([]memoryEmbeddingDocument, 0, len(sources))
	for _, source := range sources {
		document := buildMemoryEmbeddingDocument(source)
		if strings.TrimSpace(document.Text) == "" {
			if err := a.deleteMemoryEmbeddingsByMemoryIDs([]int64{source.ID}); err != nil {
				outcome = "failure"
				log.Printf("memory embedding failure: reason=%s step=delete_empty_document_embeddings memory_id=%d err=%v", request.Reason, source.ID, err)
				return err
			}
			continue
		}
		if existingState, ok := existingStates[source.ID]; ok &&
			existingState.SourceFingerprint == document.SourceFingerprint &&
			existingState.Provider == config.Provider &&
			existingState.Model == config.Model &&
			existingState.Dimensions == config.Dimensions {
			continue
		}
		documents = append(documents, document)
	}
	if len(documents) == 0 {
		if err := a.cleanupOrphanedMemoryEmbeddingIndex(); err != nil {
			outcome = "failure"
			log.Printf("memory embedding failure: reason=%s step=cleanup_orphans_noop err=%v", request.Reason, err)
			return err
		}
		log.Printf("memory embedding success: reason=%s provider=%s model=%s source_count=%d indexed=0 deleted_memory_ids=%d reset=%t full_rescan=%t", request.Reason, config.Provider, config.Model, len(sources), len(request.DeletedMemoryIDs), request.Reset, request.FullRescan)
		return nil
	}

	ctx := context.Background()
	indexedCount := 0
	for start := 0; start < len(documents); start += memoryEmbeddingBatchSize {
		end := start + memoryEmbeddingBatchSize
		if end > len(documents) {
			end = len(documents)
		}
		batch := documents[start:end]
		texts := make([]string, 0, len(batch))
		titles := make([]string, 0, len(batch))
		for _, document := range batch {
			texts = append(texts, document.Text)
			titles = append(titles, document.Title)
		}
		vectors, err := a.embedTexts(ctx, agent, config, credential, texts, titles, "RETRIEVAL_DOCUMENT")
		if err != nil {
			outcome = "failure"
			log.Printf("memory embedding failure: reason=%s step=embed_texts provider=%s model=%s batch_start=%d batch_size=%d err=%v", request.Reason, config.Provider, config.Model, start, len(batch), err)
			return err
		}
		if len(vectors) != len(batch) {
			err := fmt.Errorf("embedding provider returned %d vectors for %d inputs", len(vectors), len(batch))
			outcome = "failure"
			log.Printf("memory embedding failure: reason=%s step=vector_count_mismatch provider=%s model=%s batch_start=%d batch_size=%d err=%v", request.Reason, config.Provider, config.Model, start, len(batch), err)
			return err
		}
		if err := a.upsertMemoryEmbeddingBatch(batch, vectors, config); err != nil {
			outcome = "failure"
			log.Printf("memory embedding failure: reason=%s step=upsert_batch provider=%s model=%s batch_start=%d batch_size=%d err=%v", request.Reason, config.Provider, config.Model, start, len(batch), err)
			return err
		}
		indexedCount += len(batch)
	}

	if err := a.cleanupOrphanedMemoryEmbeddingIndex(); err != nil {
		outcome = "failure"
		log.Printf("memory embedding failure: reason=%s step=cleanup_orphans provider=%s model=%s err=%v", request.Reason, config.Provider, config.Model, err)
		return err
	}
	log.Printf("memory embedding success: reason=%s provider=%s model=%s source_count=%d indexed=%d deleted_memory_ids=%d reset=%t full_rescan=%t", request.Reason, config.Provider, config.Model, len(sources), indexedCount, len(request.DeletedMemoryIDs), request.Reset, request.FullRescan)
	return nil
}

func (a *App) listMemoryEmbeddingSources(memoryIDs []int64) ([]memoryEmbeddingSource, error) {
	query := `SELECT id, content, created_at, updated_at FROM memories`
	args := make([]any, 0, len(memoryIDs))
	if len(memoryIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(memoryIDs)), ",")
		query += ` WHERE id IN (` + placeholders + `)`
		for _, memoryID := range memoryIDs {
			args = append(args, memoryID)
		}
	}
	query += ` ORDER BY updated_at DESC, id DESC`

	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sources := make([]memoryEmbeddingSource, 0)
	for rows.Next() {
		var source memoryEmbeddingSource
		if err := rows.Scan(&source.ID, &source.Content, &source.CreatedAt, &source.UpdatedAt); err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func extractMemoryEmbeddingSourceIDs(sources []memoryEmbeddingSource) []int64 {
	ids := make([]int64, 0, len(sources))
	for _, source := range sources {
		if source.ID <= 0 {
			continue
		}
		ids = append(ids, source.ID)
	}
	return ids
}

func (a *App) listExistingMemoryEmbeddingStates(memoryIDs []int64) (map[int64]memoryEmbeddingExistingState, error) {
	states := make(map[int64]memoryEmbeddingExistingState)
	if len(memoryIDs) == 0 {
		return states, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(memoryIDs)), ",")
	args := make([]any, 0, len(memoryIDs))
	for _, memoryID := range memoryIDs {
		args = append(args, memoryID)
	}
	rows, err := a.db.Query(`
		SELECT
			memory_id,
			COALESCE(source_fingerprint, ''),
			embedding_provider,
			embedding_model,
			embedding_dimensions
		FROM memory_embeddings
		WHERE memory_id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var memoryID int64
		var state memoryEmbeddingExistingState
		if err := rows.Scan(&memoryID, &state.SourceFingerprint, &state.Provider, &state.Model, &state.Dimensions); err != nil {
			return nil, err
		}
		states[memoryID] = state
	}
	return states, rows.Err()
}

func buildMemoryEmbeddingDocument(source memoryEmbeddingSource) memoryEmbeddingDocument {
	text := normalizeMemoryText(source.Content)
	title := truncateString(text, 80)
	return memoryEmbeddingDocument{
		Source:            source,
		Text:              text,
		Title:             title,
		SourceFingerprint: computeMemoryEmbeddingFingerprint(source, text),
	}
}

func computeMemoryEmbeddingFingerprint(source memoryEmbeddingSource, text string) string {
	payload, _ := json.Marshal(struct {
		ID        int64  `json:"id"`
		Text      string `json:"text"`
		UpdatedAt string `json:"updated_at"`
	}{
		ID:        source.ID,
		Text:      text,
		UpdatedAt: source.UpdatedAt,
	})
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum[:])
}

func (a *App) upsertMemoryEmbeddingBatch(documents []memoryEmbeddingDocument, vectors [][]float32, config emailEmbeddingConfig) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for index, document := range documents {
		vectorJSONBytes, err := json.Marshal(vectors[index])
		if err != nil {
			return err
		}
		vectorJSON := string(vectorJSONBytes)

		var embeddingID int64
		if err := tx.QueryRow(`
			INSERT INTO memory_embeddings (
				memory_id,
				embedding_text,
				source_fingerprint,
				embedding_vector,
				embedding_provider,
				embedding_model,
				embedding_dimensions,
				indexed_at,
				updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			ON CONFLICT(memory_id) DO UPDATE SET
				embedding_text = excluded.embedding_text,
				source_fingerprint = excluded.source_fingerprint,
				embedding_vector = excluded.embedding_vector,
				embedding_provider = excluded.embedding_provider,
				embedding_model = excluded.embedding_model,
				embedding_dimensions = excluded.embedding_dimensions,
				indexed_at = CURRENT_TIMESTAMP,
				updated_at = CURRENT_TIMESTAMP
			RETURNING id
		`,
			document.Source.ID,
			document.Text,
			document.SourceFingerprint,
			vectorJSON,
			config.Provider,
			config.Model,
			config.Dimensions,
		).Scan(&embeddingID); err != nil {
			return err
		}

		if _, err := tx.Exec(`DELETE FROM memory_embedding_index WHERE embedding_id = ?`, embeddingID); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO memory_embedding_index (
				embedding_id,
				embedding_vector,
				memory_id,
				embedding_text
			) VALUES (?, ?, ?, ?)
		`,
			embeddingID,
			vectorJSON,
			document.Source.ID,
			document.Text,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) deleteMemoryEmbeddingsByMemoryIDs(memoryIDs []int64) error {
	if len(memoryIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(memoryIDs)), ",")
	args := make([]any, 0, len(memoryIDs))
	for _, memoryID := range memoryIDs {
		args = append(args, memoryID)
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	rows, err := tx.Query(`SELECT id FROM memory_embeddings WHERE memory_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return err
	}
	embeddingIDs := make([]int64, 0)
	for rows.Next() {
		var embeddingID int64
		if err := rows.Scan(&embeddingID); err != nil {
			rows.Close()
			return err
		}
		embeddingIDs = append(embeddingIDs, embeddingID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, embeddingID := range embeddingIDs {
		if _, err := tx.Exec(`DELETE FROM memory_embedding_index WHERE embedding_id = ?`, embeddingID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM memory_embeddings WHERE memory_id IN (`+placeholders+`)`, args...); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) clearMemoryEmbeddings() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if _, err := tx.Exec(`DELETE FROM memory_embedding_index`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM memory_embeddings`); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) cleanupOrphanedMemoryEmbeddingIndex() error {
	rows, err := a.db.Query(`
		SELECT mei.embedding_id
		FROM memory_embedding_index mei
		LEFT JOIN memory_embeddings me ON me.id = mei.embedding_id
		WHERE me.id IS NULL
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	orphanIDs := make([]int64, 0)
	for rows.Next() {
		var orphanID int64
		if err := rows.Scan(&orphanID); err != nil {
			return err
		}
		orphanIDs = append(orphanIDs, orphanID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, orphanID := range orphanIDs {
		if _, err := a.db.Exec(`DELETE FROM memory_embedding_index WHERE embedding_id = ?`, orphanID); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) searchMemoryMatches(query string) ([]memorySearchMatch, emailEmbeddingConfig, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, emailEmbeddingConfig{}, errors.New("query is required")
	}
	agent, config, credential, supported, err := a.resolveCurrentEmailEmbeddingConfig()
	if err != nil {
		return nil, emailEmbeddingConfig{}, err
	}
	if !supported {
		return nil, emailEmbeddingConfig{}, errors.New("memory search is unavailable for the selected agent provider")
	}

	queryVectors, err := a.embedTexts(context.Background(), agent, config, credential, []string{query}, []string{""}, "RETRIEVAL_QUERY")
	if err != nil {
		return nil, emailEmbeddingConfig{}, err
	}
	if len(queryVectors) != 1 {
		return nil, emailEmbeddingConfig{}, errors.New("embedding provider did not return exactly one query vector")
	}
	queryVectorJSON, err := json.Marshal(queryVectors[0])
	if err != nil {
		return nil, emailEmbeddingConfig{}, err
	}

	rows, err := a.db.Query(`
		SELECT
			memory_id,
			distance,
			COALESCE(embedding_text, '')
		FROM memory_embedding_index
		WHERE embedding_vector MATCH ?
			AND k = ?
		ORDER BY distance ASC
	`, string(queryVectorJSON), memorySearchResultLimit)
	if err != nil {
		return nil, emailEmbeddingConfig{}, err
	}
	defer rows.Close()

	orderedIDs := make([]int64, 0, memorySearchResultLimit)
	matches := make([]memorySearchMatch, 0, memorySearchResultLimit)
	for rows.Next() {
		var match memorySearchMatch
		if err := rows.Scan(&match.MemoryID, &match.Distance, &match.Text); err != nil {
			return nil, emailEmbeddingConfig{}, err
		}
		matches = append(matches, match)
		orderedIDs = append(orderedIDs, match.MemoryID)
	}
	if err := rows.Err(); err != nil {
		return nil, emailEmbeddingConfig{}, err
	}

	memoryItems, err := a.getMemoriesByID(orderedIDs)
	if err != nil {
		return nil, emailEmbeddingConfig{}, err
	}

	enriched := make([]memorySearchMatch, 0, len(matches))
	for _, match := range matches {
		item, ok := memoryItems[match.MemoryID]
		if !ok {
			continue
		}
		match.Text = item.Text
		match.CreatedAt = item.CreatedAt
		match.UpdatedAt = item.UpdatedAt
		enriched = append(enriched, match)
	}
	return enriched, config, nil
}

func (a *App) searchMemoryItems(query string) ([]MemoryItem, error) {
	matches, _, err := a.searchMemoryMatches(query)
	if err != nil {
		return nil, err
	}

	items := make([]MemoryItem, 0, len(matches))
	for _, match := range matches {
		items = append(items, MemoryItem{
			ID:        match.MemoryID,
			Text:      match.Text,
			CreatedAt: match.CreatedAt,
			UpdatedAt: match.UpdatedAt,
		})
	}
	return items, nil
}

func (a *App) searchMemories(query string) (map[string]any, error) {
	matches, config, err := a.searchMemoryMatches(query)
	if err != nil {
		return nil, err
	}

	results := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		results = append(results, map[string]any{
			"memory_id":  match.MemoryID,
			"text":       match.Text,
			"created_at": match.CreatedAt,
			"updated_at": match.UpdatedAt,
			"distance":   match.Distance,
		})
	}

	return map[string]any{
		"ok":                 true,
		"tool":               "search_memories",
		"query":              strings.TrimSpace(query),
		"result_count":       len(results),
		"results":            results,
		"embedding_provider": config.Provider,
		"embedding_model":    config.Model,
	}, nil
}

func (a *App) executeCreateMemoryTool(arguments map[string]any) (string, error) {
	text := strings.TrimSpace(stringArgument(arguments["text"]))
	if text == "" {
		return "", errors.New("text is required")
	}
	item, err := a.createMemory(text, "tool")
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"ok":     true,
		"tool":   "create_memory",
		"memory": item,
		"note":   "Briefly state in your assistant reply that you saved this as a memory.",
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func (a *App) executeSearchMemoriesTool(arguments map[string]any) (string, error) {
	query := strings.TrimSpace(stringArgument(arguments["query"]))
	payload, err := a.searchMemories(query)
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(result), nil
}
