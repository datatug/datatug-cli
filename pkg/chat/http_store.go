package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// AppendHTTPResponse owns the original downloaded bytes and, when tabular,
// links a derived immutable RecordSet to that exact response in one transaction.
func (s *SessionStore) AppendHTTPResponse(ctx context.Context, sessionID, originID string, response HTTPResponse, query *QueryResult) (HTTPResponse, error) {
	if len(response.Body) > maxHTTPResponseBytes {
		return HTTPResponse{}, fmt.Errorf("HTTP response exceeds the session storage limit")
	}
	parsed, err := url.ParseRequestURI(response.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return HTTPResponse{}, fmt.Errorf("HTTP response has an invalid source URL")
	}
	response.RequestHasQuery = response.RequestHasQuery || parsed.RawQuery != ""
	response.URL = sanitizedHTTPURL(parsed)
	if response.FinalURL != "" {
		final, err := url.ParseRequestURI(response.FinalURL)
		if err != nil || (final.Scheme != "http" && final.Scheme != "https") || final.Host == "" || final.User != nil {
			return HTTPResponse{}, fmt.Errorf("HTTP response has an invalid final URL")
		}
		response.FinalURL = sanitizedHTTPURL(final)
	}
	for i := range response.Redirects {
		hop, err := url.ParseRequestURI(response.Redirects[i].URL)
		if err != nil || (hop.Scheme != "http" && hop.Scheme != "https") || hop.Host == "" || hop.User != nil {
			return HTTPResponse{}, fmt.Errorf("HTTP response has an invalid redirect URL")
		}
		response.Redirects[i].URL = sanitizedHTTPURL(hop)
	}
	response.Headers = safeResponseHeaders(http.Header(response.Headers))
	response.RequestHeaders = safeResponseHeaders(http.Header(response.RequestHeaders))
	if response.Method == "" {
		response.Method = http.MethodGet
	}
	if !supportedHTTPMethod(response.Method) {
		return HTTPResponse{}, fmt.Errorf("unsupported HTTP request method")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HTTPResponse{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkOrigin(ctx, tx, sessionID, originID); err != nil {
		return HTTPResponse{}, err
	}
	response.ID = uuid.NewString()
	response.SessionID = sessionID
	response.OriginMessageID = originID
	response.CreatedAt = time.Now().UTC()
	queryFlag := 0
	if response.RequestHasQuery {
		queryFlag = 1
	}
	headersJSON, err := json.Marshal(response.Headers)
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("encode HTTP response headers: %w", err)
	}
	requestHeadersJSON, err := json.Marshal(response.RequestHeaders)
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("encode HTTP request headers: %w", err)
	}
	redirectsJSON, err := json.Marshal(response.Redirects)
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("encode HTTP redirects: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO http_responses (id, session_id, origin_message_id, method, url, status_code, content_type, headers_json, request_headers_json, response_nanos, download_nanos, final_url, redirects_json, request_has_query, body, refresh_parent_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, response.ID, sessionID, originID, response.Method, response.URL, response.StatusCode, response.ContentType, string(headersJSON), string(requestHeadersJSON), int64(response.TimeToResponse), int64(response.DownloadTime), response.FinalURL, string(redirectsJSON), queryFlag, response.Body, response.RefreshParentID, stamp(response.CreatedAt)); err != nil {
		return HTTPResponse{}, err
	}
	if query != nil {
		query.HTTPResponseID = response.ID
		parentResponseID := response.RefreshParentID
		for hops := 0; parentResponseID != "" && hops < 100; hops++ {
			var parentRecordID string
			if err := tx.QueryRowContext(ctx, `SELECT id FROM recordsets WHERE session_id = ? AND http_response_id = ?`, sessionID, parentResponseID).Scan(&parentRecordID); err == nil {
				query.RefreshParentID = parentRecordID
				break
			}
			var next string
			if err := tx.QueryRowContext(ctx, `SELECT refresh_parent_id FROM http_responses WHERE session_id = ? AND id = ?`, sessionID, parentResponseID).Scan(&next); err != nil || next == parentResponseID {
				break
			}
			parentResponseID = next
		}
		if err := s.appendQueryTx(ctx, tx, sessionID, originID, response.URL, query, response.CreatedAt); err != nil {
			return HTTPResponse{}, err
		}
	} else {
		kind := "http"
		if response.isMarkdown() {
			kind = "markdown"
		}
		message := ChatMessage{ID: uuid.NewString(), Role: "DataTug", Kind: kind, Text: response.summary(), HTTPResponseID: response.ID, CreatedAt: response.CreatedAt}
		if err := insertMessage(ctx, tx, sessionID, message); err != nil {
			return HTTPResponse{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, stamp(response.CreatedAt), sessionID); err != nil {
		return HTTPResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return HTTPResponse{}, err
	}
	return response, nil
}

func (s *SessionStore) loadHTTPResponses(ctx context.Context, item *ChatSession) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, origin_message_id, method, url, status_code, content_type, headers_json, request_headers_json, response_nanos, download_nanos, final_url, redirects_json, request_has_query, body, refresh_parent_id, created_at FROM http_responses WHERE session_id = ? ORDER BY rowid`, item.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var response HTTPResponse
		var hasQuery int
		var headersJSON string
		var requestHeadersJSON string
		var redirectsJSON string
		var responseNanos, downloadNanos int64
		var created string
		if err := rows.Scan(&response.ID, &response.OriginMessageID, &response.Method, &response.URL, &response.StatusCode, &response.ContentType, &headersJSON, &requestHeadersJSON, &responseNanos, &downloadNanos, &response.FinalURL, &redirectsJSON, &hasQuery, &response.Body, &response.RefreshParentID, &created); err != nil {
			return err
		}
		response.TimeToResponse, response.DownloadTime = time.Duration(responseNanos), time.Duration(downloadNanos)
		if err := json.Unmarshal([]byte(headersJSON), &response.Headers); err != nil {
			return fmt.Errorf("decode HTTP response headers %q: %w", response.ID, err)
		}
		if err := json.Unmarshal([]byte(requestHeadersJSON), &response.RequestHeaders); err != nil {
			return fmt.Errorf("decode HTTP request headers %q: %w", response.ID, err)
		}
		if err := json.Unmarshal([]byte(redirectsJSON), &response.Redirects); err != nil {
			return fmt.Errorf("decode HTTP redirects %q: %w", response.ID, err)
		}
		response.SessionID = item.ID
		response.RequestHasQuery = hasQuery != 0
		if response.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return fmt.Errorf("corrupt HTTP response timestamp %q: %w", response.ID, err)
		}
		item.HTTPResponses[response.ID] = response
	}
	return rows.Err()
}
