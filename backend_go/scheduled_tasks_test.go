package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func seedScheduledTasks(t *testing.T, app *App, count int, marker string) {
	t.Helper()

	for i := 1; i <= count; i++ {
		title := fmt.Sprintf("%s task %02d", marker, i)
		instruction := fmt.Sprintf("Process %s task %02d", marker, i)
		labelsJSON := fmt.Sprintf(`["%s"]`, marker)
		if _, err := app.db.Exec(`
			INSERT INTO scheduled_tasks (
				title,
				instruction,
				enabled,
				require_in_inbox,
				label_names_json,
				label_names_search_text
			) VALUES (?, ?, 1, 0, ?, ?)
		`, title, instruction, labelsJSON, strings.ToLower(marker)); err != nil {
			t.Fatalf("insert scheduled task %d: %v", i, err)
		}
	}
}

func resultTitles(t *testing.T, raw any) []string {
	t.Helper()

	items, ok := raw.([]ScheduledTaskItem)
	if !ok {
		t.Fatalf("results type = %T, want []ScheduledTaskItem", raw)
	}
	titles := make([]string, 0, len(items))
	for _, item := range items {
		titles = append(titles, item.Title)
	}
	return titles
}

func TestSearchScheduledTasksEmptyQueryListsWithPagination(t *testing.T) {
	app := newTestApp(t)
	seedScheduledTasks(t, app, 12, "list")

	pageOne, err := app.searchScheduledTasks("", 1)
	if err != nil {
		t.Fatalf("search page 1: %v", err)
	}
	if got := pageOne["search_mode"]; got != "list_all" {
		t.Fatalf("search_mode page 1 = %v, want list_all", got)
	}
	if got := pageOne["page"]; got != 1 {
		t.Fatalf("page page 1 = %v, want 1", got)
	}
	if got := pageOne["page_size"]; got != scheduledTaskToolPageSize {
		t.Fatalf("page_size page 1 = %v, want %d", got, scheduledTaskToolPageSize)
	}
	if got := pageOne["has_more"]; got != true {
		t.Fatalf("has_more page 1 = %v, want true", got)
	}
	if got := pageOne["result_count"]; got != scheduledTaskToolPageSize {
		t.Fatalf("result_count page 1 = %v, want %d", got, scheduledTaskToolPageSize)
	}
	titlesPageOne := resultTitles(t, pageOne["results"])
	if len(titlesPageOne) != scheduledTaskToolPageSize {
		t.Fatalf("len(results) page 1 = %d, want %d", len(titlesPageOne), scheduledTaskToolPageSize)
	}
	if titlesPageOne[0] != "list task 12" || titlesPageOne[len(titlesPageOne)-1] != "list task 03" {
		t.Fatalf("unexpected page 1 titles: first=%q last=%q", titlesPageOne[0], titlesPageOne[len(titlesPageOne)-1])
	}

	pageTwo, err := app.searchScheduledTasks("", 2)
	if err != nil {
		t.Fatalf("search page 2: %v", err)
	}
	if got := pageTwo["search_mode"]; got != "list_all" {
		t.Fatalf("search_mode page 2 = %v, want list_all", got)
	}
	if got := pageTwo["page"]; got != 2 {
		t.Fatalf("page page 2 = %v, want 2", got)
	}
	if got := pageTwo["has_more"]; got != false {
		t.Fatalf("has_more page 2 = %v, want false", got)
	}
	if got := pageTwo["result_count"]; got != 2 {
		t.Fatalf("result_count page 2 = %v, want 2", got)
	}
	titlesPageTwo := resultTitles(t, pageTwo["results"])
	if len(titlesPageTwo) != 2 {
		t.Fatalf("len(results) page 2 = %d, want 2", len(titlesPageTwo))
	}
	if titlesPageTwo[0] != "list task 02" || titlesPageTwo[1] != "list task 01" {
		t.Fatalf("unexpected page 2 titles: %v", titlesPageTwo)
	}
}

