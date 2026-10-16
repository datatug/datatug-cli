//go:build windows

package detective

import "github.com/datatug/datatug-core/pkg/sessioncontext"

// Preserve legacy Windows file-write behavior. The new native session bridge
// stays disabled until a Windows durable directory publication adapter is proven.
func syncPrivateDirectory(string) error    { return nil }
func nativeBridgePlatformAdmission() error { return sessioncontext.ErrUnavailable }
