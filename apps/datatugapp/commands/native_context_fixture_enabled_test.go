//go:build contextnativefixture

package commands

import "testing"

func TestNativeFixtureBuildTag(t *testing.T) {
	if !nativeContextFixtureEnabled {
		t.Fatal("fixture tag did not enable its proof capability")
	}
}
