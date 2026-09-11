package openvaultdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	az "github.com/dal-go/dalgo/dtql/authorization"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
)

// SourceConfig is CLI-owned connection metadata referenced by a normal project
// catalog's Path. Tokens remain in the daemon environment. PrincipalID binds the
// configured credential to the fixed local session; OVDB authenticates its own
// token and resolves memberships independently. No caller-supplied identity is sent.
type SourceConfig struct {
	BaseURL     string `json:"baseUrl"`
	DatabaseID  string `json:"databaseId"`
	TokenEnv    string `json:"tokenEnv"`
	PrincipalID string `json:"principalId"`
}

func OpenSource(path string) (dal.DB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open OpenVaultDB connection descriptor: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return nil, errors.New("invalid OpenVaultDB connection descriptor size")
	}
	var fields map[string]json.RawMessage
	if err = DecodeJSONObjectStrict(data, &fields); err != nil {
		return nil, errors.New("invalid OpenVaultDB connection descriptor")
	}
	for key := range fields {
		switch key {
		case "baseUrl", "databaseId", "tokenEnv", "principalId":
		default:
			return nil, fmt.Errorf("unknown OpenVaultDB connection field %q", key)
		}
	}
	var config SourceConfig
	if err = json.Unmarshal(data, &config); err != nil {
		return nil, errors.New("invalid OpenVaultDB connection descriptor")
	}
	if config.TokenEnv == "" || strings.ContainsAny(config.TokenEnv, "=\x00") || strings.TrimSpace(config.PrincipalID) == "" {
		return nil, errors.New("OpenVaultDB connection requires tokenEnv and principalId")
	}
	// Bind each credential to its operator-configured destination independently
	// of editable project/catalog files, preventing credential exfiltration.
	if os.Getenv(config.TokenEnv+"_BASE_URL") != config.BaseURL || os.Getenv(config.TokenEnv+"_PRINCIPAL_ID") != config.PrincipalID {
		return nil, errors.New("OpenVaultDB credential destination is not configured")
	}
	target := Target{BaseURL: config.BaseURL, DatabaseID: config.DatabaseID, Token: os.Getenv(config.TokenEnv)}
	if err = target.Validate(); err != nil {
		return nil, err
	}
	return dal.NewDB(&source{target: target, principalID: config.PrincipalID}), nil
}

type source struct {
	dal.ConcurrencyAvailable
	target      Target
	principalID string
}

func (s *source) ID() string                                      { return s.target.DatabaseID }
func (*source) Adapter() dal.Adapter                              { return dal.NewAdapter("openvaultdb", "acl-mvp") }
func (*source) Schema() dal.Schema                                { return nil }
func (*source) Get(context.Context, record.Record) error          { return dal.ErrNotSupported }
func (*source) Exists(context.Context, *record.Key) (bool, error) { return false, dal.ErrNotSupported }
func (*source) GetMulti(context.Context, []record.Record) error   { return dal.ErrNotSupported }
func (*source) RunReadonlyTransaction(context.Context, dal.ROTxWorker, ...dal.TransactionOption) error {
	return dal.ErrNotSupported
}
func (*source) RunReadwriteTransaction(context.Context, dal.RWTxWorker, ...dal.TransactionOption) error {
	return dal.ErrNotSupported
}
func (*source) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return nil, dal.ErrNotSupported
}

// AuthorizationError retains the owner's validated structured denial internally.
// Its Error string is deliberately safe for the existing public API error mapper.
type AuthorizationError struct{ Decision az.Result }

func (*AuthorizationError) Error() string { return "OpenVaultDB access denied" }
func (*AuthorizationError) Unwrap() error { return access.ErrAccessDenied }

func (s *source) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	principal, ok := access.PrincipalFrom(ctx)
	if !ok || principal.Subject != nil || principal.ID != s.principalID {
		return nil, fmt.Errorf("%w: OpenVaultDB connection principal mismatch", access.ErrAccessDenied)
	}
	query, ok := q.(dal.StructuredQuery)
	if !ok {
		return nil, dal.ErrNotSupported
	}
	doc, err := dtql.Serialize(query)
	if err != nil {
		return nil, err
	}
	response, err := (Client{}).Query(ctx, s.target, doc)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
		decision, _, decodeErr := ValidateAuthorizationEnvelope(response.Body)
		if decodeErr == nil {
			return nil, &AuthorizationError{Decision: decision}
		}
		return nil, access.ErrAccessDenied
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("OpenVaultDB query unavailable")
	}
	var payload struct {
		Records json.RawMessage `json:"records"`
	}
	if json.Unmarshal(response.Body, &payload) != nil || len(payload.Records) == 0 || string(payload.Records) == "null" {
		return nil, errors.New("invalid OpenVaultDB query result")
	}
	var rows []struct {
		Key  string         `json:"key"`
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(payload.Records, &rows) != nil {
		return nil, errors.New("invalid OpenVaultDB records")
	}
	collection := query.From().Base().Name()
	reader := &sourceReader{}
	for _, row := range rows {
		prefix := collection + "/"
		if !strings.HasPrefix(row.Key, prefix) || len(row.Key) == len(prefix) {
			return nil, errors.New("invalid OpenVaultDB record identity")
		}
		id, decodeErr := url.PathUnescape(strings.TrimPrefix(row.Key, prefix))
		if decodeErr != nil || record.ValidateStringID(id) != nil {
			return nil, errors.New("invalid OpenVaultDB record identity")
		}
		reader.records = append(reader.records, record.NewRecordWithData(record.NewKeyWithID(collection, id), row.Data))
	}
	return reader, nil
}

type sourceReader struct {
	records  []record.Record
	position int
}

func (r *sourceReader) Next() (record.Record, error) {
	if r.position == len(r.records) {
		return nil, dal.ErrNoMoreRecords
	}
	rec := r.records[r.position]
	r.position++
	return rec, nil
}
func (r *sourceReader) Close() error          { r.position = len(r.records); return nil }
func (*sourceReader) Cursor() (string, error) { return "", dal.ErrNotSupported }
