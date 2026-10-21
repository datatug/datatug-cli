package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"unicode"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/strongo/validation"
)

// These serialization and filesystem boundaries use their production implementations;
// tests inject failures to verify that conditional writes preserve existing files.
var (
	itemTagsProjectDir    = servedProjectDir
	itemTagsJSONUnmarshal = json.Unmarshal
	itemTagsMarshalIndent = json.MarshalIndent
	itemTagsLstat         = (*os.Root).Lstat
	itemTagsStat          = (*os.Root).Stat
	itemTagsOpenFile      = (*os.Root).OpenFile
	itemTagsRename        = (*os.Root).Rename
)

var ErrItemTagsRevisionConflict = errors.New("item tags document changed; reload before saving")

// ItemTagsSelector names one existing project document. Server IDs are the
// full ServerRef.GetID(), including driver; driver also selects its directory.
type ItemTagsSelector struct {
	dto.ProjectRef
	Branch string `json:"branch"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Driver string `json:"driver,omitempty"`
}

type SaveItemTagsRequest struct {
	ItemTagsSelector
	Tags               []string `json:"tags"`
	IfMatch            string   `json:"ifMatch"`
	ExpectedBranchHead string   `json:"expectedBranchHead"`
}

type ItemTags struct {
	Kind       string   `json:"kind"`
	ID         string   `json:"id"`
	Tags       []string `json:"tags"`
	Revision   string   `json:"revision"`
	BranchHead string   `json:"branchHead"`
}

func (s ItemTagsSelector) validate() error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Branch == "" || len(s.Branch) > 1024 {
		return validation.NewErrBadRequestFieldValue("branch", "required")
	}
	switch s.Kind {
	case "board":
		if s.Driver != "" {
			return validation.NewErrBadRequestFieldValue("driver", "not used for boards")
		}
		return ValidateIdentifier("id", s.ID)
	case "dbserver":
		if err := ValidateIdentifier("driver", s.Driver); err != nil {
			return err
		}
		if s.ID == "" || len(s.ID) > 512 || strings.ContainsAny(s.ID, "/\\%") || strings.IndexFunc(s.ID, unicode.IsControl) >= 0 || !strings.HasPrefix(s.ID, s.Driver+":") {
			return validation.NewErrBadRequestFieldValue("id", "must be a server ID")
		}
		return nil
	default:
		return validation.NewErrBadRequestFieldValue("kind", "must be board or dbserver")
	}
}

func validateItemTags(tags []string) error {
	if tags == nil || len(tags) > 100 {
		return validation.NewErrBadRequestFieldValue("tags", "must be an array of at most 100 tags")
	}
	if err := (datatug.ListOfTags{Tags: tags}).Validate(); err != nil {
		return validation.NewBadRequestError(err)
	}
	for _, tag := range tags {
		if strings.TrimSpace(tag) != tag || strings.IndexFunc(tag, unicode.IsControl) >= 0 {
			return validation.NewErrBadRequestFieldValue("tags", "tags must be trimmed and contain no control characters")
		}
	}
	return nil
}

func itemTagsRevision(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// loadItemTagsDocument refuses ambiguous layouts and symlinks. It preserves
// the original JSON fields, including fields newer than this Core model.
func loadItemTagsDocument(reader *qualifiedProjectReader, s ItemTagsSelector) (string, []byte, map[string]json.RawMessage, error) {
	var names []string
	if s.Kind == "board" {
		names = []string{path.Join("boards", boardFileName(s.ID)), path.Join("boards", s.ID, "board.json")}
	} else {
		names = []string{path.Join("dbs", s.Driver, s.ID+".dbserver.json"), path.Join("dbs", s.Driver, s.ID, ".datatug-dbserver.json")}
	}
	var selected string
	var body []byte
	for _, name := range names {
		data, err := readQualifiedOptionalFileLimit(reader, name, 256<<10)
		if err != nil {
			return "", nil, nil, err
		}
		if data == nil {
			continue
		}
		if selected != "" {
			return "", nil, nil, dto.ErrUnsupportedCapability
		}
		selected, body = name, data
	}
	if selected == "" {
		return "", nil, nil, fs.ErrNotExist
	}
	parts := strings.Split(selected, "/")
	for i := range parts {
		info, err := itemTagsLstat(reader.root, strings.Join(parts[:i+1], "/"))
		if err != nil {
			return "", nil, nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, nil, dto.ErrUnsupportedCapability
		}
	}
	if err := validateQualifiedStoredID(body, s.ID, s.Kind); err != nil {
		return "", nil, nil, err
	}
	var fields map[string]json.RawMessage
	if err := itemTagsJSONUnmarshal(body, &fields); err != nil || fields == nil {
		return "", nil, nil, dto.ErrUnsupportedCapability
	}
	if s.Kind == "dbserver" {
		var server datatug.ServerRef
		if itemTagsJSONUnmarshal(fields["server"], &server) != nil || server.Driver != s.Driver || server.GetID() != s.ID {
			return "", nil, nil, dto.ErrUnsupportedCapability
		}
		if err := validateDbServer(server); err != nil {
			return "", nil, nil, err
		}
	}
	return selected, body, fields, nil
}

func itemTagsResult(s ItemTagsSelector, fields map[string]json.RawMessage, body []byte, head string) (*ItemTags, error) {
	tags := make([]string, 0)
	if raw, ok := fields["tags"]; ok {
		if err := itemTagsJSONUnmarshal(raw, &tags); err != nil {
			return nil, err
		}
	}
	if tags == nil {
		tags = []string{}
	}
	return &ItemTags{Kind: s.Kind, ID: s.ID, Tags: tags, Revision: itemTagsRevision(body), BranchHead: head}, nil
}

func LocalProjectItemTags(ctx context.Context, s ItemTagsSelector) (*ItemTags, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	value, err := withCurrentBranchRead(ctx, s.ProjectRef, s.Branch, func(reader *qualifiedProjectReader) (any, error) {
		_, body, fields, err := loadItemTagsDocument(reader, s)
		if err != nil {
			return nil, err
		}
		dir, err := itemTagsProjectDir(s.ProjectID)
		if err != nil {
			return nil, err
		}
		state, err := localGitStateSeam(ctx, dir)
		if err != nil {
			return nil, err
		}
		return itemTagsResult(s, fields, body, state.head)
	})
	if err != nil {
		return nil, err
	}
	return value.(*ItemTags), nil
}

func authorizeItemTagsWrite(ctx context.Context, s ItemTagsSelector) error {
	if err := requireLocalReadPrincipal(); err != nil {
		return err
	}
	secureMu.RLock()
	caps, session := capabilities, secureSession
	secureMu.RUnlock()
	if !caps.AllowWrites {
		return secureread.ErrAccessDenied
	}
	collection, id := "boards", s.ID
	if s.Kind == "dbserver" {
		collection, id = "dbservers", s.Driver+"/"+s.ID
	}
	project := record.NewKeyWithID(accesspolicies.ProjectsCollection, s.ProjectID)
	resource := access.RecordResourceForKey(record.NewKeyWithParentAndID(project, collection, accesspolicies.CanonicalQueryID(id)))
	return accesspolicies.AuthorizeWrite(ctx, accesspolicies.WriteOptions{Principal: session.Principal, Policies: session.Policies, Unrestricted: session.Unrestricted}, access.Update, resource)
}

// SaveLocalProjectItemTags changes only tags on an existing document. It uses
// the shared project lock, a branch/HEAD precondition and an exact-byte revision.
// A failed/lost response is reconciled by reloading; conditional PUTs never
// silently overwrite a newer document or create an absent item.
func SaveLocalProjectItemTags(ctx context.Context, request SaveItemTagsRequest) (*ItemTags, error) {
	s := request.ItemTagsSelector
	if err := s.validate(); err != nil {
		return nil, err
	}
	if err := validateItemTags(request.Tags); err != nil {
		return nil, err
	}
	if len(request.IfMatch) != 64 || request.ExpectedBranchHead == "" {
		return nil, validation.NewErrBadRequestFieldValue("ifMatch", "revision and expectedBranchHead are required")
	}
	if err := authorizeItemTagsWrite(ctx, s); err != nil {
		return nil, err
	}
	value, err := withCurrentBranchRead(ctx, s.ProjectRef, s.Branch, func(reader *qualifiedProjectReader) (any, error) {
		dir, err := itemTagsProjectDir(s.ProjectID)
		if err != nil {
			return nil, err
		}
		state, err := localGitStateSeam(ctx, dir)
		if err != nil {
			return nil, err
		}
		if state.head != request.ExpectedBranchHead {
			return nil, dto.ErrBranchHeadConflict
		}
		name, body, fields, err := loadItemTagsDocument(reader, s)
		if err != nil {
			return nil, err
		}
		if itemTagsRevision(body) != request.IfMatch {
			return nil, ErrItemTagsRevisionConflict
		}
		fields["tags"], err = jsonMarshal(request.Tags)
		if err != nil {
			return nil, err
		}
		updated, err := itemTagsMarshalIndent(fields, "", "  ")
		if err != nil {
			return nil, err
		}
		updated = append(updated, '\n')
		// Create the replacement inside the confined root with an unpredictable
		// exclusive name. Rename preserves the existing item and its other fields.
		temp := path.Join(path.Dir(name), ".datatug-tags-"+newSecurityContextID()+".tmp")
		file, err := itemTagsOpenFile(reader.root, temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		defer func() { _ = reader.root.Remove(temp) }()
		info, err := itemTagsStat(reader.root, name)
		if err != nil {
			_ = fileClose(file)
			return nil, err
		}
		if err = fileChmod(file, info.Mode().Perm()); err == nil {
			_, err = fileWrite(file, updated)
		}
		if err == nil {
			err = fileSync(file)
		}
		closeErr := fileClose(file)
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
		current, err := localGitStateSeam(ctx, dir)
		if err != nil || current.branch != s.Branch || current.head != state.head {
			return nil, dto.ErrBranchHeadConflict
		}
		// Check bytes last, after Git inspection, to preserve edits detected
		// during any earlier I/O. The project lock fences cooperating API
		// writers. Arbitrary external editors do not take that lock; the
		// filesystem provides no compare-and-swap against those processes.
		checkName, check, _, err := loadItemTagsDocument(reader, s)
		if err != nil {
			return nil, err
		}
		if checkName != name || !bytes.Equal(check, body) {
			return nil, ErrItemTagsRevisionConflict
		}
		if err := itemTagsRename(reader.root, temp, name); err != nil {
			return nil, err
		}
		// Once renamed a branch move or foreign edit is an uncertain outcome,
		// never an acknowledgement that tags were saved to the selected branch.
		after, err := localGitStateSeam(ctx, dir)
		if err != nil || after.branch != s.Branch || after.head != state.head {
			return nil, ErrProjectMutationOutcomeUncertain
		}
		persisted, err := reader.readFileLimit(name, 256<<10)
		if err != nil || !bytes.Equal(persisted, updated) {
			return nil, ErrProjectMutationOutcomeUncertain
		}
		return itemTagsResult(s, fields, updated, state.head)
	})
	if err != nil {
		return nil, err
	}
	return value.(*ItemTags), nil
}

// LocalProjectDbServers returns persisted server metadata on the selected
// current branch without opening live database connections.
func LocalProjectDbServers(ctx context.Context, ref dto.ProjectRef, branch string) ([]datatug.ProjDbServer, error) {
	value, err := withCurrentBranchRead(ctx, ref, branch, func(reader *qualifiedProjectReader) (any, error) {
		drivers, err := reader.dirEntries("dbs")
		if err != nil {
			return nil, err
		}
		result := make([]datatug.ProjDbServer, 0)
		for _, driver := range drivers {
			if !driver.IsDir() {
				continue
			}
			if err := ValidateIdentifier("driver", driver.Name()); err != nil {
				return nil, err
			}
			entries, err := reader.dirEntries(path.Join("dbs", driver.Name()))
			if err != nil {
				return nil, err
			}
			ids := itemIDs(entries, "dbserver")
			sort.Strings(ids)
			for _, id := range ids {
				if len(result) >= maxQualifiedProjectItems {
					return nil, dto.ErrUnsupportedCapability
				}
				selector := ItemTagsSelector{ProjectRef: ref, Branch: branch, Kind: "dbserver", Driver: driver.Name(), ID: id}
				if err := selector.validate(); err != nil {
					return nil, err
				}
				_, body, _, err := loadItemTagsDocument(reader, selector)
				if errors.Is(err, fs.ErrNotExist) && isDirectoryEntry(entries, id) {
					continue
				}
				if err != nil {
					return nil, err
				}
				var server datatug.ProjDbServer
				if err := itemTagsJSONUnmarshal(body, &server); err != nil {
					return nil, err
				}
				server.SetID(id)
				result = append(result, server)
			}
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	return value.([]datatug.ProjDbServer), nil
}
