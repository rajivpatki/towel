package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var errGmailMessageNotFound = errors.New("gmail message not found")

type gmailMessageResource struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	HistoryID    string   `json:"historyId"`
	InternalDate string   `json:"internalDate"`
	Snippet      string   `json:"snippet"`
	LabelIDs     []string `json:"labelIds"`
	SizeEstimate int64    `json:"sizeEstimate"`
	Payload      struct {
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
		MimeType string `json:"mimeType"`
		Filename string `json:"filename"`
		Body     struct {
			Size         int64  `json:"size"`
			Data         string `json:"data"`
			AttachmentID string `json:"attachmentId"`
		} `json:"body"`
		Parts []gmailMessagePart `json:"parts"`
	} `json:"payload"`
}

type gmailMessagePart struct {
	PartID   string `json:"partId"`
	MimeType string `json:"mimeType"`
	Filename string `json:"filename"`
	Headers  []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
	Body struct {
		Size         int64  `json:"size"`
		Data         string `json:"data"`
		AttachmentID string `json:"attachmentId"`
	} `json:"body"`
	Parts []gmailMessagePart `json:"parts"`
}

type gmailMessageListResponse struct {
	Messages []struct {
		ID string `json:"id"`
	} `json:"messages"`
	NextPageToken string `json:"nextPageToken"`
}

type gmailLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type gmailLabelsListResponse struct {
	Labels []gmailLabel `json:"labels"`
}

type gmailHistoryListResponse struct {
	History []struct {
		ID            string `json:"id"`
		MessagesAdded []struct {
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
		} `json:"messagesAdded"`
		MessagesDeleted []struct {
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
		} `json:"messagesDeleted"`
		LabelsAdded []struct {
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
		} `json:"labelsAdded"`
		LabelsRemoved []struct {
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
		} `json:"labelsRemoved"`
	} `json:"history"`
	HistoryID     string `json:"historyId"`
	NextPageToken string `json:"nextPageToken"`
}

type historyChangeSet struct {
	Upsert  map[string]struct{}
	Deleted map[string]struct{}
}

type syncedEmailRecord struct {
	MessageID           string
	ThreadID            string
	HistoryID           string
	Subject             string
	SubjectNormalized   string
	Snippet             string
	FromName            string
	FromEmail           string
	FromRaw             string
	ReplyTo             string
	ToAddresses         string
	CcAddresses         string
	BccAddresses        string
	DeliveredTo         string
	DateHeader          string
	InternalDateUnix    int64
	InternalDate        string
	SizeEstimate        int64
	BodyText            string
	BodyHTML            string
	BodySizeEstimate    int64
	AttachmentCount     int
	AttachmentNamesJSON string
	AttachmentTotalSize int64
	LabelIDsJSON        string
	LabelIDs            []string
	ListUnsubscribe     string
	ListUnsubscribePost string
	ListID              string
	PrecedenceHeader    string
	AutoSubmittedHeader string
	FeedbackID          string
	InReplyTo           string
	ReferencesHeader    string
	IsUnread            bool
	IsStarred           bool
	IsImportant         bool
	IsInInbox           bool
	IsInSpam            bool
	IsInTrash           bool
	HasAttachments      bool
	Attachments         []syncedEmailAttachment
}

type syncedEmailAttachment struct {
	Filename     string
	MimeType     string
	SizeEstimate int64
	AttachmentID string
}

