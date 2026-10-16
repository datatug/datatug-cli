//go:build contextnativefixture

package commands

// This proof-only build reuses normal serve authentication and authorization.
// Release builds must never enable this tag; it is not product activation.
const nativeContextFixtureEnabled = true
