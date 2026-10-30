package commands

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/spf13/cobra"
)

func sessionImportCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "import", Short: "Explicitly import legacy values/messages into a new inactive context bag", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		var payload sessioncontext.LegacyImport
		path, _ := cmd.Flags().GetString("legacy-chat-store")
		if path == "" {
			if err = readSessionInput(cmd, &payload); err != nil {
				return err
			}
		} else {
			if cmd.Flags().Changed("input") {
				return errors.New("choose either --input or --legacy-chat-store")
			}
			id, _ := cmd.Flags().GetString("legacy-session")
			scope, scopeErr := cli.scope(cmd)
			if scopeErr != nil {
				return scopeErr
			}
			payload, err = readLegacyTerminalChat(cmd.Context(), path, id, *scope)
			if err != nil {
				return err
			}
		}
		return cli.submit(cmd, sessioncontext.Command{Action: "importLegacy", LegacyImport: &payload})
	}}
	cmd.Flags().String("input", "-", "LegacyImport JSON file, or - for stdin; no outer session ID/scope")
	cmd.Flags().String("legacy-chat-store", "", "Existing private terminal chat SQLite file, opened read-only; imports text messages only")
	cmd.Flags().String("legacy-session", "", "Exact existing terminal chat session ID to import")
	return cmd
}

// This reads the existing terminal schema without OpenSessionStore, whose open
// path creates/migrates data. It never reads queries, result rows or HTTP bodies.
func readLegacyTerminalChat(ctx context.Context, path, id string, scope sessioncontext.Scope) (sessioncontext.LegacyImport, error) {
	var payload sessioncontext.LegacyImport
	if id == "" {
		return payload, errors.New("--legacy-session is required")
	}
	path, err := sessionAbsPath(path)
	if err != nil {
		return payload, errors.New("legacy chat source unavailable")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return payload, errors.New("legacy chat source must be an existing private regular file")
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=query_only(1)"}
	db, err := sessionSqlOpen("sqlite", uri.String())
	if err != nil {
		return payload, errors.New("legacy chat source unavailable")
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return payload, errors.New("legacy chat source unavailable")
	}
	defer func() { _ = tx.Rollback() }()
	var sourceScope string
	if err = tx.QueryRowContext(ctx, `SELECT title,scope FROM sessions WHERE id=?`, id).Scan(&payload.Title, &sourceScope); err != nil {
		return payload, errors.New("legacy session unavailable")
	}
	digest := sha256.Sum256([]byte(sourceScope + "\x00" + id))
	payload.SourceID, payload.Scope = "terminal-chat-"+hex.EncodeToString(digest[:]), scope
	rows, err := tx.QueryContext(ctx, `SELECT id,role,text,created_at FROM messages WHERE session_id=? AND kind IN ('text','markdown') ORDER BY rowid LIMIT 495`, id)
	if err != nil {
		return payload, errors.New("legacy chat messages unavailable")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var message sessioncontext.LegacyChatMessage
		var created string
		if err = rows.Scan(&message.ID, &message.Role, &message.Text, &created); err != nil {
			return payload, errors.New("legacy chat messages unavailable")
		}
		switch message.Role {
		case "You":
			message.Role = "user"
		case "DataTug":
			message.Role = "assistant"
		default:
			return payload, errors.New("legacy chat has an unsupported message role")
		}
		if message.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return payload, errors.New("legacy chat has an invalid historical timestamp")
		}
		if len(message.Text) > 100000 {
			return payload, errors.New("legacy chat message exceeds import limit")
		}
		payload.Messages = append(payload.Messages, message)
		if len(payload.Messages) > 494 {
			return payload, errors.New("legacy chat exceeds atomic import limit of 494 messages; no partial import was submitted")
		}
	}
	if rows.Err() != nil {
		return payload, errors.New("legacy chat messages unavailable")
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > 4<<20 {
		return payload, errors.New("legacy chat exceeds bounded import size")
	}
	return payload, nil
}

var (
	sessionAbsPath = filepath.Abs
	sessionSqlOpen = sql.Open
)
