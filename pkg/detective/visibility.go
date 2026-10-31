package detective

import (
	"context"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// VisibilityItems reads current definitions and Cases only. Completed runs,
// evidence, timeline membership and execution snapshots are not an inventory
// of objects the user may archive.
func (s *LocalStore) VisibilityItems(ctx context.Context) (items []datatug.ProjectUsageItem, err error) {
	items = []datatug.ProjectUsageItem{}
	err = s.withLock(ctx, func() error {
		checks, e := readItems[Check](s.definitionDir("checks"))
		if e != nil {
			return e
		}
		validators, e := readItems[PublishedValidator](s.definitionDir("validators"))
		if e != nil {
			return e
		}
		queryValidators, e := readItems[QueryValidators](s.definitionDir("query-validators"))
		if e != nil {
			return e
		}
		scenarios, e := readItems[Scenario](s.definitionDir("scenarios"))
		if e != nil {
			return e
		}
		cases, e := readItems[Case](s.privateItemDir("cases"))
		if e != nil {
			return e
		}
		add := func(kind, id, title string) {
			items = append(items, datatug.ProjectUsageItem{Ref: datatug.ProjectObjectRef{Kind: kind, ID: id}, Title: title})
		}
		for _, item := range checks {
			add("check", item.ID, item.Title)
		}
		for _, item := range validators {
			add("validator", item.ID, item.Title)
		}
		for _, item := range queryValidators {
			add("query-validator", item.QueryID, item.QueryID+" validators")
		}
		for _, item := range scenarios {
			add("scenario", item.ID, item.Title)
		}
		for _, item := range cases {
			if item.WorkItemID == "" {
				add("case", item.ID, item.Title)
			}
		}
		return nil
	})
	return items, err
}
