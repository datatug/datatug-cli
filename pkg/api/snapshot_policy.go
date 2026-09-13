package api

// SnapshotProjectPolicy is trusted server configuration for one project.
// A source absent from Sources is denied snapshot retention by default.
type SnapshotProjectPolicy struct {
	Sources map[string]SnapshotSourcePolicy `json:"sources" yaml:"sources"`
}

// SnapshotSourcePolicy explicitly allows snapshot retention for one source
// and names columns that must be removed before bytes reach private storage.
type SnapshotSourcePolicy struct {
	Allow         bool     `json:"allow" yaml:"allow"`
	MaskedColumns []string `json:"maskedColumns,omitempty" yaml:"maskedColumns,omitempty"`
}

// SnapshotPolicy returns an isolated copy of the configured source policy.
// Missing project/source entries and allow:false all fail closed.
func SnapshotPolicy(projectID, sourceID string) (SnapshotSourcePolicy, bool) {
	secureMu.RLock()
	defer secureMu.RUnlock()
	project, ok := capabilities.SnapshotPolicies[projectID]
	if !ok {
		return SnapshotSourcePolicy{}, false
	}
	policy, ok := project.Sources[sourceID]
	if !ok || !policy.Allow {
		return SnapshotSourcePolicy{}, false
	}
	policy.MaskedColumns = append([]string(nil), policy.MaskedColumns...)
	return policy, true
}
