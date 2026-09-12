// Package querywrite holds the guards every saved-query write path of this
// agent shares - the legacy queries/create_query, update_query and
// delete_query routes and queries/capture - so each check has one
// definition, whichever route a write takes.
package querywrite
