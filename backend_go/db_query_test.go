package main

import (
	"strings"
	"testing"
)

func TestExecuteSafeDBQueryAllowsKeywordText(t *testing.T) {
	app := newTestApp(t)

	response, err := app.executeSafeDBQuery(`SELECT 'update available' AS note`, 10)
	if err != nil {
		t.Fatalf("execute query containing keyword text: %v", err)
	}
	if response.RowCount != 1 {
		t.Fatalf("row count = %d, want 1", response.RowCount)
	}
	if got := response.Rows[0]["note"]; got != "update available" {
		t.Fatalf("note = %v, want update available", got)
	}
}

func TestExecuteSafeDBQueryRejectsPrivateTables(t *testing.T) {
	app := newTestApp(t)

	_, err := app.executeSafeDBQuery(`SELECT COUNT(*) FROM secret_records`, 10)
	if err == nil {
		t.Fatal("expected private table reference to be rejected")
	}
	if !strings.Contains(err.Error(), "forbidden table reference: secret_records") {
		t.Fatalf("error = %q, want forbidden table reference", err.Error())
	}
}

func TestExecuteSafeDBQueryRunsWithSQLiteQueryOnly(t *testing.T) {
	app := newTestApp(t)

	_, err := app.executeSafeDBQuery(`UPDATE email_sync_state SET synced_window_days = 999 WHERE id = 1`, 10)
	if err == nil {
		t.Fatal("expected SQLite to reject mutation in query_only mode")
	}

	var days int
	if scanErr := app.db.QueryRow(`SELECT synced_window_days FROM email_sync_state WHERE id = 1`).Scan(&days); scanErr != nil {
		t.Fatalf("read synced_window_days: %v", scanErr)
	}
	if days == 999 {
		t.Fatal("write query mutated email_sync_state despite query_only mode")
	}
}
