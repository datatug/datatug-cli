package commands

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/spf13/cobra"
)

// This private recovery envelope is saved before POST. It stores no token and
// preserves the exact request plus the server-attested operation locator.
type sessionDetectiveRecovery struct {
	SchemaVersion  int                                `json:"schemaVersion"`
	Base           string                             `json:"base"`
	Prefix         string                             `json:"prefix"`
	OperationScope api.SessionDetectiveOperationScope `json:"operationScope"`
	Request        api.SessionDetectiveRequest        `json:"request"`
}

func sessionDetectiveCommand() *cobra.Command {
	parent := &cobra.Command{Use: "detective", Short: "Run a native Check from durable private session inputs"}
	for _, action := range []string{"run", "retry", "status", "reconcile"} {
		command := &cobra.Command{Use: action, Args: cobra.NoArgs, SilenceUsage: true}
		command.Flags().String("recovery-file", "", "Private operation recovery file (required)")
		if action == "run" {
			command.Flags().String("request", "", "Shared Detective request JSON file (required)")
		}
		command.RunE = func(cmd *cobra.Command, _ []string) error { return runSessionDetectiveCommand(cmd, action) }
		parent.AddCommand(command)
	}
	return parent
}

func runSessionDetectiveCommand(cmd *cobra.Command, action string) error {
	path, _ := cmd.Flags().GetString("recovery-file")
	if path == "" {
		return errors.New("--recovery-file is required")
	}
	var record sessionDetectiveRecovery
	var cli sessionCLI
	var err error
	if action == "run" {
		requestPath, _ := cmd.Flags().GetString("request")
		if requestPath == "" {
			return errors.New("--request is required")
		}
		data, readErr := os.ReadFile(requestPath)
		if readErr != nil {
			return readErr
		}
		var request api.SessionDetectiveRequest
		if len(data) > 128<<10 || apicontract.DecodeStrict(data, &request) != nil {
			return errors.New("invalid Detective request")
		}
		cli, err = newSessionCLI(cmd)
		if err != nil {
			return err
		}
		if !cli.caps.NativeDetective || cli.caps.OperationScope == nil {
			return errors.New("native session Detective is unavailable")
		}
		if request.OperationID == "" {
			request.OperationID = sessionOp(cmd)
		}
		record = sessionDetectiveRecovery{SchemaVersion: 1, Base: cli.base, Prefix: cli.prefix, OperationScope: *cli.caps.OperationScope, Request: request}
		if err = writeSessionDetectiveRecovery(path, record); err != nil {
			return err
		}
	} else {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("recovery file must be private and regular")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if len(data) > 1<<20 || apicontract.DecodeStrict(data, &record) != nil || record.SchemaVersion != 1 || record.Request.OperationID == "" {
			return errors.New("invalid Detective recovery file")
		}
		cli, err = newSessionTransport(cmd, false)
		if err != nil {
			return err
		}
		if cli.base != record.Base || cli.prefix != record.Prefix {
			return errors.New("recovery endpoint differs from the original operation")
		}
	}
	scope := record.OperationScope
	if scope.Project != cli.query.Get("project") || scope.Storage != cli.query.Get("storage") || scope.DatabaseID == "" || scope.ProjectID == "" || scope.NamespaceID == "" {
		return errors.New("recovery locator differs from the configured source")
	}
	cli.query.Del("branch")
	cli.query.Set("databaseId", scope.DatabaseID)
	cli.query.Set("projectId", scope.ProjectID)
	cli.query.Set("namespaceId", scope.NamespaceID)
	if scope.SpaceID != "" {
		cli.query.Set("spaceID", scope.SpaceID)
	} else {
		cli.query.Del("spaceID")
	}
	method, suffix, body := http.MethodPost, "/detective", any(record.Request)
	switch action {
	case "status":
		method, suffix, body = http.MethodGet, "/detective/operations/"+url.PathEscape(record.Request.OperationID), nil
	case "reconcile":
		suffix = "/detective/reconcile"
		body = struct {
			OperationID string `json:"operationId"`
		}{record.Request.OperationID}
	}
	var response api.SessionDetectiveResponse
	if _, err = cli.request(cmd.Context(), method, suffix, body, &response); err != nil {
		return err
	}
	if response.OperationID != record.Request.OperationID || response.OperationScope != scope {
		return errors.New("server returned a different accepted operation")
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(response)
}

type sessionRecoveryFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}
type sessionRecoveryDirectory interface {
	Sync() error
	Close() error
}

var openSessionRecoveryFile = func(path string) (sessionRecoveryFile, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
}
var openSessionRecoveryDirectory = func(path string) (sessionRecoveryDirectory, error) { return os.Open(path) }

func writeSessionDetectiveRecovery(path string, record sessionDetectiveRecovery) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	// Exclusive creation prevents replacing a prior operation's recovery identity.
	f, err := openSessionRecoveryFile(path)
	if err != nil {
		return err
	}
	if _, err = f.Write(encoded); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	dir, err := openSessionRecoveryDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