func (a *App) doGmailJSONRequest(method string, apiPath string, params url.Values, payload any, target any) (int, error) {
	var requestBody []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		requestBody = encoded
	}
	execute := func(accessToken string) ([]byte, int, error) {
		requestURL := gmailAPIBase + apiPath
		if len(params) > 0 {
			requestURL += "?" + params.Encode()
		}
		log.Printf("gmail request starting: method=%s path=%s", method, requestURL)
		var bodyReader *bytes.Reader
		if requestBody == nil {
			bodyReader = bytes.NewReader(nil)
		} else {
			bodyReader = bytes.NewReader(requestBody)
		}
		req, err := http.NewRequest(method, requestURL, bodyReader)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		body, statusCode, err := a.doGmailRequest(req)
		if err != nil {
			log.Printf("gmail request transport failed: method=%s path=%s err=%v", method, requestURL, err)
			return body, statusCode, err
		}
		log.Printf("gmail request finished: method=%s path=%s status=%d", method, requestURL, statusCode)
		return body, statusCode, nil
	}
	log.Printf("gmail auth: requesting access token for api_path=%s", apiPath)
	accessToken, err := a.getGmailAccessToken()
	if err != nil {
		log.Printf("gmail auth failed: api_path=%s err=%v", apiPath, err)
		return 0, err
	}
	log.Printf("gmail auth successful: api_path=%s", apiPath)
	body, statusCode, err := execute(accessToken)
	if err == nil && statusCode == http.StatusNotFound && strings.Contains(apiPath, "/history") {
		log.Printf("gmail history 404 detected: api_path=%s status=%d", apiPath, statusCode)
		return statusCode, errEmailHistoryExpired
	}
	if err == nil && (statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden) {
		log.Printf("gmail request unauthorized/forbidden; attempting token refresh: api_path=%s status=%d", apiPath, statusCode)
		refreshedToken, refreshErr := a.refreshGmailToken()
		if refreshErr != nil {
			log.Printf("gmail token refresh failed: api_path=%s err=%v", apiPath, refreshErr)
			return statusCode, refreshErr
		}
		log.Printf("gmail token refresh successful: api_path=%s", apiPath)
		body, statusCode, err = execute(refreshedToken)
		if err == nil && statusCode == http.StatusNotFound && strings.Contains(apiPath, "/history") {
			log.Printf("gmail history 404 detected after token refresh: api_path=%s status=%d", apiPath, statusCode)
			return statusCode, errEmailHistoryExpired
		}
	}
	if err != nil {
		return statusCode, err
	}
	if statusCode >= 400 {
		return statusCode, fmt.Errorf("gmail request failed: %s", strings.TrimSpace(string(body)))
	}
	if target != nil {
		if err := json.Unmarshal(body, target); err != nil {
			return statusCode, err
		}
	}
	return statusCode, nil
}

func (a *App) syncRecentMessagesWindow(windowDays int) ([]string, string, error) {
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	cutoffUnixMillis := time.Now().Add(-time.Duration(windowDays) * 24 * time.Hour).UnixMilli()
	log.Printf("email full sync listing recent messages: window_days=%d cutoff_unix_ms=%d cutoff_rfc3339=%s", windowDays, cutoffUnixMillis, time.UnixMilli(cutoffUnixMillis).UTC().Format(time.RFC3339))
	pageToken := ""
	pageNumber := 1
	cursorHistoryID := ""
	for {
		params := url.Values{}
		params.Set("includeSpamTrash", "true")
		params.Set("maxResults", "100")
		if pageToken != "" {
			params.Set("pageToken", pageToken)
		}
		log.Printf("email full sync listing page: page=%d page_token_present=%t", pageNumber, pageToken != "")
		var response gmailMessageListResponse
		if _, err := a.doGmailJSONRequest(http.MethodGet, "/users/me/messages", params, nil, &response); err != nil {
			return nil, "", err
		}
		log.Printf("email full sync page loaded: page=%d candidates=%d next_page_token_present=%t", pageNumber, len(response.Messages), strings.TrimSpace(response.NextPageToken) != "")
		pageHasRecentMessages := false
		for _, message := range response.Messages {
			if _, ok := seen[message.ID]; ok || strings.TrimSpace(message.ID) == "" {
				continue
			}
			fetchedMessage, err := a.fetchGmailMessage(message.ID)
			if err != nil {
				return nil, "", err
			}
			internalDateUnix, _ := strconv.ParseInt(strings.TrimSpace(fetchedMessage.InternalDate), 10, 64)
			if internalDateUnix < cutoffUnixMillis {
				log.Printf("email full sync skipping old message: message_id=%s internal_date=%s", fetchedMessage.ID, unixMillisToRFC3339(internalDateUnix))
				continue
			}
			pageHasRecentMessages = true
			if cursorHistoryID == "" {
				cursorHistoryID = strings.TrimSpace(fetchedMessage.HistoryID)
			}
			if _, err := a.upsertSyncedEmail(fetchedMessage); err != nil {
				return nil, "", err
			}
			seen[fetchedMessage.ID] = struct{}{}
			ids = append(ids, fetchedMessage.ID)
			log.Printf("email full sync accepted and upserted message: message_id=%s internal_date=%s accepted_count=%d", fetchedMessage.ID, unixMillisToRFC3339(internalDateUnix), len(ids))
		}
		if strings.TrimSpace(response.NextPageToken) == "" || !pageHasRecentMessages {
			if !pageHasRecentMessages {
				log.Printf("email full sync stopping pagination: page=%d reason=no_recent_messages_on_page", pageNumber)
			}
			break
		}
		pageToken = response.NextPageToken
		pageNumber++
	}
	log.Printf("email full sync listing completed: retained_ids=%d", len(ids))
	return ids, cursorHistoryID, nil
}

