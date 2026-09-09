package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/openvaultdb"
	"github.com/julienschmidt/httprouter"
)

const agentTokenHeader = "X-Datatug-Agent-Token"

type OpenVaultDBProxyOptions struct {
	Origin       string
	SessionToken string
	Targets      map[string]openvaultdb.Target
	Client       openvaultdb.Client
}

func NewAgentSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create agent session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (o OpenVaultDBProxyOptions) validate() error {
	if o.Origin == "" || o.SessionToken == "" {
		return errors.New("OpenVaultDB proxy requires origin and agent session token")
	}
	origin, err := url.Parse(o.Origin)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" ||
		origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return errors.New("invalid OpenVaultDB proxy origin")
	}
	for id, target := range o.Targets {
		if !validTargetID(id) {
			return fmt.Errorf("invalid OpenVaultDB target ID %q", id)
		}
		if err := target.Validate(); err != nil {
			return fmt.Errorf("invalid OpenVaultDB target %q: %w", id, err)
		}
	}
	return nil
}

func validTargetID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func registerOpenVaultDBProxy(router *httprouter.Router, o OpenVaultDBProxyOptions) error {
	if len(o.Targets) == 0 {
		return nil
	}
	if err := o.validate(); err != nil {
		return err
	}
	targets := make(map[string]openvaultdb.Target, len(o.Targets))
	for id, target := range o.Targets {
		targets[id] = target
	}
	o.Targets = targets
	register := func(path string, handler http.HandlerFunc) {
		router.HandlerFunc(http.MethodPost, path, o.protect(handler))
		router.HandlerFunc(http.MethodOptions, path, o.preflight)
	}
	register("/datatug/ovdb/query", o.query)
	register("/datatug/ovdb/explain", o.explain)
	register("/datatug/ovdb/update", o.update)
	register("/datatug/ovdb/evidence", o.evidence)
	register("/datatug/ovdb/targets", o.targets)
	return nil
}

func (o OpenVaultDBProxyOptions) preflight(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != o.Origin ||
		r.Header.Get("Access-Control-Request-Method") != http.MethodPost ||
		!allowedPreflightHeaders(r.Header.Get("Access-Control-Request-Headers")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", o.Origin)
	w.Header().Set("Access-Control-Allow-Methods", http.MethodPost)
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, "+agentTokenHeader)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func allowedPreflightHeaders(value string) bool {
	for _, h := range strings.Split(value, ",") {
		h = strings.TrimSpace(h)
		if h != "" && !strings.EqualFold(h, "Content-Type") && !strings.EqualFold(h, agentTokenHeader) {
			return false
		}
	}
	return true
}

func (o OpenVaultDBProxyOptions) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Vary", "Origin")
		if r.Header.Get("Origin") != o.Origin ||
			subtle.ConstantTimeCompare([]byte(r.Header.Get(agentTokenHeader)), []byte(o.SessionToken)) != 1 {
			writeProxyError(w, http.StatusForbidden, "agent_session_required", "agent session required")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", o.Origin)
		if r.Header.Get("Content-Type") != "application/json" {
			writeProxyError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
			return
		}
		next(w, r)
	}
}

type proxyRequest struct {
	Target  string          `json:"target"`
	DTQL    string          `json:"dtql,omitempty"`
	Request json.RawMessage `json:"request,omitempty"`
}

func decodeProxyRequest(w http.ResponseWriter, r *http.Request) (proxyRequest, bool) {
	reader := http.MaxBytesReader(w, r.Body, openvaultdb.MaxRequestBytes)
	data, err := io.ReadAll(reader)
	if err != nil {
		writeProxyError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return proxyRequest{}, false
	}
	var fields map[string]json.RawMessage
	if openvaultdb.DecodeJSONObjectStrict(data, &fields) != nil {
		writeProxyError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return proxyRequest{}, false
	}
	for key := range fields {
		if key != "target" && key != "dtql" && key != "request" {
			writeProxyError(w, http.StatusBadRequest, "invalid_request", "invalid request")
			return proxyRequest{}, false
		}
	}
	var in proxyRequest
	if json.Unmarshal(fields["target"], &in.Target) != nil || in.Target == "" {
		writeProxyError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return proxyRequest{}, false
	}
	if raw := fields["dtql"]; len(raw) != 0 && json.Unmarshal(raw, &in.DTQL) != nil {
		writeProxyError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return proxyRequest{}, false
	}
	in.Request = fields["request"]
	return in, true
}

func (o OpenVaultDBProxyOptions) targets(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil {
		writeProxyError(w, http.StatusBadRequest, "invalid_request", "targets request must have an empty body")
		return
	}
	if len(strings.TrimSpace(string(data))) != 0 {
		var fields map[string]json.RawMessage
		if openvaultdb.DecodeJSONObjectStrict(data, &fields) != nil || len(fields) != 0 {
			writeProxyError(w, http.StatusBadRequest, "invalid_request", "targets request must have an empty body")
			return
		}
	}
	type item struct {
		ID         string `json:"id"`
		DatabaseID string `json:"databaseId"`
	}
	items := make([]item, 0, len(o.Targets))
	for id, target := range o.Targets {
		items = append(items, item{ID: id, DatabaseID: target.DatabaseID})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"targets": items})
}

func (o OpenVaultDBProxyOptions) target(w http.ResponseWriter, id string) (openvaultdb.Target, bool) {
	target, ok := o.Targets[id]
	if !ok {
		writeProxyError(w, http.StatusNotFound, "target_unavailable", "OpenVaultDB target unavailable")
	}
	return target, ok
}

