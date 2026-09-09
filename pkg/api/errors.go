package api

import "errors"

var errNotImplementedYet = errors.New("not implemented yet")

// ErrSourceUnavailable marks a source resolution failure the contract maps
// to SOURCE_UNAVAILABLE (503): zero eligible/registered sources for the
// requested (environment, source) pair — see pkg/api/resolver.go's
// ResolveSource/EligibleTargets and pkg/server/endpoints's
// newSourceUnavailable.
var ErrSourceUnavailable = errors.New("resolver: source unavailable")