func TestSearchScheduledTasksKeywordQueryPaginates(t *testing.T) {
	app := newTestApp(t)
	seedScheduledTasks(t, app, 12, "alpha")
	seedScheduledTasks(t, app, 3, "beta")

	pageTwo, err := app.searchScheduledTasks("alpha", 2)
	if err != nil {
		t.Fatalf("search alpha page 2: %v", err)
	}
	if got := pageTwo["search_mode"]; got != "basic_keyword" {
		t.Fatalf("search_mode = %v, want basic_keyword", got)
	}
	if got := pageTwo["page"]; got != 2 {
		t.Fatalf("page = %v, want 2", got)
	}
	if got := pageTwo["page_size"]; got != scheduledTaskToolPageSize {
		t.Fatalf("page_size = %v, want %d", got, scheduledTaskToolPageSize)
	}
	if got := pageTwo["has_more"]; got != false {
		t.Fatalf("has_more = %v, want false", got)
	}
	if got := pageTwo["result_count"]; got != 2 {
		t.Fatalf("result_count = %v, want 2", got)
	}
	titles := resultTitles(t, pageTwo["results"])
	if len(titles) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(titles))
	}
	for _, title := range titles {
		if !strings.Contains(title, "alpha") {
			t.Fatalf("title %q does not match alpha query", title)
		}
	}
}

