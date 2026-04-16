package main

import "strings"

func gmailDescription(summary string, examples ...string) string {
	parts := []string{strings.TrimSpace(summary)}
	if len(examples) > 0 {
		parts = append(parts, "Examples: "+strings.Join(examples, " | "))
	}
	return strings.Join(parts, "\n\n")
}

func gmailObjectSchema(description string, properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"description":          description,
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func gmailStringSchema(description string, enum ...string) map[string]any {
	schema := map[string]any{
		"type":        "string",
		"description": description,
	}
	if len(enum) > 0 {
		schema["enum"] = enum
	}
	return schema
}

func gmailIntegerSchema(description string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"description": description,
	}
}

func gmailBooleanSchema(description string) map[string]any {
	return map[string]any{
		"type":        "boolean",
		"description": description,
	}
}

func gmailStringArraySchema(description string, enum ...string) map[string]any {
	items := map[string]any{"type": "string"}
	if len(enum) > 0 {
		items["enum"] = enum
	}
	return map[string]any{
		"type":        "array",
		"description": description,
		"items":       items,
	}
}

func gmailUserIDSchema() map[string]any {
	return gmailStringSchema(gmailDescription(
		"Mailbox owner to operate on. Gmail accepts the special value `me` for the currently authenticated account, and some delegated or workspace scenarios can use the actual email address when the access token has authority for that mailbox.",
		`{"userId":"me"}`,
		`{"userId":"finance@example.com"}`,
	))
}

func gmailMessageIDSchema() map[string]any {
	return gmailStringSchema(gmailDescription(
		"Opaque Gmail message identifier for one specific message resource. This is not the RFC 822 Message-ID header; it is the Gmail API resource id returned from list or thread responses.",
		`{"id":"1966c2f4c8a1b123"}`,
		`{"id":"18c7f4dd91ef90ab"}`,
	))
}

func gmailThreadIDSchema() map[string]any {
	return gmailStringSchema(gmailDescription(
		"Opaque Gmail thread identifier for one conversation thread. Use the `id` returned by `users.threads.list` or the `threadId` attached to a message resource.",
		`{"id":"1966c2f4c8a1b123"}`,
		`{"id":"18f0a0bf934fe777"}`,
	))
}

