package endpoints

import "net/http"

// Legacy definition deletion carries neither a reviewed usage revision nor an
// idempotent lifecycle command. Do not fall back to its old store operation:
// retained references can exist outside the visible definition tree. Database
// row mutations and evidence retention are separate capabilities, not project
// definition deletion.
func rejectUnreviewedProjectLifecycle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")
	writeProjectMutationJSON(w, r, http.StatusConflict, map[string]any{
		"error": map[string]string{
			"code":    "LIFECYCLE_REVIEW_REQUIRED",
			"message": "legacy project deletion is unavailable; use a verified project usage review before archiving or deleting an item",
		},
	})
}
