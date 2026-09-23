package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// HTTPRequestSettings are local DataTug project defaults. They never enter
// chat messages, model context, or persisted HTTP response metadata.
type HTTPRequestSettings struct {
	Headers       map[string]string
	Cookies       map[string]string
	HeaderScopes  map[string]string
	CookieScopes  map[string]string
	HeaderOrigins map[string]string
}

const httpCLISettingsScope = "@cli"

// The shared file sits beside per-project chat databases under the same
// private local directory. It is never written into a project repository.
func openHTTPSettingsDB(path string) (*sql.DB, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("HTTP settings database must be a private regular file")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return nil, fmt.Errorf("create private HTTP settings database: %w", createErr)
		}
		_ = file.Close()
	} else {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		`CREATE TABLE IF NOT EXISTS http_request_settings (project_id TEXT NOT NULL, origin TEXT NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY (project_id, origin, kind, name))`,
	} {
		if _, err = db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize HTTP settings: %w", err)
		}
	}
	return db, nil
}

func httpOrigin(raw string) (string, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("provide an HTTP or HTTPS origin without credentials")
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: strings.ToLower(parsed.Host)}).String(), nil
}

func validHTTPSettingName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", r) {
			return false
		}
	}
	return true
}

func validateHTTPSetting(kind, origin, name, value string) (string, error) {
	if kind != "header" && kind != "cookie" {
		return "", fmt.Errorf("unknown HTTP setting kind")
	}
	if !validHTTPSettingName(name) || value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("invalid HTTP %s name or value", kind)
	}
	if origin != "" {
		if _, err := httpOrigin(origin); err != nil {
			return "", err
		}
	}
	if kind == "cookie" {
		if origin == "" {
			return "", fmt.Errorf("cookies require a target origin")
		}
		if err := (&http.Cookie{Name: name, Value: value}).Valid(); err != nil {
			return "", fmt.Errorf("invalid cookie name or value")
		}
		return name, nil
	}
	canonical := http.CanonicalHeaderKey(name)
	switch canonical {
	case "Host", "Cookie", "Content-Length", "Transfer-Encoding", "Connection", "Proxy-Authorization":
		return "", fmt.Errorf("use a supported HTTP header; cookies have a separate command")
	}
	if origin == "" && canonical != "User-Agent" && canonical != "Accept" && canonical != "Accept-Language" {
		return "", fmt.Errorf("this header needs a target origin")
	}
	return canonical, nil
}

func (s *SessionStore) SetHTTPRequestSetting(ctx context.Context, scope, kind, origin, name, value string) error {
	projectID, err := s.httpSettingsProjectID(scope)
	if err != nil {
		return err
	}
	canonical, err := validateHTTPSetting(kind, origin, name, value)
	if err != nil {
		return err
	}
	_, err = s.settingsDB.ExecContext(ctx, `INSERT INTO http_request_settings (project_id, origin, kind, name, value) VALUES (?, ?, ?, ?, ?) ON CONFLICT(project_id, origin, kind, name) DO UPDATE SET value = excluded.value`, projectID, origin, kind, canonical, value)
	return err
}

func (s *SessionStore) RemoveHTTPRequestSetting(ctx context.Context, scope, kind, origin, name string) error {
	projectID, err := s.httpSettingsProjectID(scope)
	if err != nil {
		return err
	}
	if kind != "header" && kind != "cookie" || !validHTTPSettingName(name) {
		return fmt.Errorf("invalid HTTP setting")
	}
	if kind == "header" {
		name = http.CanonicalHeaderKey(name)
	}
	_, err = s.settingsDB.ExecContext(ctx, `DELETE FROM http_request_settings WHERE project_id = ? AND origin = ? AND kind = ? AND name = ?`, projectID, origin, kind, name)
	return err
}

func (s *SessionStore) HTTPRequestSettings(ctx context.Context, origin string) (HTTPRequestSettings, error) {
	settings := HTTPRequestSettings{Headers: map[string]string{"User-Agent": "DataTug"}, Cookies: map[string]string{}, HeaderScopes: map[string]string{"User-Agent": "default"}, CookieScopes: map[string]string{}, HeaderOrigins: map[string]string{"User-Agent": ""}}
	projectKey, err := s.httpSettingsProjectID("project")
	if err != nil {
		return settings, err
	}
	rows, err := s.settingsDB.QueryContext(ctx, `SELECT project_id, origin, kind, name, value FROM http_request_settings WHERE project_id IN (?, ?) AND (origin = '' OR origin = ?) ORDER BY CASE WHEN project_id = ? THEN 0 ELSE 1 END, origin`, httpCLISettingsScope, projectKey, origin, httpCLISettingsScope)
	if err != nil {
		return settings, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var projectID, storedOrigin, kind, name, value string
		if err := rows.Scan(&projectID, &storedOrigin, &kind, &name, &value); err != nil {
			return settings, err
		}
		scope := "project"
		if projectID == httpCLISettingsScope {
			scope = "cli"
		}
		if kind == "header" {
			settings.Headers[name] = value
			settings.HeaderScopes[name] = scope
			settings.HeaderOrigins[name] = storedOrigin
		} else if kind == "cookie" && storedOrigin == origin {
			settings.Cookies[name] = value
			settings.CookieScopes[name] = scope
		}
	}
	return settings, rows.Err()
}

func (s *SessionStore) httpSettingsProjectID(scope string) (string, error) {
	switch scope {
	case "cli":
		return httpCLISettingsScope, nil
	case "project":
		return "@project:" + filepath.Base(s.path), nil
	default:
		return "", fmt.Errorf("HTTP setting scope must be cli or project")
	}
}