var gmailToolDefinitions = []GmailToolDefinition{
	{
		Name:         "users.labels.list",
		GmailActions: []string{"users.labels.list"},
		Description: gmailDescription(
			"List every system and user-created label currently available in the target mailbox. Use this before applying labels, building filters, or explaining the mailbox taxonomy so the model can reference real label ids instead of guessing names.",
			`{"userId":"me"}`,
			`{"userId":"support@example.com"}`,
		),
		SafetyModel: "read_only",
		Parameters: gmailObjectSchema(
			"Parameters for `users.labels.list`. The Gmail API only exposes the mailbox owner path parameter for this operation.",
			map[string]any{
				"userId": gmailUserIDSchema(),
			},
		),
	},
	{
		Name:         "users.labels.create",
		GmailActions: []string{"users.labels.create"},
		Description: gmailDescription(
			"Create a Gmail label with optional visibility and color settings. Use this when the user wants a new organizational bucket before filters, inbox cleanup, or bulk relabeling actions are performed.",
			`{"userId":"me","name":"Newsletters/Keep","labelListVisibility":"labelShow","messageListVisibility":"show"}`,
			`{"userId":"me","name":"VIP Clients","color":{"textColor":"#ffffff","backgroundColor":"#16a766"}}`,
		),
		SafetyModel: "safe_write",
		Parameters: gmailObjectSchema(
			"Parameters for `users.labels.create`. This schema covers the Gmail API path parameter and the full request body accepted by the create-label endpoint.",
			map[string]any{
				"userId": gmailUserIDSchema(),
				"name": gmailStringSchema(gmailDescription(
					"Human-readable label name to create. Nested names can be expressed with `/` to create a hierarchy-style path in Gmail's UI, and the name must be unique within the mailbox.",
					`{"name":"Receipts"}`,
					`{"name":"Projects/Alpha"}`,
				)),
				"messageListVisibility": gmailStringSchema(gmailDescription(
					"Controls whether conversations with this label appear in the message list. Use `show` when the label should remain visible in conversation rows, or `hide` when the label is only for background classification.",
					`{"messageListVisibility":"show"}`,
					`{"messageListVisibility":"hide"}`,
				), "show", "hide"),
				"labelListVisibility": gmailStringSchema(gmailDescription(
					"Controls how the label appears in Gmail's left navigation label list. `labelShow` always shows it, `labelShowIfUnread` shows it only when unread conversations exist, and `labelHide` keeps it out of the main list.",
					`{"labelListVisibility":"labelShow"}`,
					`{"labelListVisibility":"labelShowIfUnread"}`,
				), "labelShow", "labelShowIfUnread", "labelHide"),
				"color": gmailObjectSchema(
					gmailDescription(
						"Optional label color object. Gmail expects supported hex color values for text and background, and invalid combinations may be rejected by the API.",
						`{"color":{"textColor":"#ffffff","backgroundColor":"#16a766"}}`,
						`{"color":{"textColor":"#434343","backgroundColor":"#fce8b2"}}`,
					),
					map[string]any{
						"textColor": gmailStringSchema(gmailDescription(
							"Foreground text color for the label chip, expressed as a hex string. Pick a value with enough contrast against the chosen background.",
							`{"textColor":"#ffffff"}`,
							`{"textColor":"#434343"}`,
						)),
						"backgroundColor": gmailStringSchema(gmailDescription(
							"Background fill color for the label chip, expressed as a hex string supported by Gmail's palette.",
							`{"backgroundColor":"#16a766"}`,
							`{"backgroundColor":"#fce8b2"}`,
						)),
					},
				),
			},
			"name",
		),
	},
	{
		Name:         "users.messages.list",
		GmailActions: []string{"users.messages.list"},
		Description: gmailDescription(
			"Search or page through individual messages in a mailbox. This is the primary discovery tool for inbox triage because it supports Gmail query syntax, label filtering, spam or trash inclusion, and pagination over large result sets.",
			`{"userId":"me","q":"from:github newer_than:7d","maxResults":25}`,
			`{"userId":"me","labelIds":["UNREAD","CATEGORY_PROMOTIONS"],"includeSpamTrash":false}`, 
		),
		SafetyModel: "read_only",
		Parameters: gmailObjectSchema(
			"Parameters for `users.messages.list`. This schema covers the full set of Gmail API query parameters plus the mailbox owner path parameter.",
			map[string]any{
				"userId": gmailUserIDSchema(),
				"includeSpamTrash": gmailBooleanSchema(gmailDescription(
					"Whether search results may include messages from Spam and Trash. Use `true` only when the user explicitly wants a complete search across ignored or deleted mail.",
					`{"includeSpamTrash":true}`,
					`{"includeSpamTrash":false}`,
				)),
				"labelIds": gmailStringArraySchema(gmailDescription(
					"Restrict results to messages carrying all of the listed label ids. Supply Gmail system labels such as `UNREAD` or `INBOX`, or user label ids returned by the labels endpoint.",
					`{"labelIds":["UNREAD"]}`,
					`{"labelIds":["Label_1234567890","IMPORTANT"]}`,
				)),
				"maxResults": gmailIntegerSchema(gmailDescription(
					"Maximum number of messages to return in one page. Gmail accepts values up to 500, and smaller page sizes are useful when the model only needs a sample or a first-pass triage batch.",
					`{"maxResults":10}`,
					`{"maxResults":100}`,
				)),
				"pageToken": gmailStringSchema(gmailDescription(
					"Opaque continuation token from a previous list response. Pass it unchanged to fetch the next page when Gmail indicates there are more results.",
					`{"pageToken":"089123abc_next_page"}`,
					`{"pageToken":"16c0f0d4f4_page_2"}`,
				)),
				"q": gmailStringSchema(gmailDescription(
					"Gmail search query using the same operators as the Gmail web UI. Combine senders, labels, time ranges, categories, attachment operators, and boolean search terms to target exactly the mail you need.",
					`{"q":"from:stripe newer_than:30d has:attachment"}`,
					`{"q":"category:promotions older_than:90d -label:keep"}`,
				)),
			},
		),
	},
	{
		Name:         "users.messages.get",
		GmailActions: []string{"users.messages.get"},
		Description: gmailDescription(
			"Fetch one message by id, optionally controlling how much payload Gmail returns. Use `minimal` for cheap state checks, `metadata` for headers only, `full` for parsed MIME payload, and `raw` when the original RFC 822 source is needed.",
			`{"userId":"me","id":"1966c2f4c8a1b123","format":"metadata","metadataHeaders":["Subject","From"]}`,
			`{"userId":"me","id":"18c7f4dd91ef90ab","format":"raw"}`,
		),
		SafetyModel: "read_only",
		Parameters: gmailObjectSchema(
			"Parameters for `users.messages.get`. This schema covers the path parameters and all Gmail API query parameters for retrieving a message.",
			map[string]any{
				"userId": gmailUserIDSchema(),
				"id":     gmailMessageIDSchema(),
				"format": gmailStringSchema(gmailDescription(
					"Response projection to return. `minimal` returns only ids and labels, `metadata` returns selected headers, `full` returns the parsed payload, and `raw` returns the base64url-encoded full source message.",
					`{"format":"full"}`,
					`{"format":"metadata"}`,
				), "full", "metadata", "minimal", "raw"),
				"metadataHeaders": gmailStringArraySchema(gmailDescription(
					"List of header names to include when `format` is `metadata`. Use canonical email header names exactly as you want them returned by Gmail.",
					`{"metadataHeaders":["Subject","From"]}`,
					`{"metadataHeaders":["List-Unsubscribe","Delivered-To"]}`,
				)),
			},
			"id",
		),
	},
	{
		Name:         "users.messages.batchModify",
		GmailActions: []string{"users.messages.batchModify"},
		Description: gmailDescription(
			"Add or remove labels across many messages in one request. This is the right tool for bulk archive, mark-as-read, tagging, or cleanup workflows after search results have already identified the target message ids.",
			`{"userId":"me","ids":["1966c2f4c8a1b123","18c7f4dd91ef90ab"],"removeLabelIds":["UNREAD"]}`,
			`{"userId":"me","ids":["1966c2f4c8a1b123"],"addLabelIds":["Label_1234567890"],"removeLabelIds":["INBOX"]}`,
		),
		SafetyModel: "safe_write",
		Parameters: gmailObjectSchema(
			"Parameters for `users.messages.batchModify`. This schema covers the mailbox owner path parameter and the complete Gmail API request body for batch label mutation.",
			map[string]any{
				"userId": gmailUserIDSchema(),
				"ids": gmailStringArraySchema(gmailDescription(
					"Array of Gmail message resource ids to mutate. Gmail allows batch operations over many ids in one request, making this much cheaper than calling modify repeatedly.",
					`{"ids":["1966c2f4c8a1b123","18c7f4dd91ef90ab"]}`,
					`{"ids":["18c7f4dd91ef90ab"]}`,
				)),
				"addLabelIds": gmailStringArraySchema(gmailDescription(
					"Label ids to attach to every message in `ids`. Use system labels such as `STARRED` where allowed, or user label ids returned by the labels endpoint.",
					`{"addLabelIds":["Label_1234567890"]}`,
					`{"addLabelIds":["STARRED","IMPORTANT"]}`,
				)),
				"removeLabelIds": gmailStringArraySchema(gmailDescription(
					"Label ids to remove from every targeted message. Common uses include removing `UNREAD`, removing `INBOX` to archive, or removing an obsolete custom label.",
					`{"removeLabelIds":["UNREAD"]}`,
					`{"removeLabelIds":["INBOX","Label_999999"]}`,
				)),
			},
			"ids",
		),
	},
	{
		Name:         "users.messages.modify",
		GmailActions: []string{"users.messages.modify"},
		Description: gmailDescription(
			"Add or remove labels on exactly one message. Use this for precise state changes when the user is acting on a single email rather than a bulk result set.",
			`{"userId":"me","id":"1966c2f4c8a1b123","removeLabelIds":["UNREAD"]}`,
			`{"userId":"me","id":"18c7f4dd91ef90ab","addLabelIds":["Label_1234567890"],"removeLabelIds":["INBOX"]}`,
		),
		SafetyModel: "safe_write",
		Parameters: gmailObjectSchema(
			"Parameters for `users.messages.modify`. This schema covers the path parameters and the full Gmail API request body for single-message label mutation.",
			map[string]any{
				"userId":        gmailUserIDSchema(),
				"id":            gmailMessageIDSchema(),
				"addLabelIds":   gmailStringArraySchema(gmailDescription("Label ids to add to the target message. Use this to tag or classify one message without touching the rest of the conversation or the rest of the search results.", `{"addLabelIds":["STARRED"]}`, `{"addLabelIds":["Label_1234567890","IMPORTANT"]}`)),
				"removeLabelIds": gmailStringArraySchema(gmailDescription("Label ids to remove from the target message. This is commonly used to mark the mail as read by removing `UNREAD`, or to archive it by removing `INBOX`.", `{"removeLabelIds":["UNREAD"]}`, `{"removeLabelIds":["INBOX","Label_999999"]}`)),
			},
			"id",
		),
	},
	{
		Name:         "users.settings.filters.create",
		GmailActions: []string{"users.settings.filters.create"},
		Description: gmailDescription(
			"Create a persistent Gmail filter rule. Filters match incoming mail using criteria and then apply actions such as attaching labels, skipping inbox by removing `INBOX`, or forwarding to another address already verified in Gmail settings.",
			`{"userId":"me","criteria":{"from":"billing@vendor.com","hasAttachment":true},"action":{"addLabelIds":["Label_1234567890"],"removeLabelIds":["INBOX"]}}`,
			`{"userId":"me","criteria":{"query":"list:weekly@updates.example.com"},"action":{"removeLabelIds":["INBOX"],"forward":"archive@example.com"}}`,
		),
		SafetyModel: "safe_write",
		Parameters: gmailObjectSchema(
			"Parameters for `users.settings.filters.create`. This schema covers the mailbox owner path parameter and the full Gmail API request body with nested `criteria` and `action` objects.",
			map[string]any{
				"userId": gmailUserIDSchema(),
				"criteria": gmailObjectSchema(
					gmailDescription(
						"Conditions Gmail evaluates against incoming messages. You can combine direct fields such as `from` or `subject` with richer search syntax in `query` and exclusion logic in `negatedQuery`.",
						`{"criteria":{"from":"billing@vendor.com","subject":"invoice"}}`,
						`{"criteria":{"query":"category:promotions older_than:30d","excludeChats":true}}`,
					),
					map[string]any{
						"from": gmailStringSchema(gmailDescription("Match messages from a specific sender address or phrase in the From header.", `{"from":"billing@vendor.com"}`, `{"from":"newsletter@example.com"}`)),
						"to": gmailStringSchema(gmailDescription("Match messages addressed to a specific recipient value in the To header.", `{"to":"support@example.com"}`, `{"to":"inbox+orders@example.com"}`)),
						"subject": gmailStringSchema(gmailDescription("Match messages whose subject contains the given text. This is helpful for predictable automated mail such as invoices or alerts.", `{"subject":"invoice"}`, `{"subject":"build failed"}`)),
						"query": gmailStringSchema(gmailDescription("Advanced Gmail search syntax that must match for the filter to fire. This supports the same operators as Gmail search, including labels, categories, size filters, and boolean terms.", `{"query":"has:attachment filename:pdf"}`, `{"query":"category:promotions older_than:30d"}`)),
						"negatedQuery": gmailStringSchema(gmailDescription("Advanced Gmail search syntax that must not match. Use this to carve out exceptions from a broader filter definition.", `{"negatedQuery":"label:keep"}`, `{"negatedQuery":"from:ceo@example.com"}`)),
						"hasAttachment": gmailBooleanSchema(gmailDescription("Whether the message must include at least one attachment.", `{"hasAttachment":true}`, `{"hasAttachment":false}`)),
						"excludeChats": gmailBooleanSchema(gmailDescription("Whether chat conversations should be excluded from matching. This is useful when you only want email messages and never Google Chat transcripts.", `{"excludeChats":true}`, `{"excludeChats":false}`)),
						"size": gmailIntegerSchema(gmailDescription("Message size threshold in bytes used together with `sizeComparison`.", `{"size":500000}`, `{"size":10485760}`)),
						"sizeComparison": gmailStringSchema(gmailDescription("How Gmail should compare the message size against `size`. Use `larger` for messages above the threshold or `smaller` for messages below it.", `{"sizeComparison":"larger"}`, `{"sizeComparison":"smaller"}`), "larger", "smaller"),
					},
				),
				"action": gmailObjectSchema(
					gmailDescription(
						"Actions Gmail should apply when the criteria match. The most common actions are attaching labels, removing `INBOX` to archive, and forwarding mail to a pre-approved address.",
						`{"action":{"addLabelIds":["Label_1234567890"],"removeLabelIds":["INBOX"]}}`,
						`{"action":{"forward":"archive@example.com"}}`,
					),
					map[string]any{
						"addLabelIds": gmailStringArraySchema(gmailDescription("Label ids Gmail should add when a message matches the filter.", `{"addLabelIds":["IMPORTANT"]}`, `{"addLabelIds":["Label_1234567890","STARRED"]}`)),
						"removeLabelIds": gmailStringArraySchema(gmailDescription("Label ids Gmail should remove when a message matches the filter. Removing `INBOX` is the normal way to skip the inbox and archive matching mail.", `{"removeLabelIds":["INBOX"]}`, `{"removeLabelIds":["UNREAD","INBOX"]}`)),
						"forward": gmailStringSchema(gmailDescription("Verified forwarding address that Gmail should send matching mail to. The destination must already be configured in the Gmail account settings.", `{"forward":"archive@example.com"}`, `{"forward":"ops-team@example.com"}`)),
					},
				),
			},
			"action",
		),
	},
	{
		Name:         "users.threads.list",
		GmailActions: []string{"users.threads.list"},
		Description: gmailDescription(
			"Search or page through conversation threads instead of individual messages. Use this when the user thinks in conversations and you want one result per email thread rather than one result per message.",
			`{"userId":"me","q":"label:inbox newer_than:14d","maxResults":20}`,
			`{"userId":"me","labelIds":["UNREAD"],"includeSpamTrash":false}`, 
		),
		SafetyModel: "read_only",
		Parameters: gmailObjectSchema(
			"Parameters for `users.threads.list`. This schema covers the full set of Gmail API query parameters plus the mailbox owner path parameter.",
			map[string]any{
				"userId":           gmailUserIDSchema(),
				"includeSpamTrash": gmailBooleanSchema(gmailDescription("Whether search results may include threads from Spam and Trash.", `{"includeSpamTrash":true}`, `{"includeSpamTrash":false}`)),
				"labelIds":         gmailStringArraySchema(gmailDescription("Restrict results to threads carrying all of the specified label ids.", `{"labelIds":["UNREAD"]}`, `{"labelIds":["Label_1234567890","IMPORTANT"]}`)),
				"maxResults":       gmailIntegerSchema(gmailDescription("Maximum number of threads to return in one page, up to Gmail's API limit.", `{"maxResults":10}`, `{"maxResults":100}`)),
				"pageToken":        gmailStringSchema(gmailDescription("Opaque continuation token from a previous thread list response.", `{"pageToken":"page_2_token"}`, `{"pageToken":"next_threads_abc"}`)),
				"q":                gmailStringSchema(gmailDescription("Gmail search query for conversation threads using standard Gmail operators.", `{"q":"from:manager@example.com has:attachment"}`, `{"q":"category:social older_than:180d"}`)),
			},
		),
	},
	{
		Name:         "users.threads.get",
		GmailActions: []string{"users.threads.get"},
		Description: gmailDescription(
			"Fetch one conversation thread by id, optionally controlling the returned detail level. This is useful after `users.threads.list` when you need the complete conversation context or only a lightweight metadata view.",
			`{"userId":"me","id":"18f0a0bf934fe777","format":"metadata","metadataHeaders":["Subject","From"]}`,
			`{"userId":"me","id":"1966c2f4c8a1b123","format":"full"}`,
		),
		SafetyModel: "read_only",
		Parameters: gmailObjectSchema(
			"Parameters for `users.threads.get`. This schema covers the path parameters and the full Gmail API query surface for thread retrieval.",
			map[string]any{
				"userId": gmailUserIDSchema(),
				"id":     gmailThreadIDSchema(),
				"format": gmailStringSchema(gmailDescription("Response projection for the thread. `minimal` keeps the response compact, `metadata` returns selected headers, and `full` returns the parsed message payloads in the thread.", `{"format":"minimal"}`, `{"format":"full"}`), "full", "metadata", "minimal"),
				"metadataHeaders": gmailStringArraySchema(gmailDescription("Header names to include when `format` is `metadata`.", `{"metadataHeaders":["Subject","From"]}`, `{"metadataHeaders":["Delivered-To","List-Unsubscribe"]}`)),
			},
			"id",
		),
	},
}