func TestHandleScheduledTasksSearchUsesPagination(t *testing.T) {
	app := newTestApp(t)
	seedScheduledTasks(t, app, 12, "alpha")
	seedScheduledTasks(t, app, 3, "beta")

	req := httptest.NewRequest(http.MethodGet, "/api/scheduled-tasks?q=alpha&page=2&page_size=5", nil)
	rec := httptest.NewRecorder()

	app.handleScheduledTasks(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var out ScheduledTasksOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Query != "alpha" {
		t.Fatalf("query = %q, want alpha", out.Query)
	}
	if out.Page != 2 {
		t.Fatalf("page = %d, want 2", out.Page)
	}
	if out.PageSize != 5 {
		t.Fatalf("page_size = %d, want 5", out.PageSize)
	}
	if out.HasMore != true {
		t.Fatalf("has_more = %v, want true", out.HasMore)
	}
	if len(out.Tasks) != 5 {
		t.Fatalf("len(tasks) = %d, want 5", len(out.Tasks))
	}
	for _, task := range out.Tasks {
		if !strings.Contains(task.Title, "alpha") {
			t.Fatalf("task title %q does not match alpha query", task.Title)
		}
	}
}

func TestFilterScheduledTaskCandidatesRequiresCanonicalInboxState(t *testing.T) {
	task := ScheduledTaskItem{
		RequireInInbox: true,
	}
	candidates := []scheduledTaskEmailCandidate{
		{
			MessageID: "archived-with-stale-label",
			IsInInbox: false,
			LabelNames: []string{
				"INBOX",
				"Alerts",
			},
		},
		{
			MessageID: "inbox-message",
			IsInInbox: true,
			LabelNames: []string{
				"Alerts",
			},
		},
	}

	matched := filterScheduledTaskCandidates(task, candidates)
	if len(matched) != 1 {
		t.Fatalf("len(matched) = %d, want 1", len(matched))
	}
	if matched[0].MessageID != "inbox-message" {
		t.Fatalf("matched message = %q, want inbox-message", matched[0].MessageID)
	}
}

func TestFilterScheduledTaskCandidatesAppliesInboxAndLabelFilters(t *testing.T) {
	task := ScheduledTaskItem{
		RequireInInbox: true,
		LabelNames:     []string{"Alerts"},
	}
	candidates := []scheduledTaskEmailCandidate{
		{
			MessageID:  "inbox-alert",
			IsInInbox:  true,
			LabelNames: []string{"Alerts"},
		},
		{
			MessageID:  "archived-alert",
			IsInInbox:  false,
			LabelNames: []string{"Alerts"},
		},
		{
			MessageID:  "inbox-other-label",
			IsInInbox:  true,
			LabelNames: []string{"Other"},
		},
	}

	matched := filterScheduledTaskCandidates(task, candidates)
	if len(matched) != 1 {
		t.Fatalf("len(matched) = %d, want 1", len(matched))
	}
	if matched[0].MessageID != "inbox-alert" {
		t.Fatalf("matched message = %q, want inbox-alert", matched[0].MessageID)
	}
}

func TestFilterScheduledTaskCandidatesRequiresInboxAndNoUserLabels(t *testing.T) {
	task := ScheduledTaskItem{
		RequireNoUserLabels: true,
	}
	candidates := []scheduledTaskEmailCandidate{
		{
			MessageID:  "inbox-system-labels-only",
			IsInInbox:  true,
			LabelNames: nil,
		},
		{
			MessageID:  "archived-system-labels-only",
			IsInInbox:  false,
			LabelNames: nil,
		},
		{
			MessageID:  "inbox-user-label",
			IsInInbox:  true,
			LabelNames: []string{"Alerts"},
		},
	}

	matched := filterScheduledTaskCandidates(task, candidates)
	if len(matched) != 1 {
		t.Fatalf("len(matched) = %d, want 1", len(matched))
	}
	if matched[0].MessageID != "inbox-system-labels-only" {
		t.Fatalf("matched message = %q, want inbox-system-labels-only", matched[0].MessageID)
	}
}

func TestListScheduledTaskEmailCandidatesKeepsOnlyUserLabels(t *testing.T) {
	app := newTestApp(t)

	if _, err := app.db.Exec(`
		INSERT INTO synced_emails (message_id, thread_id, is_in_inbox, internal_date_unix)
		VALUES ('msg-system-only', 'thread-1', 1, 200), ('msg-user-label', 'thread-2', 1, 100)
	`); err != nil {
		t.Fatalf("insert emails: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO gmail_labels (label_id, label_name, label_type)
		VALUES
			('INBOX', 'INBOX', 'system'),
			('CATEGORY_UPDATES', 'Updates', 'system'),
			('Label_123', 'Alerts', 'user')
	`); err != nil {
		t.Fatalf("insert labels: %v", err)
	}
	if _, err := app.db.Exec(`
		INSERT INTO synced_email_labels (message_id, label_id)
		VALUES
			('msg-system-only', 'INBOX'),
			('msg-system-only', 'CATEGORY_UPDATES'),
			('msg-user-label', 'INBOX'),
			('msg-user-label', 'CATEGORY_UPDATES'),
			('msg-user-label', 'Label_123')
	`); err != nil {
		t.Fatalf("insert email labels: %v", err)
	}

	candidates, err := app.listScheduledTaskEmailCandidates([]string{"msg-system-only", "msg-user-label"})
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(candidates))
	}
	if candidates[0].MessageID != "msg-system-only" {
		t.Fatalf("first candidate = %q, want msg-system-only", candidates[0].MessageID)
	}
	if len(candidates[0].LabelNames) != 0 {
		t.Fatalf("system-only labels = %v, want none", candidates[0].LabelNames)
	}
	if candidates[1].MessageID != "msg-user-label" {
		t.Fatalf("second candidate = %q, want msg-user-label", candidates[1].MessageID)
	}
	if len(candidates[1].LabelNames) != 1 || candidates[1].LabelNames[0] != "Alerts" {
		t.Fatalf("user-label labels = %v, want [Alerts]", candidates[1].LabelNames)
	}
}

func TestCreateScheduledTaskUnlabelledClearsLabelNames(t *testing.T) {
	app := newTestApp(t)

	item, err := app.createScheduledTask("Unlabelled inbox", "Process unlabelled inbox emails.", true, false, true, []string{"Alerts"}, "test")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if !item.RequireNoUserLabels {
		t.Fatalf("RequireNoUserLabels = false, want true")
	}
	if !item.RequireInInbox {
		t.Fatalf("RequireInInbox = false, want true")
	}
	if len(item.LabelNames) != 0 {
		t.Fatalf("LabelNames = %v, want none", item.LabelNames)
	}
}

func TestBuildSearchScheduledTasksToolDefinitionIncludesListPagination(t *testing.T) {
	definition := buildSearchScheduledTasksToolDefinition()

	if !strings.Contains(definition.Description, "empty query lists all scheduled tasks") {
		t.Fatalf("description missing list-mode guidance: %q", definition.Description)
	}
	properties, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", definition.Parameters["properties"])
	}
	querySchema, ok := properties["query"].(map[string]any)
	if !ok {
		t.Fatalf("query schema type = %T, want map[string]any", properties["query"])
	}
	pageSchema, ok := properties["page"].(map[string]any)
	if !ok {
		t.Fatalf("page schema type = %T, want map[string]any", properties["page"])
	}
	if got := querySchema["type"]; got != "string" {
		t.Fatalf("query type = %v, want string", got)
	}
	if got := pageSchema["type"]; got != "integer" {
		t.Fatalf("page type = %v, want integer", got)
	}
	queryDescription, _ := querySchema["description"].(string)
	if !strings.Contains(queryDescription, "empty string to list all tasks") {
		t.Fatalf("query description missing empty-string guidance: %q", queryDescription)
	}
	pageDescription, _ := pageSchema["description"].(string)
	if !strings.Contains(pageDescription, "Each page returns up to 10 tasks") {
		t.Fatalf("page description missing fixed page size guidance: %q", pageDescription)
	}
	if _, hasRequired := definition.Parameters["required"]; hasRequired {
		t.Fatalf("required should be omitted for optional list-mode query")
	}
}

func TestBuildCreateScheduledTaskToolDefinitionIncludesUnlabelledFilter(t *testing.T) {
	definition := buildCreateScheduledTaskToolDefinition()
	properties, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", definition.Parameters["properties"])
	}
	unlabelledSchema, ok := properties["require_no_user_labels"].(map[string]any)
	if !ok {
		t.Fatalf("require_no_user_labels schema type = %T, want map[string]any", properties["require_no_user_labels"])
	}
	description, _ := unlabelledSchema["description"].(string)
	if !strings.Contains(description, "no user-created Gmail labels") {
		t.Fatalf("unlabelled description missing user-label semantics: %q", description)
	}
	if !strings.Contains(description, "system labels") {
		t.Fatalf("unlabelled description missing system-label guidance: %q", description)
	}
}

func TestScheduledTaskAgentDefinitionUsesMiniForOpenAI(t *testing.T) {
	agent, ok := getAgentDefinition("openai:gpt-5.4")
	if !ok {
		t.Fatal("openai agent definition not found")
	}

	scheduled := scheduledTaskAgentDefinition(agent)
	if scheduled.AgentID != scheduledTaskOpenAIAgentID {
		t.Fatalf("scheduled agent id = %q, want %q", scheduled.AgentID, scheduledTaskOpenAIAgentID)
	}
	if scheduled.Model != "gpt-5.4-mini" {
		t.Fatalf("scheduled model = %q, want gpt-5.4-mini", scheduled.Model)
	}
}

func TestScheduledTaskAgentDefinitionUsesFlashLiteForGemini(t *testing.T) {
	agent, ok := getAgentDefinition("gemini:gemini-3-flash-preview")
	if !ok {
		t.Fatal("gemini agent definition not found")
	}

	scheduled := scheduledTaskAgentDefinition(agent)
	if scheduled.Provider != "gemini" {
		t.Fatalf("scheduled provider = %q, want gemini", scheduled.Provider)
	}
	if scheduled.Model != scheduledTaskGeminiModel {
		t.Fatalf("scheduled model = %q, want %q", scheduled.Model, scheduledTaskGeminiModel)
	}
	if scheduled.AuthMode != "google_oauth" {
		t.Fatalf("scheduled auth mode = %q, want google_oauth", scheduled.AuthMode)
	}
}

func TestScheduledTaskAgentDefinitionLeavesOtherProvidersUnchanged(t *testing.T) {
	agent, ok := getAgentDefinition("deepseek:deepseek-thinking")
	if !ok {
		t.Fatal("deepseek agent definition not found")
	}

	scheduled := scheduledTaskAgentDefinition(agent)
	if scheduled.AgentID != agent.AgentID {
		t.Fatalf("scheduled agent id = %q, want %q", scheduled.AgentID, agent.AgentID)
	}
	if scheduled.Model != agent.Model {
		t.Fatalf("scheduled model = %q, want %q", scheduled.Model, agent.Model)
	}
}
