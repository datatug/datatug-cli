package dtlog

import "github.com/strongo/buildinfo"

// version feeds the "$app_version" posthog property. It is sourced from the
// shared github.com/strongo/buildinfo module — which reads the same
// link-time-stamped values `datatug --version`/`datatug version` report —
// rather than a second, independently-stamped `-X main.version=...` value.
// A second source of truth for the CLI's version is the exact class of bug
// this migration removes: see spec/features/cli/version/README.md.
var version = buildinfo.Get("datatug").Version
