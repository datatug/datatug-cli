package api

import "time"

// DataTugAgentVersion specifies agent version
const DataTugAgentVersion = "0.0.1"

// AgentInfo holds agent info
type AgentInfo struct {
	Version       string  `json:"version"`
	UptimeMinutes float64 `json:"uptimeMinutes"`
	// Principal is the serving principal's ID (REQ:principal-selection); ""
	// when the session carries no identified principal (Unrestricted with no
	// --as, or a role/group-only principal). The web UI displays this and
	// MUST NOT be able to change it — no endpoint accepts a principal from
	// the request.
	Principal string `json:"principal,omitempty"`
}

var started = time.Now()

// GetAgentInfo returns agent info
func GetAgentInfo() AgentInfo {
	return AgentInfo{
		Version:       DataTugAgentVersion,
		UptimeMinutes: time.Since(started).Minutes(),
		Principal:     SecurePrincipalID(),
	}
}
