// Copyright (c) Sneat Co. All rights reserved.
package secureread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dataaccess"
)

// HostedTableRead is routing metadata, never permission or eligibility proof.
// Presence (including unavailable) forbids a consumer's native read fallback.
type HostedTableRead struct {
	Version      int                     `json:"version"`
	Status       string                  `json:"status"`
	SpaceID      string                  `json:"spaceID,omitempty"`
	ProjectID    string                  `json:"projectId,omitempty"`
	ConnectionID string                  `json:"connectionId,omitempty"`
	Fingerprint  *dataaccess.Fingerprint `json:"fingerprint,omitempty"`
	ReasonCode   string                  `json:"reasonCode,omitempty"`
}

type hostedTableRegistration struct {
	Version      int    `json:"version"`
	SpaceID      string `json:"spaceID"`
	ProjectID    string `json:"projectId"`
	ConnectionID string `json:"connectionId"`
	// CatalogSHA256 is recorded registration provenance, not a current hosted read.
	CatalogSHA256 string `json:"catalogSHA256"`
	FileSHA256    string `json:"fileSHA256"`
	Database      string `json:"database"`
}

func decodeHostedTableRegistration(raw json.RawMessage) (*hostedTableRegistration, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// Registration keys are literal decoded names, not encoding/json's field aliases.
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil {
		return nil, ErrSourceBinding
	}
	for key := range keys {
		switch key {
		case "version", "spaceID", "projectId", "connectionId", "catalogSHA256", "fileSHA256", "database":
		default:
			return nil, ErrSourceBinding
		}
	}
	var c hostedTableRegistration
	if apicontract.DecodeStrict(raw, &c) != nil || c.Version != 1 || !dbcopy.IsPlainSourceID(c.SpaceID) || !dbcopy.IsPlainSourceID(c.ProjectID) || !dbcopy.IsPlainSourceID(c.ConnectionID) || !utf8.ValidString(c.Database) || c.Database == "" || len(c.Database) > 256 || strings.TrimSpace(c.Database) != c.Database || strings.IndexFunc(c.Database, unicode.IsControl) >= 0 {
		return nil, ErrSourceBinding
	}
	for _, s := range []string{c.CatalogSHA256, c.FileSHA256} {
		b, e := hex.DecodeString(s)
		if e != nil || len(b) != sha256.Size || hex.EncodeToString(b) != s {
			return nil, ErrSourceBinding
		}
	}
	return &c, nil
}

var hostedSourceOpen = os.Open
var hostedSourceRead = func(f *os.File, p []byte) (int, error) { return f.Read(p) }

// HostedTableRead inspects only the already fixed source, bracketed by current
// session checks. Content equality is independently rechecked by the hosted API.
func (s Session) HostedTableRead(ctx context.Context, project, environment, catalog string) (*HostedTableRead, error) {
	if s.binding == nil {
		return nil, nil
	}
	if err := s.CheckSourceBinding(ctx); err != nil {
		return nil, err
	}
	b := s.binding
	if project != b.config.Project || environment != b.config.Environment || catalog != b.config.Catalog {
		return nil, ErrAccessDenied
	}
	c := b.hosted
	if c == nil {
		return nil, nil
	}
	unavailable := &HostedTableRead{Version: 1, Status: "unavailable", ReasonCode: "source-binding-unavailable"}
	digest, err := b.hostedSourceDigest(ctx)
	if err != nil || digest != c.FileSHA256 {
		return unavailable, nil
	}
	if err = s.CheckSourceBinding(ctx); err != nil {
		return nil, err
	}
	return &HostedTableRead{Version: 1, Status: "available", SpaceID: c.SpaceID, ProjectID: c.ProjectID, ConnectionID: c.ConnectionID, Fingerprint: &dataaccess.Fingerprint{Engine: "sqlite", Database: c.Database, EngineIdentity: "sha256:" + digest}}, nil
}

func (b *sourceBinding) hostedSourceDigest(ctx context.Context) (string, error) {
	file, err := hostedSourceOpen(b.target)
	if err != nil {
		return "", ErrSourceBinding
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || !os.SameFile(before, b.file) || before.Size() > 64<<20 {
		return "", ErrSourceBinding
	}
	h := sha256.New()
	buf := make([]byte, 32768)
	var size int64
	for {
		if ctx.Err() != nil {
			return "", ErrSourceBinding
		}
		n, e := hostedSourceRead(file, buf)
		size += int64(n)
		if size > 64<<20 {
			return "", ErrSourceBinding
		}
		_, _ = h.Write(buf[:n])
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", ErrSourceBinding
		}
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", ErrSourceBinding
	}
	current, err := strictCanonicalFile(b.target, false)
	if err != nil || current != b.target {
		return "", ErrSourceBinding
	}
	info, err := bindingStat(current)
	if err != nil || !os.SameFile(after, info) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return "", ErrSourceBinding
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
