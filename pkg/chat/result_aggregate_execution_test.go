package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func TestUnifiedWorkspaceAggregateDefaultsExecuteReviewedScopeAndReloadTypes(t *testing.T) {
	for _, original := range []bool{false, true} {
		name := "Matching source"
		if original {
			name = "Original output"
		}
		t.Run(name, func(t *testing.T) {
			sessions, parent := derivedFixture(t)
			sessions.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: sessions.source, Snapshot: ForeignKeySnapshot{Source: sessions.source, Columns: map[string][]string{"main.item": {"Id", "Category", "Value"}}}})
			u, err := NewSessionChatUI(t.Context(), sessions, "none")
			if err != nil {
				t.Fatal(err)
			}
			u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 42})
			u.shell.FocusEntry(u.lastGridEntryID)
			uxSearchAction(t, u, "Group loaded")
			u.shell.Update(uitest.Key("2"))
			_, cmd := u.shell.Update(uitest.Key("enter"))
			drainCmd(t, u, cmd)
			local, ok := u.shell.CenterPanel().(*localGroupsPanel)
			if !ok || local.model.Cell(0, 1) != "2" || local.model.Cell(0, 2) != "30" || len(u.snapshot.RecordSets) != 1 {
				t.Fatal("loaded defaults did not capture two rows without creating a server result")
			}
			_, cmd = u.shell.Update(uitest.Key("esc"))
			drainCmd(t, u, cmd)
			uxSearchAction(t, u, "Group loaded")
			u.shell.Update(uitest.Key("2"))
			if original {
				for range 3 {
					u.shell.Update(uitest.Key("tab"))
				}
				u.shell.Update(uitest.Key("space"))
			}
			_, cmd = u.shell.Update(uitest.Key("shift+enter"))
			drainCmd(t, u, cmd)
			view := u.shell.View().Content
			for range 5 {
				u.shell.Update(uitest.Key("pgdown"))
				view += "\n" + u.shell.View().Content
			}
			if !strings.Contains(flattenView(view), "sum_Value") || len(u.snapshot.RecordSets) != 1 {
				t.Fatalf("server aggregate skipped readable preview: %s", flattenView(view))
			}
			_, cmd = u.shell.Update(uitest.Key("shift+enter"))
			drainCmd(t, u, cmd)
			_, record, ok := u.activeGrid()
			wantRows := 2
			if original {
				wantRows = 1
			}
			if !ok || record.ID == parent.ID || len(record.Result.Rows) != wantRows || FormatValue(record.Result.Rows[0].Data["sum_Value"]) != "30" || record.Lineage == nil || record.Lineage.ParentRecordSetID != parent.ID || len(u.snapshot.RecordSets[parent.ID].Result.Rows) != 2 {
				t.Fatalf("exact scope/lineage failed: %s", flattenView(u.shell.View().Content))
			}
			if !original && (FormatValue(record.Result.Rows[1].Data["count"]) != "4" || FormatValue(record.Result.Rows[1].Data["sum_Value"]) != "180") {
				t.Fatalf("matching source did not include all six rows: %#v", record.Result.Rows)
			}
			reloaded, err := sessions.store.Load(t.Context(), sessions.activeID)
			if err != nil || reloaded.RecordSets[record.ID].Result.ColumnTypes["sum_Value"] != "INTEGER" {
				t.Fatalf("derived type did not survive durable reload: %v", err)
			}
			if _, err = u.newResultOperation(false); err != nil {
				t.Fatalf("typed derived result cannot be locally sorted: %v", err)
			}
		})
	}
}
