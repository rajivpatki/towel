package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGmailUsersLabelsUpdateSendsPutRequest(t *testing.T) {
	var requestBody map[string]any
	var gotMethod string
	var gotPath string
	var gotAuth string
	var gotContentType string

	app := &App{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				gotMethod = req.Method
				gotPath = req.URL.Path
				gotAuth = req.Header.Get("Authorization")
				gotContentType = req.Header.Get("Content-Type")

				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read request body: %v", err)
				}
				if err := json.Unmarshal(body, &requestBody); err != nil {
					t.Fatalf("decode request body: %v", err)
				}

				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"id":"Label_123","name":"Newsletters/Archive"}`)),
				}, nil
			}),
		},
	}

	result, status, err := app.gmailUsersLabelsUpdate("test-token", map[string]any{
		"userId":              "me",
		"id":                  "Label_123",
		"name":                "Newsletters/Archive",
		"labelListVisibility": "labelShow",
		"color": map[string]any{
			"textColor":       "#ffffff",
			"backgroundColor": "#16a766",
		},
	})
	if err != nil {
		t.Fatalf("gmailUsersLabelsUpdate returned error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if result != `{"id":"Label_123","name":"Newsletters/Archive"}` {
		t.Fatalf("result = %s", result)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method = %s, want %s", gotMethod, http.MethodPut)
	}
	if gotPath != "/gmail/v1/users/me/labels/Label_123" {
		t.Fatalf("path = %s, want /gmail/v1/users/me/labels/Label_123", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want bearer token", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}
	if _, exists := requestBody["userId"]; exists {
		t.Fatalf("request body included userId: %#v", requestBody)
	}
	if _, exists := requestBody["id"]; exists {
		t.Fatalf("request body included id: %#v", requestBody)
	}
	if requestBody["name"] != "Newsletters/Archive" {
		t.Fatalf("body name = %#v, want Newsletters/Archive", requestBody["name"])
	}
	if requestBody["labelListVisibility"] != "labelShow" {
		t.Fatalf("body labelListVisibility = %#v, want labelShow", requestBody["labelListVisibility"])
	}
	color, ok := requestBody["color"].(map[string]any)
	if !ok {
		t.Fatalf("body color type = %T, want object", requestBody["color"])
	}
	if color["textColor"] != "#ffffff" || color["backgroundColor"] != "#16a766" {
		t.Fatalf("body color = %#v", color)
	}
}

func TestGmailUsersLabelsUpdateRequiresFields(t *testing.T) {
	app := &App{httpClient: http.DefaultClient}

	if _, _, err := app.gmailUsersLabelsUpdate("test-token", map[string]any{"userId": "me"}); err == nil {
		t.Fatal("gmailUsersLabelsUpdate without id returned nil error")
	}
	if _, _, err := app.gmailUsersLabelsUpdate("test-token", map[string]any{"userId": "me", "id": "Label_123"}); err == nil {
		t.Fatal("gmailUsersLabelsUpdate without update fields returned nil error")
	}
}

func TestGmailLabelUpdateToolRegisteredWithoutDelete(t *testing.T) {
	app := &App{}
	mapping := app.gmailToolMapping()
	if _, ok := mapping["users.labels.update"]; !ok {
		t.Fatal("users.labels.update missing from Gmail tool mapping")
	}
	if _, ok := mapping["users.labels.delete"]; ok {
		t.Fatal("users.labels.delete should not be registered")
	}

	hasUpdateDefinition := false
	for _, definition := range gmailToolDefinitions {
		switch definition.Name {
		case "users.labels.update":
			hasUpdateDefinition = true
			if definition.SafetyModel != "safe_write" {
				t.Fatalf("users.labels.update safety model = %q, want safe_write", definition.SafetyModel)
			}
			properties, ok := definition.Parameters["properties"].(map[string]any)
			if !ok {
				t.Fatalf("users.labels.update properties type = %T, want map", definition.Parameters["properties"])
			}
			for _, field := range []string{"id", "name", "labelListVisibility", "messageListVisibility", "color"} {
				if _, ok := properties[field]; !ok {
					t.Fatalf("users.labels.update missing property %q", field)
				}
			}
		case "users.labels.delete":
			t.Fatal("users.labels.delete should not be exposed as a tool definition")
		}
	}
	if !hasUpdateDefinition {
		t.Fatal("users.labels.update missing from Gmail tool definitions")
	}
}
