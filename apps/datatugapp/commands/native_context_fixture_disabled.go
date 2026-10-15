//go:build !contextnativefixture

package commands

// Production and release builds leave native adapter activation gated.
const nativeContextFixtureEnabled = false
