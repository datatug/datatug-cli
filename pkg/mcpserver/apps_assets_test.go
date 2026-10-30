package mcpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAppsEmbeddedAssetProvenance(t *testing.T) {
	raw, err := os.ReadFile("app/build-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		HTMLSHA256 string `json:"htmlSHA256"`
		HTMLBytes  int    `json:"htmlBytes"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(appsHTML))
	if hex.EncodeToString(sum[:]) != manifest.HTMLSHA256 || len(appsHTML) != manifest.HTMLBytes {
		t.Fatal("embedded HTML differs from build provenance")
	}
	if len(appsHTML) > 1500000 {
		t.Fatal("offline resource exceeds build bound")
	}
	for _, required := range []string{"@modelcontextprotocol/ext-apps@2.0.4", "d4ff0c5beac346eb8ead527144630978c5052ff5", "connect-src 'none'", "frame-src 'none'", "font-src 'none'", `id="view"`, `id="actions"`} {
		if !strings.Contains(appsHTML, required) {
			t.Errorf("resource missing %s", required)
		}
	}
	for _, forbidden := range []string{`<script src=`, `<link href=`, `<iframe`, `sourceMappingURL=`, `/Users/`, `renderer is not installed`} {
		if strings.Contains(appsHTML, forbidden) {
			t.Errorf("nonportable resource contains %s", forbidden)
		}
	}
}

// This is the same JSON fixture the browser tests read, decoded using the real
// core contract so integer/decimal tags cannot silently drift into JS numbers.
func TestAppsBrowserFixtureUsesCoreWireContract(t *testing.T) {
	raw, err := os.ReadFile("app/test/typed-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Meta struct {
			Payload appsPayload `json:"datatug"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var result apicontract.Result
	if err := json.Unmarshal(envelope.Meta.Payload.Result, &result); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	first := result.Recordset.Rows[0]
	if first[0].Type != apicontract.ValueTypeInteger || first[0].Str != "9007199254740993" || first[1].Type != apicontract.ValueTypeDecimal || first[1].Str != "9007199254740992.2" || first[3].Bool {
		t.Fatal("typed precision fixture drifted")
	}
}

func TestAppsMixedBrowserFixtureUsesCoreWireContract(t *testing.T) {
	raw, err := os.ReadFile("app/test/mixed-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Meta struct {
			Payload appsPayload `json:"datatug"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var result apicontract.Result
	if err := json.Unmarshal(envelope.Meta.Payload.Result, &result); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if result.Recordset.Columns[0].Type != "integer" || result.Recordset.Rows[1][0].Type != apicontract.ValueTypeString || result.Recordset.Rows[2][0].Type != apicontract.ValueTypeBoolean || result.Recordset.Rows[2][0].Bool {
		t.Fatal("mixed type fixture no longer exercises independent cell types")
	}
}

func TestAppsResourceRequestsOnlyClipboardWritePermission(t *testing.T) {
	protocol := newAppsProtocol()
	defer protocol.close()
	server := newServer(nil, "test", protocol)
	client := connectAppsClient(t, server, true, "2025-11-25")
	resource, err := client.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: appsResourceURI})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(resource.Contents[0].Meta)
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		UI struct {
			Permissions map[string]json.RawMessage `json:"permissions"`
			CSP         map[string][]string        `json:"csp"`
		} `json:"ui"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.UI.Permissions) != 1 || string(meta.UI.Permissions["clipboardWrite"]) != "{}" {
		t.Fatalf("permissions: %s", raw)
	}
	if len(meta.UI.CSP) != 3 {
		t.Fatalf("CSP: %s", raw)
	}
	for _, domains := range meta.UI.CSP {
		if len(domains) != 0 {
			t.Fatal("external domains unexpectedly permitted")
		}
	}
}
