package reconcileapi

import (
	"testing"

	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/stretchr/testify/require"
)

func TestRequestValidate(t *testing.T) {
	selector := &reconcile.InstanceSelector{Family: "f"}
	for _, c := range []struct {
		req  Request
		want string
	}{
		{Request{Project: "p", ProfileID: "x"}, "securityContextId"},
		{Request{SecurityContextID: "s", ProfileID: "x"}, "project"},
		{Request{SecurityContextID: "s", Project: "p"}, "queryId or profileId"},
		{Request{SecurityContextID: "s", Project: "p", QueryID: "q"}, "instances"},
		{Request{SecurityContextID: "s", Project: "p", QueryID: "../etc/passwd", Instances: selector}, "project query reference"},
		{Request{SecurityContextID: "s", Project: "p", QueryID: "a//b", Instances: selector}, "project query reference"},
	} {
		require.ErrorContains(t, c.req.Validate(), c.want)
	}
	require.NoError(t, Request{SecurityContextID: "s", Project: "p", ProfileID: "x"}.Validate())
	require.NoError(t, Request{SecurityContextID: "s", Project: "p", QueryID: "q", Instances: selector}.Validate())
}

func TestOverlay(t *testing.T) {
	base := reconcile.Rules{Key: []string{"code"}, Mode: reconcile.ModeBaseline, Baseline: "a", Fields: map[string]reconcile.FieldPolicy{"name": reconcile.FieldIdentical}}
	same := Overlay(base, nil)
	require.Equal(t, base, same)
	same.Fields["name"] = reconcile.FieldIgnored
	require.Equal(t, reconcile.FieldIdentical, base.Fields["name"], "the result never aliases the base")

	out := Overlay(base, &reconcile.Rules{
		Key: []string{"iso"}, Mode: reconcile.ModeConsensus, Presence: reconcile.PresenceAny, DefaultField: reconcile.FieldNormalized,
		Fields: map[string]reconcile.FieldPolicy{"price": reconcile.FieldInstanceSpecific}, Disclosure: reconcile.DiscloseGroupsOnly, Limit: 7,
	})
	require.Equal(t, reconcile.Rules{
		Key: []string{"iso"}, Mode: reconcile.ModeConsensus, Presence: reconcile.PresenceAny, DefaultField: reconcile.FieldNormalized,
		Fields:     map[string]reconcile.FieldPolicy{"name": reconcile.FieldIdentical, "price": reconcile.FieldInstanceSpecific},
		Disclosure: reconcile.DiscloseGroupsOnly, Limit: 7,
	}, out)

	out = Overlay(reconcile.Rules{Key: []string{"code"}}, &reconcile.Rules{Baseline: "b", Fields: map[string]reconcile.FieldPolicy{"x": reconcile.FieldIgnored}})
	require.Equal(t, reconcile.ModeBaseline, out.Mode)
	require.Equal(t, "b", out.Baseline)
	require.Equal(t, reconcile.FieldIgnored, out.Fields["x"])

	out = Overlay(reconcile.Rules{Key: []string{"code"}}, &reconcile.Rules{Mode: reconcile.ModeBaseline, Baseline: "c"})
	require.Equal(t, reconcile.ModeBaseline, out.Mode)
}

func TestOverlaySelector(t *testing.T) {
	base := reconcile.InstanceSelector{Family: "f", Kind: "production"}
	require.Equal(t, base, OverlaySelector(base, nil))
	require.Equal(t, reconcile.InstanceSelector{Family: "g", Kind: "test", Tenants: []string{"t"}, Environments: []string{"e1", "e2"}},
		OverlaySelector(base, &reconcile.InstanceSelector{Family: "g", Kind: "test", Tenants: []string{"t"}, Environments: []string{"e1", "e2"}}))
}

func TestOverlayNeverLoosensDisclosure(t *testing.T) {
	groupsOnly := reconcile.Rules{Key: []string{"code"}, Disclosure: reconcile.DiscloseGroupsOnly}
	require.Equal(t, reconcile.DiscloseGroupsOnly, Overlay(groupsOnly, &reconcile.Rules{Disclosure: reconcile.DiscloseValues}).Disclosure)
	values := reconcile.Rules{Key: []string{"code"}, Disclosure: reconcile.DiscloseValues}
	require.Equal(t, reconcile.DiscloseGroupsOnly, Overlay(values, &reconcile.Rules{Disclosure: reconcile.DiscloseGroupsOnly}).Disclosure)
	require.Equal(t, reconcile.DiscloseValues, Overlay(reconcile.Rules{Key: []string{"code"}}, &reconcile.Rules{Disclosure: reconcile.DiscloseValues}).Disclosure)
}
