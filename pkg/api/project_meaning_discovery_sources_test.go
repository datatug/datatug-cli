package api

import (
	"encoding/json"
	"github.com/datatug/datatug-core/pkg/datatug"
	"testing"
)

func TestMeaningDiscoveryArchivedSourcesPreserveReferenceCatalog(t *testing.T) {
	var model datatug.QuerySemanticModel
	if err := json.Unmarshal([]byte(`{"version":1,"basis":{"queryHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","resultIndex":0,"columns":[{"name":"name","dataType":"text"}]},"meaning":{"format":"meaning/draft-2","id":"query","name":"Query","description":"Query meaning","models":{"query":"query.modelspec.json"},"concepts":[{"id":"person","kind":"entity","labels":{"en":"Person"},"description":"A person"},{"id":"person-name","kind":"property","labels":{"en":"Name"},"description":"Name of a person","of":"person","bindings":[{"model":"modelspec:///query.QueryResult","field":"column1","role":"value"}]}]},"model":{"modelspec":"1.0-draft-2","module":{"id":"datatug/query","name":"query","version":"0.1.0"},"records":{"QueryResult":{"fields":{"column1":{"type":"string"}}}}},"bindings":[{"columnIndex":0,"field":"column1","concept":"person-name"}]}`), &model); err != nil {
		t.Fatal(err)
	}
	scope := datatug.MeaningScope{Storage: "local", Project: "project", Branch: "main"}
	identity := datatug.MeaningIdentity{DocumentID: "project", ConceptID: "person"}
	fieldIdentity := datatug.MeaningIdentity{DocumentID: "project", ConceptID: "person-name"}
	model.Meaning.Concepts[0].MeaningRef = &datatug.MeaningReference{MeaningIdentity: identity, Scope: scope}
	model.Meaning.Concepts[1].MeaningRef = &datatug.MeaningReference{MeaningIdentity: fieldIdentity, Scope: scope}
	document := datatug.ProjectMeaningsDocument{Version: 1, Entities: []datatug.MeaningEntity{{MeaningIdentity: identity, Origin: "project", Name: "Person", Fields: []datatug.MeaningField{{MeaningIdentity: fieldIdentity, Name: "Name"}}}}}
	full, err := datatug.BuildProjectMeaningsCatalog(document, []datatug.MeaningQuerySource{{ID: "q", SemanticModel: &model}, {ID: "active", SemanticModel: &model}}, []datatug.MeaningBoardSource{{ID: "board", QueryIDs: []string{"q"}}}, scope, false, true)
	if err != nil || len(full.Entities) != 3 {
		t.Fatal("real full catalog fixture", full, err)
	}
	before, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		refs  []datatug.ProjectObjectRef
		count int
	}{
		{"active", nil, 3},
		{"query", []datatug.ProjectObjectRef{{Kind: "query", ID: "q"}}, 2},
		{"board", []datatug.ProjectObjectRef{{Kind: "board", ID: "board"}}, 3},
		{"both", []datatug.ProjectObjectRef{{Kind: "query", ID: "q"}, {Kind: "board", ID: "board"}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archived := map[datatug.ProjectObjectRef]bool{}
			for _, r := range tc.refs {
				archived[r] = true
			}
			active := projectMeaningDiscovery(full, archived)
			if len(active.Entities) != tc.count || active.Revision != full.Revision || active.Writable != full.Writable {
				t.Fatal("display source filtering changed reference metadata", active)
			}
			projectFound := false
			for _, entity := range active.Entities {
				if entity.DocumentID == "project" {
					projectFound = true
				}
				groups := [][]datatug.MeaningResource{entity.Resources}
				for _, field := range entity.Fields {
					groups = append(groups, field.Resources)
				}
				for _, resources := range groups {
					for _, resource := range resources {
						if archived[datatug.ProjectObjectRef{Kind: resource.Kind, ID: resource.ID}] {
							t.Fatal("archived source remained discoverable", resource)
						}
					}
				}
			}
			if !projectFound {
				t.Fatal("an archived referring source hid an independent project meaning")
			}
			after, err := json.Marshal(full)
			if err != nil || string(before) != string(after) {
				t.Fatal("display filtering mutated retained full catalog", err)
			}
		})
	}
}
