package api

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

type SessionContextSubject struct {
	ID      string                 `json:"id"`
	Label   string                 `json:"label"`
	Subject sessioncontext.Subject `json:"subject"`
	Type    apicontract.ValueType  `json:"type"`
	Shape   string                 `json:"shape"`
	Scope   sessioncontext.Scope   `json:"scope"`
}

func localSessionSampleSubjects(projectDir string, environments []SessionContextEnvironment) []SessionContextSubject {
	f, err := os.Open(filepath.Join(projectDir, "connections", "demo-db.json"))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil || len(body) > 256<<10 {
		return nil
	}
	var catalog struct {
		Format      string `json:"format"`
		Connections []struct {
			ID             string `json:"id"`
			Dataset        string `json:"dataset"`
			Storage        string `json:"storage"`
			Readiness      string `json:"readiness"`
			Source         string `json:"source"`
			FixtureSHA256  string `json:"fixtureSha256"`
			BrowserFixture struct {
				URL   string `json:"url"`
				Bytes int64  `json:"bytes"`
			} `json:"browserFixture"`
		} `json:"connections"`
	}
	if json.Unmarshal(body, &catalog) != nil || catalog.Format != datatug.ConnectionCatalogFormat {
		return nil
	}
	count := 0
	for _, c := range catalog.Connections {
		if c.ID == "chinook-sqlite" {
			count++
			if c.Dataset != "chinook" || c.Storage != "sqlite" || c.Readiness != "public-api" || c.Source != "https://demodb.dev/ovdb/v1/databases/chinook" || c.FixtureSHA256 != "7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15" || c.BrowserFixture.URL != "https://chinook.demodb.dev/data/chinook.sqlite" || c.BrowserFixture.Bytes != 1007616 {
				return nil
			}
		}
	}
	if count != 1 {
		return nil
	}
	const root = "https://github.com/demo-db/chinook/blob/95d14a11256f8db512d5ecd7ea5be56e01d51781/model/"
	out := []SessionContextSubject{}
	for _, env := range environments {
		out = append(out, SessionContextSubject{ID: "chinook-invoice-id", Label: "Invoice ID", Type: apicontract.ValueTypeInteger, Shape: "scalar", Scope: sessioncontext.Scope{EnvironmentID: env.EnvironmentID, IdentityDomain: env.IdentityDomain}, Subject: sessioncontext.Subject{ConceptID: "invoice", FieldID: "InvoiceId", FieldMappingRef: root + "chinook.modelspec.json#/records/Invoice/fields/InvoiceId", ModelRef: "modelspec:///chinook.Invoice", ModelRevision: "19be3e0f575e14aa999769da79ba3ecb7fe3cb7e6a7dee151399dcbebfc56a7b", GraphResource: root + "chinook.meaning.yaml", GraphRevision: "08070167da1626c3fd51f6555e846cd8aa1aa068a533db45c53394127bb0fcad", Record: "Invoice", Fields: []string{"InvoiceId"}}})
	}
	return out
}
