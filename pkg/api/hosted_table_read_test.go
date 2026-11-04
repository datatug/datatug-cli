package api

import (
	"context"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"testing"
)

func TestHostedTableReadLegacyAccessor(t *testing.T) {
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	if v, e := HostedTableRead(context.Background(), "project", "local", "catalog"); v != nil || e != nil {
		t.Fatal("unregistered accessor changed legacy")
	}
}