func (a *App) fetchGmailMessage(messageID string) (gmailMessageResource, error) {
	params := url.Values{}
	params.Set("format", "full")
	var message gmailMessageResource
	statusCode, err := a.doGmailJSONRequest(http.MethodGet, "/users/me/messages/"+url.PathEscape(messageID), params, nil, &message)
	if err != nil {
		if statusCode == http.StatusNotFound {
			return gmailMessageResource{}, errGmailMessageNotFound
		}
		return gmailMessageResource{}, err
	}
	return message, nil
}

func (a *App) fetchAndCacheGmailLabels() error {
	var response gmailLabelsListResponse
	if _, err := a.doGmailJSONRequest(http.MethodGet, "/users/me/labels", nil, nil, &response); err != nil {
		return err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if _, err := tx.Exec(`DELETE FROM gmail_labels`); err != nil {
		return err
	}
	for _, label := range response.Labels {
		if _, err := tx.Exec(`INSERT INTO gmail_labels (label_id, label_name, label_type) VALUES (?, ?, ?)`, label.ID, label.Name, label.Type); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("gmail labels cached: count=%d", len(response.Labels))
	return nil
}

func (a *App) listGmailHistoryChanges(cursor string) (historyChangeSet, string, error) {
	changes := historyChangeSet{Upsert: make(map[string]struct{}), Deleted: make(map[string]struct{})}
	pageToken := ""
	lastHistoryID := ""
	for {
		params := url.Values{}
		params.Set("startHistoryId", cursor)
		params.Set("maxResults", "200")
		if pageToken != "" {
			params.Set("pageToken", pageToken)
		}
		var response gmailHistoryListResponse
		_, err := a.doGmailJSONRequest(http.MethodGet, "/users/me/history", params, nil, &response)
		if err != nil {
			return historyChangeSet{}, "", err
		}
		if strings.TrimSpace(response.HistoryID) != "" {
			lastHistoryID = strings.TrimSpace(response.HistoryID)
		}
		for _, item := range response.History {
			if strings.TrimSpace(item.ID) != "" {
				lastHistoryID = strings.TrimSpace(item.ID)
			}
			for _, entry := range item.MessagesAdded {
				changes.Upsert[strings.TrimSpace(entry.Message.ID)] = struct{}{}
			}
			for _, entry := range item.LabelsAdded {
				changes.Upsert[strings.TrimSpace(entry.Message.ID)] = struct{}{}
			}
			for _, entry := range item.LabelsRemoved {
				changes.Upsert[strings.TrimSpace(entry.Message.ID)] = struct{}{}
			}
			for _, entry := range item.MessagesDeleted {
				changes.Deleted[strings.TrimSpace(entry.Message.ID)] = struct{}{}
			}
		}
		if strings.TrimSpace(response.NextPageToken) == "" {
			break
		}
		pageToken = response.NextPageToken
	}
	for messageID := range changes.Deleted {
		delete(changes.Upsert, messageID)
	}
	return changes, lastHistoryID, nil
}

func (a *App) upsertSyncedEmail(message gmailMessageResource) (syncedEmailRecord, error) {
	record := buildSyncedEmailRecord(message)
	tx, err := a.db.Begin()
	if err != nil {
		return syncedEmailRecord{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	_, err = tx.Exec(`
		INSERT INTO synced_emails (
			message_id, thread_id, history_id, subject, subject_normalized, snippet,
			from_name, from_email, from_raw, reply_to, to_addresses, cc_addresses,
			bcc_addresses, delivered_to, date_header, internal_date_unix, internal_date,
			size_estimate, body_text, body_html, body_size_estimate, attachment_count,
			attachment_names, attachment_total_size, label_ids, list_unsubscribe,
			list_unsubscribe_post, list_id, precedence_header, auto_submitted_header,
			feedback_id, in_reply_to, references_header, is_unread, is_starred,
			is_important, is_in_inbox, is_in_spam, is_in_trash, has_attachments,
			is_deleted, deleted_at, sync_updated_at
		) VALUES (
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, CURRENT_TIMESTAMP
		)
		ON CONFLICT(message_id) DO UPDATE SET
			thread_id = excluded.thread_id,
			history_id = excluded.history_id,
			subject = excluded.subject,
			subject_normalized = excluded.subject_normalized,
			snippet = excluded.snippet,
			from_name = excluded.from_name,
			from_email = excluded.from_email,
			from_raw = excluded.from_raw,
			reply_to = excluded.reply_to,
			to_addresses = excluded.to_addresses,
			cc_addresses = excluded.cc_addresses,
			bcc_addresses = excluded.bcc_addresses,
			delivered_to = excluded.delivered_to,
			date_header = excluded.date_header,
			internal_date_unix = excluded.internal_date_unix,
			internal_date = excluded.internal_date,
			size_estimate = excluded.size_estimate,
			body_text = excluded.body_text,
			body_html = excluded.body_html,
			body_size_estimate = excluded.body_size_estimate,
			attachment_count = excluded.attachment_count,
			attachment_names = excluded.attachment_names,
			attachment_total_size = excluded.attachment_total_size,
			label_ids = excluded.label_ids,
			list_unsubscribe = excluded.list_unsubscribe,
			list_unsubscribe_post = excluded.list_unsubscribe_post,
			list_id = excluded.list_id,
			precedence_header = excluded.precedence_header,
			auto_submitted_header = excluded.auto_submitted_header,
			feedback_id = excluded.feedback_id,
			in_reply_to = excluded.in_reply_to,
			references_header = excluded.references_header,
			is_unread = excluded.is_unread,
			is_starred = excluded.is_starred,
			is_important = excluded.is_important,
			is_in_inbox = excluded.is_in_inbox,
			is_in_spam = excluded.is_in_spam,
			is_in_trash = excluded.is_in_trash,
			has_attachments = excluded.has_attachments,
			is_deleted = 0,
			deleted_at = NULL,
			sync_updated_at = CURRENT_TIMESTAMP
	`,
		record.MessageID,
		record.ThreadID,
		nullIfEmpty(record.HistoryID),
		nullIfEmpty(record.Subject),
		nullIfEmpty(record.SubjectNormalized),
		nullIfEmpty(record.Snippet),
		nullIfEmpty(record.FromName),
		nullIfEmpty(record.FromEmail),
		nullIfEmpty(record.FromRaw),
		nullIfEmpty(record.ReplyTo),
		nullIfEmpty(record.ToAddresses),
		nullIfEmpty(record.CcAddresses),
		nullIfEmpty(record.BccAddresses),
		nullIfEmpty(record.DeliveredTo),
		nullIfEmpty(record.DateHeader),
		record.InternalDateUnix,
		nullIfEmpty(record.InternalDate),
		record.SizeEstimate,
		nullIfEmpty(record.BodyText),
		nullIfEmpty(record.BodyHTML),
		record.BodySizeEstimate,
		record.AttachmentCount,
		nullIfEmpty(record.AttachmentNamesJSON),
		record.AttachmentTotalSize,
		nullIfEmpty(record.LabelIDsJSON),
		nullIfEmpty(record.ListUnsubscribe),
		nullIfEmpty(record.ListUnsubscribePost),
		nullIfEmpty(record.ListID),
		nullIfEmpty(record.PrecedenceHeader),
		nullIfEmpty(record.AutoSubmittedHeader),
		nullIfEmpty(record.FeedbackID),
		nullIfEmpty(record.InReplyTo),
		nullIfEmpty(record.ReferencesHeader),
		boolToInt(record.IsUnread),
		boolToInt(record.IsStarred),
		boolToInt(record.IsImportant),
		boolToInt(record.IsInInbox),
		boolToInt(record.IsInSpam),
		boolToInt(record.IsInTrash),
		boolToInt(record.HasAttachments),
	)
	if err != nil {
		return syncedEmailRecord{}, err
	}
	if _, err := tx.Exec(`DELETE FROM synced_email_labels WHERE message_id = ?`, record.MessageID); err != nil {
		return syncedEmailRecord{}, err
	}
	for _, labelID := range record.LabelIDs {
		if _, err := tx.Exec(`INSERT INTO synced_email_labels (message_id, label_id) VALUES (?, ?)`, record.MessageID, labelID); err != nil {
			return syncedEmailRecord{}, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM synced_email_attachments WHERE message_id = ?`, record.MessageID); err != nil {
		return syncedEmailRecord{}, err
	}
	for _, attachment := range record.Attachments {
		if _, err := tx.Exec(`INSERT INTO synced_email_attachments (message_id, filename, mime_type, size_estimate, attachment_id) VALUES (?, ?, ?, ?, ?)`, record.MessageID, attachment.Filename, nullIfEmpty(attachment.MimeType), attachment.SizeEstimate, nullIfEmpty(attachment.AttachmentID)); err != nil {
			return syncedEmailRecord{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return syncedEmailRecord{}, err
	}
	return record, nil
}

func (a *App) markSyncedEmailDeleted(messageID string) error {
	_, err := a.db.Exec(`UPDATE synced_emails SET is_deleted = 1, deleted_at = CURRENT_TIMESTAMP, sync_updated_at = CURRENT_TIMESTAMP WHERE message_id = ?`, messageID)
	return err
}

func (a *App) pruneSyncedEmailsOutsideWindow(currentIDs []string) error {
	if len(currentIDs) == 0 {
		_, err := a.db.Exec(`DELETE FROM synced_emails`)
		return err
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(currentIDs)), ",")
	args := make([]any, 0, len(currentIDs))
	for _, id := range currentIDs {
		args = append(args, id)
	}
	_, err := a.db.Exec(`DELETE FROM synced_emails WHERE message_id NOT IN (`+placeholders+`)`, args...)
	return err
}

func (a *App) getEmailSyncMetrics() (emailSyncMetrics, error) {
	row := a.db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(internal_date_unix), 0), COALESCE(MAX(internal_date_unix), 0) FROM synced_emails WHERE is_deleted = 0`)
	var metrics emailSyncMetrics
	if err := row.Scan(&metrics.MessageCount, &metrics.OldestMessageInternalDateUnix, &metrics.NewestMessageInternalDateUnix); err != nil {
		return emailSyncMetrics{}, err
	}
	metrics.OldestMessageAt = unixMillisToRFC3339(metrics.OldestMessageInternalDateUnix)
	metrics.NewestMessageAt = unixMillisToRFC3339(metrics.NewestMessageInternalDateUnix)
	return metrics, nil
}

func (h historyChangeSet) UpsertIDs() []string {
	ids := make([]string, 0, len(h.Upsert))
	for id := range h.Upsert {
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (h historyChangeSet) DeletedIDs() []string {
	ids := make([]string, 0, len(h.Deleted))
	for id := range h.Deleted {
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func buildSyncedEmailRecord(message gmailMessageResource) syncedEmailRecord {
	headers := map[string]string{}
	for _, header := range message.Payload.Headers {
		headers[strings.ToLower(strings.TrimSpace(header.Name))] = decodeMIMEHeader(header.Value)
	}
	textBody, htmlBody, bodySize, attachmentNames, attachmentTotalSize, attachments := extractMessageContent(message.Payload.Parts, gmailMessagePart{
		MimeType: message.Payload.MimeType,
		Filename: message.Payload.Filename,
		Body:     message.Payload.Body,
	})
	internalDateUnix, _ := strconv.ParseInt(strings.TrimSpace(message.InternalDate), 10, 64)
	labels := normalizeStringSlice(message.LabelIDs)
	labelJSON, _ := json.Marshal(labels)
	attachmentsJSON, _ := json.Marshal(attachmentNames)
	fromName, fromEmail := splitAddress(headers["from"])
	return syncedEmailRecord{MessageID: message.ID, ThreadID: message.ThreadID, HistoryID: message.HistoryID, Subject: cleanWhitespace(headers["subject"]), SubjectNormalized: normalizeEmailSubject(headers["subject"]), Snippet: cleanWhitespace(message.Snippet), FromName: fromName, FromEmail: strings.ToLower(fromEmail), FromRaw: cleanWhitespace(headers["from"]), ReplyTo: cleanWhitespace(headers["reply-to"]), ToAddresses: cleanWhitespace(headers["to"]), CcAddresses: cleanWhitespace(headers["cc"]), BccAddresses: cleanWhitespace(headers["bcc"]), DeliveredTo: cleanWhitespace(headers["delivered-to"]), DateHeader: cleanWhitespace(headers["date"]), InternalDateUnix: internalDateUnix, InternalDate: unixMillisToRFC3339(internalDateUnix), SizeEstimate: message.SizeEstimate, BodyText: strings.TrimSpace(textBody), BodyHTML: strings.TrimSpace(htmlBody), BodySizeEstimate: bodySize, AttachmentCount: len(attachmentNames), AttachmentNamesJSON: string(attachmentsJSON), AttachmentTotalSize: attachmentTotalSize, LabelIDsJSON: string(labelJSON), LabelIDs: labels, ListUnsubscribe: cleanWhitespace(headers["list-unsubscribe"]), ListUnsubscribePost: cleanWhitespace(headers["list-unsubscribe-post"]), ListID: cleanWhitespace(headers["list-id"]), PrecedenceHeader: cleanWhitespace(headers["precedence"]), AutoSubmittedHeader: cleanWhitespace(headers["auto-submitted"]), FeedbackID: cleanWhitespace(headers["feedback-id"]), InReplyTo: cleanWhitespace(headers["in-reply-to"]), ReferencesHeader: cleanWhitespace(headers["references"]), IsUnread: hasLabel(labels, "UNREAD"), IsStarred: hasLabel(labels, "STARRED"), IsImportant: hasLabel(labels, "IMPORTANT"), IsInInbox: hasLabel(labels, "INBOX"), IsInSpam: hasLabel(labels, "SPAM"), IsInTrash: hasLabel(labels, "TRASH"), HasAttachments: len(attachmentNames) > 0, Attachments: attachments}
}

func extractMessageContent(parts []gmailMessagePart, root gmailMessagePart) (string, string, int64, []string, int64, []syncedEmailAttachment) {
	allParts := append([]gmailMessagePart{root}, parts...)
	textParts := make([]string, 0)
	htmlParts := make([]string, 0)
	attachmentNames := make([]string, 0)
	attachments := make([]syncedEmailAttachment, 0)
	var bodySize int64
	var attachmentTotalSize int64
	var walk func(items []gmailMessagePart)
	walk = func(items []gmailMessagePart) {
		for _, part := range items {
			if len(part.Parts) > 0 {
				walk(part.Parts)
			}
			if strings.TrimSpace(part.Filename) != "" {
				attachmentNames = append(attachmentNames, cleanWhitespace(part.Filename))
				attachments = append(attachments, syncedEmailAttachment{
					Filename:     cleanWhitespace(part.Filename),
					MimeType:     cleanWhitespace(part.MimeType),
					SizeEstimate: part.Body.Size,
					AttachmentID: cleanWhitespace(part.Body.AttachmentID),
				})
				attachmentTotalSize += part.Body.Size
				continue
			}
			decoded := decodeBase64URL(strings.TrimSpace(part.Body.Data))
			if decoded == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(part.MimeType), "text/plain") {
				textParts = append(textParts, decoded)
				bodySize += part.Body.Size
			} else if strings.HasPrefix(strings.ToLower(part.MimeType), "text/html") {
				htmlParts = append(htmlParts, decoded)
				bodySize += part.Body.Size
			}
		}
	}
	walk(allParts)
	return strings.Join(textParts, "\n\n"), strings.Join(htmlParts, "\n"), bodySize, normalizeStringSlice(attachmentNames), attachmentTotalSize, attachments
}

func splitAddress(value string) (string, string) {
	addresses, err := mail.ParseAddressList(value)
	if err != nil || len(addresses) == 0 {
		return "", ""
	}
	return cleanWhitespace(addresses[0].Name), cleanWhitespace(addresses[0].Address)
}

func normalizeEmailSubject(value string) string {
	subject := cleanWhitespace(decodeMIMEHeader(value))
	for {
		lower := strings.ToLower(subject)
		switched := false
		for _, prefix := range []string{"re:", "fw:", "fwd:"} {
			if strings.HasPrefix(lower, prefix) {
				subject = cleanWhitespace(subject[len(prefix):])
				switched = true
				break
			}
		}
		if !switched {
			break
		}
	}
	return subject
}

func decodeMIMEHeader(value string) string {
	decoder := new(mime.WordDecoder)
	decoded, err := decoder.DecodeHeader(strings.TrimSpace(value))
	if err != nil {
		return cleanWhitespace(value)
	}
	return cleanWhitespace(decoded)
}

func cleanWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func normalizeStringSlice(values []string) []string {
	seen := make(map[string]struct{})
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := cleanWhitespace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	sort.Strings(normalized)
	return normalized
}

func hasLabel(labels []string, target string) bool {
	for _, label := range labels {
		if strings.EqualFold(label, target) {
			return true
		}
	}
	return false
}

func unixMillisToRFC3339(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.UnixMilli(value).UTC().Format(time.RFC3339)
}

var _ sql.NullString
