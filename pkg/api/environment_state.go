package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/gofrs/flock"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// EnvironmentMetadataPolicy is independent of query execution/save grants.
type EnvironmentMetadataPolicy struct {
	PrincipalID          string `yaml:"principalId"`
	CanReadOrder         bool   `yaml:"canReadOrder"`
	CanWriteOrder        bool   `yaml:"canWriteOrder"`
	CanCreateEnvironment bool   `yaml:"canCreateEnvironment"`
}
type EnvironmentError struct {
	Code    string
	Status  int
	Message string
}

func (e *EnvironmentError) Error() string { return e.Message }
func environmentError(code string, status int) *EnvironmentError {
	return &EnvironmentError{code, status, code}
}
func envDigest(b []byte) string { x := sha256.Sum256(b); return hex.EncodeToString(x[:]) }
func envScopeBinding(s dto.EnvironmentScope, actor string) string {
	parts := []any{"environment-scope/1", s.StoreID, s.ProjectID, s.SpaceID, s.ProjectAPI, s.Branch, s.AuthorityID, s.ResolvedBranch, s.RepositoryID, s.Owner, s.Repository, s.Folder, actor}
	var out strings.Builder
	for _, part := range parts {
		b, _ := json.Marshal(part)
		fmt.Fprintf(&out, "%d:", len(b))
		out.Write(b)
	}
	return envDigest([]byte(out.String()))
}
func envPolicy(projectID string) (actor string, c dto.EnvironmentCapabilities, private, authority string, err error) {
	secureMu.RLock()
	defer secureMu.RUnlock()
	if securityContextID == "" || secureSession.Principal == nil || secureSession.Principal.ID == nil {
		return "", c, "", "", environmentError("UNAUTHENTICATED", 401)
	}
	actor = fmt.Sprint(secureSession.Principal.ID)
	private = capabilities.EnvironmentPrivateDir
	authority = capabilities.EnvironmentAuthorityID
	p, ok := capabilities.EnvironmentMetadataPolicies[projectID]
	if ok && p.PrincipalID == actor {
		c.CanReadOrder = p.CanReadOrder
		c.CanWriteOrder = p.CanWriteOrder && capabilities.AllowWrites
		c.CanCreateEnvironment = p.CanCreateEnvironment && capabilities.AllowWrites
	} else if secureSession.Unrestricted {
		c.CanReadOrder = true
		c.CanWriteOrder = capabilities.AllowWrites
		c.CanCreateEnvironment = capabilities.AllowWrites
	}
	c.Reasons = map[string]string{}
	if !c.CanReadOrder {
		c.Reasons["canReadOrder"] = "Environment metadata policy denies reading"
	}
	if !c.CanWriteOrder {
		c.Reasons["canWriteOrder"] = "Environment metadata write grant and --allow-writes are required"
	}
	if !c.CanCreateEnvironment {
		c.Reasons["canCreateEnvironment"] = "Environment create grant and --allow-writes are required"
	}
	return
}
func environmentGitState(ctx context.Context, root string) (gitState, error) {
	state, err := localGitStateSeam(ctx, root)
	if err == nil {
		return state, nil
	}
	gitDir, e := localGitOutputSeam(ctx, root, "rev-parse", "--absolute-git-dir")
	if e != nil {
		for at := root; ; at = filepath.Dir(at) {
			if _, e := os.Lstat(filepath.Join(at, ".git")); e == nil {
				return gitState{}, environmentError("TEMPORARILY_UNAVAILABLE", 503)
			}
			if at == filepath.Dir(at) {
				break
			}
		}
		return gitState{}, err
	}
	branch, e := localGitOutputSeam(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if e != nil {
		return gitState{}, environmentError("ENVIRONMENT_REVISION_CONFLICT", 409)
	}
	top, e := localGitOutputSeam(ctx, root, "rev-parse", "--show-toplevel")
	if e != nil {
		return gitState{}, e
	}
	return gitState{root: top, gitDir: gitDir, branch: branch, head: "unborn"}, nil
}
func withLocalEnvironment(ctx context.Context, requested dto.EnvironmentScope, initialize bool, fn func(*filestore.EnvironmentFiles, dto.EnvironmentScope, string, dto.EnvironmentCapabilities, string) error) error {
	generation := SecurityContextID()
	actor, c, private, authority, err := envPolicy(requested.ProjectID)
	if err != nil {
		return err
	}
	if !c.CanReadOrder {
		return environmentError("ACCESS_DENIED", 403)
	}
	if requested.StoreID != LocalStoreID || requested.SpaceID != nil || requested.RepositoryID != nil || requested.Owner != nil || requested.Repository != nil || requested.Folder != nil {
		return environmentError("INVALID_REQUEST", 400)
	}
	root, err := servedProjectDir(requested.ProjectID)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	resolved := requested
	state, gitErr := environmentGitState(ctx, root)
	var environmentFailure *EnvironmentError
	if errors.As(gitErr, &environmentFailure) {
		return gitErr
	}
	fence := "non-git"
	var gitLock *flock.Flock
	if gitErr == nil {
		if requested.Branch != nil && *requested.Branch != state.branch {
			return environmentError("ENVIRONMENT_REVISION_CONFLICT", 409)
		}
		if initialize {
			gitLock = flock.New(filepath.Join(state.gitDir, "datatug-project-api.lock"))
			ok, e := gitLock.TryLockContext(ctx, 25*time.Millisecond)
			if e != nil {
				return e
			}
			if !ok {
				return ctx.Err()
			}
			defer gitLock.Unlock()
		}
		resolved.ResolvedBranch = &state.branch
		fence = state.branch + ":" + state.head
	} else {
		if requested.Branch != nil {
			return environmentError("ENVIRONMENT_REVISION_CONFLICT", 409)
		}
		resolved.ResolvedBranch = nil
	}
	if authority == "" {
		host, _ := os.Hostname()
		authority = "local-" + envDigest([]byte(host+":"+root+":"+private))
	}
	resolved.AuthorityID = authority
	if requested.AuthorityID != "" && requested.AuthorityID != authority {
		return environmentError("OPERATION_CONFLICT", 409)
	}
	if requested.ResolvedBranch != nil && (resolved.ResolvedBranch == nil || *requested.ResolvedBranch != *resolved.ResolvedBranch) {
		return environmentError("ENVIRONMENT_REVISION_CONFLICT", 409)
	}
	binding := envScopeBinding(resolved, actor)
	run := func(callback func(*filestore.EnvironmentFiles) error) error {
		if initialize {
			return filestore.WithEnvironmentFiles(ctx, root, private, callback)
		}
		return filestore.ReadEnvironmentFiles(ctx, root, callback)
	}
	return run(func(f *filestore.EnvironmentFiles) error {
		actor2, c2, _, authority2, e := envPolicy(requested.ProjectID)
		if e != nil {
			return e
		}
		if !ValidateSecurityContext(generation) || actor2 != actor || !c2.CanReadOrder || (authority2 != "" && authority2 != authority) {
			return environmentError("ACCESS_DENIED", 403)
		}
		if gitErr == nil {
			after, e := environmentGitState(ctx, root)
			if e != nil || after.branch != state.branch || after.head != state.head {
				return environmentError("ENVIRONMENT_REVISION_CONFLICT", 409)
			}
		}
		if e = fn(f, resolved, binding, c2, fence); e != nil {
			return e
		}
		if !ValidateSecurityContext(generation) {
			return environmentError("ACCESS_DENIED", 403)
		}
		if gitErr == nil {
			after, e := environmentGitState(ctx, root)
			if e != nil || after.branch != state.branch || after.head != state.head {
				return environmentError("ENVIRONMENT_REVISION_CONFLICT", 409)
			}
		}
		return nil
	})
}
func localEnvironmentState(ctx context.Context, f *filestore.EnvironmentFiles, s dto.EnvironmentScope, binding string, c dto.EnvironmentCapabilities, fence string) (*dto.EnvironmentState, error) {
	project, e := f.LoadProjectFile()
	if e != nil {
		return nil, e
	}
	environments, e := f.LoadEnvironments(ctx)
	if e != nil {
		return nil, e
	}
	briefs := make([]dto.EnvironmentBrief, 0, len(environments))
	for _, env := range environments {
		b, e := json.Marshal(env)
		if e != nil {
			return nil, e
		}
		var safe dto.EnvironmentBrief
		if e = json.Unmarshal(b, &safe); e != nil {
			return nil, e
		}
		briefs = append(briefs, safe)
	}
	sort.Slice(briefs, func(i, j int) bool { d, _ := datatug.CompareEnvironmentIDs(briefs[i].ID, briefs[j].ID); return d < 0 })
	order, repair, e := datatug.ResolveEnvironmentOrder(environments.IDs(), project.EnvironmentOrder)
	if e != nil {
		return nil, e
	}
	raw, e := os.ReadFile(filepath.Join(f.Root, "datatug-project.json"))
	if e != nil {
		return nil, e
	}
	membership, e := json.Marshal(environments)
	if e != nil {
		return nil, e
	}
	revision := "environment-revision/1." + envDigest(append(append([]byte(binding+":"+fence+":"), raw...), membership...))
	return &dto.EnvironmentState{Contract: "environment-state/1", Scope: s, ScopeBinding: binding, Environments: briefs, SavedOrder: project.EnvironmentOrder, Order: order, NeedsRepair: repair, Revision: revision, Capabilities: c}, nil
}
func LocalEnvironmentState(ctx context.Context, s dto.EnvironmentScope) (result *dto.EnvironmentState, err error) {
	err = withLocalEnvironment(ctx, s, false, func(f *filestore.EnvironmentFiles, s dto.EnvironmentScope, b string, c dto.EnvironmentCapabilities, fence string) error {
		result, err = localEnvironmentState(ctx, f, s, b, c, fence)
		return err
	})
	return
}

var environmentOperationPattern = regexp.MustCompile(`^[a-f0-9]{64}\.[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

func environmentNewID(name string, ids []string) string {
	var slug strings.Builder
	for _, r := range name {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			slug.WriteRune(r)
		} else if r == ' ' || r == '_' || r == '-' {
			slug.WriteByte('-')
		}
	}
	id := strings.Trim(slug.String(), "-")
	if len(id) > 80 {
		id = id[:80]
	}
	collision := id == ""
	for _, old := range ids {
		if old == id {
			collision = true
		}
	}
	if collision {
		var b [16]byte
		if _, e := rand.Read(b[:]); e != nil {
			return ""
		}
		if id != "" {
			id += "-"
		}
		b[6] = (b[6] & 15) | 64
		b[8] = (b[8] & 63) | 128
		encoded := hex.EncodeToString(b[:])
		id += encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
	}
	return id
}
func LocalCreateEnvironment(ctx context.Context, r dto.CreateEnvironmentRequest) (*dto.EnvironmentOperationResult, error) {
	name := strings.TrimSpace(r.Name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 200 {
		return nil, environmentError("INVALID_REQUEST", 400)
	}
	for _, ch := range name {
		if unicode.IsControl(ch) {
			return nil, environmentError("INVALID_REQUEST", 400)
		}
	}
	return localEnvironmentMutation(ctx, r.EnvironmentMutation, "create-environment", name, nil)
}
func LocalSaveEnvironmentOrder(ctx context.Context, r dto.SaveEnvironmentOrderRequest) (*dto.EnvironmentOperationResult, error) {
	return localEnvironmentMutation(ctx, r.EnvironmentMutation, "save-environment-order", "", r.EnvironmentOrder)
}
func localEnvironmentMutation(ctx context.Context, r dto.EnvironmentMutation, kind, name string, order []string) (result *dto.EnvironmentOperationResult, err error) {
	generation := SecurityContextID()
	err = withLocalEnvironment(ctx, r.Scope, true, func(f *filestore.EnvironmentFiles, s dto.EnvironmentScope, b string, c dto.EnvironmentCapabilities, fence string) error {
		if (kind == "create-environment" && !c.CanCreateEnvironment) || (kind == "save-environment-order" && !c.CanWriteOrder) {
			return environmentError("ACCESS_DENIED", 403)
		}
		if !environmentOperationPattern.MatchString(r.OperationID) || !strings.HasPrefix(r.OperationID, b+".") {
			return environmentError("OPERATION_CONFLICT", 409)
		}
		payload, _ := json.Marshal(struct {
			Kind        string
			Scope       dto.EnvironmentScope
			IfRevision  string
			OperationID string
			Name        string
			Order       []string
		}{kind, s, r.IfRevision, r.OperationID, name, order})
		digest := envDigest(payload)
		old, e := f.Operation(r.OperationID, b)
		if e == nil {
			if old.PayloadDigest != digest {
				return environmentError("OPERATION_CONFLICT", 409)
			}
			until, e := time.Parse(time.RFC3339Nano, old.ReplayUntil)
			if e != nil {
				return e
			}
			if !time.Now().Before(until) {
				return environmentError("OPERATION_EXPIRED", 410)
			}
			result = old
			return nil
		}
		if !os.IsNotExist(e) {
			return e
		}
		state, e := localEnvironmentState(ctx, f, s, b, c, fence)
		if e != nil {
			return e
		}
		if state.Revision != r.IfRevision {
			return environmentError("ENVIRONMENT_REVISION_CONFLICT", 409)
		}
		changes := map[string][]byte{}
		expected := map[string]string{}
		now := time.Now().UTC()
		result = &dto.EnvironmentOperationResult{OperationID: r.OperationID, Kind: kind, PayloadDigest: digest, AcceptedAt: now.Format(time.RFC3339Nano), ReplayUntil: now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano), Status: "committed"}
		if kind == "save-environment-order" {
			ids := make([]string, len(state.Environments))
			for i, v := range state.Environments {
				ids[i] = v.ID
			}
			if datatug.ValidateEnvironmentPermutation(ids, order) != nil {
				return environmentError("INVALID_REQUEST", 400)
			}
			raw, e := os.ReadFile(filepath.Join(f.Root, "datatug-project.json"))
			if e != nil {
				return e
			}
			expected["datatug-project.json"] = envDigest(raw)
			var fields map[string]json.RawMessage
			if e = json.Unmarshal(raw, &fields); e != nil {
				return e
			}
			fields["environmentOrder"], _ = json.Marshal(order)
			changes["datatug-project.json"], e = json.MarshalIndent(fields, "", "\t")
			if e != nil {
				return e
			}
			result.EnvironmentOrder = append([]string{}, order...)
		} else {
			ids := make([]string, len(state.Environments))
			for i, v := range state.Environments {
				ids[i] = v.ID
				title := v.Title
				if title == "" {
					title = v.ID
				}
				if strings.TrimSpace(title) == name {
					return environmentError("DUPLICATE_NAME", 400)
				}
			}
			id := environmentNewID(name, ids)
			if id == "" {
				return errors.New("random environment ID unavailable")
			}
			body, _ := json.Marshal(map[string]any{"id": id, "title": name, "dbServers": []any{}})
			changes["environments/"+id+"/"+id+".env.json"] = body
			expected["environments/"+id+"/"+id+".env.json"] = "absent"
			result.EnvironmentID = id
		}
		prospective := *state
		prospective.Environments = append([]dto.EnvironmentBrief{}, state.Environments...)
		if kind == "create-environment" {
			prospective.Environments = append(prospective.Environments, dto.EnvironmentBrief{ID: result.EnvironmentID, Title: name})
			ids := make([]string, len(prospective.Environments))
			for i, v := range prospective.Environments {
				ids[i] = v.ID
			}
			prospective.Order, _, e = datatug.ResolveEnvironmentOrder(ids, state.SavedOrder)
			if e != nil {
				return e
			}
		} else {
			prospective.SavedOrder = append([]string{}, order...)
			prospective.Order = append([]string{}, order...)
		}
		result.AfterStateDigest, e = dto.EnvironmentStateDigest(prospective)
		if e != nil {
			return e
		}
		reread, e := localEnvironmentState(ctx, f, s, b, c, fence)
		if e != nil {
			return e
		}
		if reread.Revision != state.Revision {
			return environmentError("ENVIRONMENT_REVISION_CONFLICT", 409)
		}
		if actorNow, policyNow, _, _, e := envPolicy(s.ProjectID); e != nil || !ValidateSecurityContext(generation) || envScopeBinding(s, actorNow) != b || (!policyNow.CanCreateEnvironment && kind == "create-environment") || (!policyNow.CanWriteOrder && kind == "save-environment-order") {
			return environmentError("ACCESS_DENIED", 403)
		}
		if fence != "non-git" {
			current, e := environmentGitState(ctx, f.Root)
			if e != nil || current.branch+":"+current.head != fence {
				return environmentError("ENVIRONMENT_REVISION_CONFLICT", 409)
			}
		}
		if e = f.CommitChecked(changes, expected, b, *result); e != nil {
			return environmentError("OUTCOME_UNCERTAIN", 409)
		}
		confirmed, e := localEnvironmentState(ctx, f, s, b, c, fence)
		if e != nil {
			return environmentError("OUTCOME_UNCERTAIN", 409)
		}
		after, e := dto.EnvironmentStateDigest(*confirmed)
		if e != nil || after != result.AfterStateDigest {
			return environmentError("OUTCOME_UNCERTAIN", 409)
		}
		return nil
	})
	return
}
func LocalEnvironmentOperationStatus(ctx context.Context, r dto.EnvironmentOperationStatusRequest) (result *dto.EnvironmentOperationResult, err error) {
	err = withLocalEnvironment(ctx, r.Scope, false, func(f *filestore.EnvironmentFiles, s dto.EnvironmentScope, b string, c dto.EnvironmentCapabilities, fence string) error {
		if !environmentOperationPattern.MatchString(r.OperationID) || !strings.HasPrefix(r.OperationID, b+".") {
			return environmentError("OPERATION_CONFLICT", 409)
		}
		var e error
		result, e = f.Operation(r.OperationID, b)
		if os.IsNotExist(e) {
			return environmentError("TEMPORARILY_UNAVAILABLE", 503)
		}
		return e
	})
	return
}