func (o OpenVaultDBProxyOptions) query(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeProxyRequest(w, r)
	if !ok {
		return
	}
	target, ok := o.target(w, in.Target)
	if !ok {
		return
	}
	if in.DTQL == "" || len(in.Request) != 0 {
		writeProxyError(w, http.StatusBadRequest, "invalid_request", "query requires dtql")
		return
	}
	response, err := o.Client.Query(r.Context(), target, []byte(in.DTQL))
	o.forward(w, response, err)
}

func (o OpenVaultDBProxyOptions) explain(w http.ResponseWriter, r *http.Request) {
	in, target, ok := o.documentRequest(w, r)
	if !ok || !validateDocumentDatabase(in.Request, target.DatabaseID, true, false) {
		if ok {
			writeProxyError(w, http.StatusBadRequest, "invalid_request", "invalid authorization request")
		}
		return
	}
	response, err := o.Client.Explain(r.Context(), target, in.Request)
	o.forward(w, response, err)
}

func (o OpenVaultDBProxyOptions) update(w http.ResponseWriter, r *http.Request) {
	in, target, ok := o.documentRequest(w, r)
	if !ok {
		return
	}
	table, row, valid := validateUpdate(in.Request, target.DatabaseID)
	if !valid {
		writeProxyError(w, http.StatusBadRequest, "invalid_request", "invalid update operation")
		return
	}
	response, err := o.Client.Update(r.Context(), target, table, row, in.Request)
	o.forward(w, response, err)
}

func (o OpenVaultDBProxyOptions) evidence(w http.ResponseWriter, r *http.Request) {
	in, target, ok := o.documentRequest(w, r)
	if !ok || !validateDocumentDatabase(in.Request, target.DatabaseID, false, true) {
		if ok {
			writeProxyError(w, http.StatusBadRequest, "invalid_request", "invalid evidence request")
		}
		return
	}
	response, err := o.Client.Evidence(r.Context(), target, in.Request)
	o.forward(w, response, err)
}

func (o OpenVaultDBProxyOptions) documentRequest(w http.ResponseWriter, r *http.Request) (proxyRequest, openvaultdb.Target, bool) {
	in, ok := decodeProxyRequest(w, r)
	if !ok {
		return in, openvaultdb.Target{}, false
	}
	target, ok := o.target(w, in.Target)
	if !ok {
		return in, target, false
	}
	if in.DTQL != "" || len(in.Request) == 0 {
		writeProxyError(w, http.StatusBadRequest, "invalid_request", "request document required")
		return in, target, false
	}
	return in, target, true
}

type resourceDocument struct {
	Subject    json.RawMessage `json:"subject,omitempty"`
	Simulation json.RawMessage `json:"simulation,omitempty"`
	Resource   struct {
		DatabaseID string     `json:"databaseId"`
		Path       string     `json:"path"`
		Table      string     `json:"table,omitempty"`
		RowID      string     `json:"rowId,omitempty"`
		Columns    [][]string `json:"columns,omitempty"`
	} `json:"resource,omitempty"`
	Operations []struct {
		Resource struct {
			DatabaseID string `json:"databaseId"`
		} `json:"resource"`
	} `json:"operations,omitempty"`
}

func validateDocumentDatabase(body []byte, database string, rejectIdentity, evidence bool) bool {
	var doc resourceDocument
	if json.Unmarshal(body, &doc) != nil || rejectIdentity && (len(doc.Subject) != 0 || len(doc.Simulation) != 0) {
		return false
	}
	if evidence {
		parts := strings.Split(strings.TrimPrefix(doc.Resource.Path, "/"), "/")
		return doc.Resource.DatabaseID == database && len(parts) == 2 && parts[1] == doc.Resource.RowID &&
			(doc.Resource.Table == "" || doc.Resource.Table == parts[0]) && len(doc.Resource.Columns) == 0
	}
	if len(doc.Operations) == 0 {
		return false
	}
	for _, op := range doc.Operations {
		if op.Resource.DatabaseID != database {
			return false
		}
	}
	return true
}

func validateUpdate(body []byte, database string) (string, string, bool) {
	var op struct {
		Action   string `json:"action"`
		Resource struct {
			DatabaseID string `json:"databaseId"`
			Path       string `json:"path"`
			Table      string `json:"table"`
			RowID      string `json:"rowId"`
		} `json:"resource"`
		Mutation   json.RawMessage `json:"mutation"`
		Subject    json.RawMessage `json:"subject,omitempty"`
		Simulation json.RawMessage `json:"simulation,omitempty"`
	}
	if json.Unmarshal(body, &op) != nil || op.Action != "update" || op.Resource.DatabaseID != database ||
		op.Resource.Table == "" || op.Resource.RowID == "" || len(op.Mutation) == 0 || len(op.Subject) != 0 || len(op.Simulation) != 0 {
		return "", "", false
	}
	if op.Resource.Path != "/"+op.Resource.Table+"/"+op.Resource.RowID || strings.ContainsAny(op.Resource.Table+op.Resource.RowID, "\\?#") {
		return "", "", false
	}
	if !validResourceSegment(op.Resource.Table) || !validResourceSegment(op.Resource.RowID) {
		return "", "", false
	}
	return op.Resource.Table, op.Resource.RowID, true
}

func validResourceSegment(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 256 &&
		strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\%?#\x00\r\n")
}

func (o OpenVaultDBProxyOptions) forward(w http.ResponseWriter, response openvaultdb.Response, err error) {
	if err != nil {
		writeProxyError(w, http.StatusBadGateway, "openvaultdb_unavailable", "OpenVaultDB request failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(response.Body)
}

func writeProxyError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
