package chat

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
)

func (r relatedRecord) target() RelationInstance {
	if r.incoming {
		return RelationInstance{Schema: r.key.Schema, Relation: r.key.FromRelation}
	}
	return RelationInstance{Schema: r.key.ToSchema, Relation: r.key.ToRelation}
}

func (r relatedRecord) identity() string {
	return fmt.Sprintf("%s|%s|%s|%t", r.key.ConstraintID, relationKey(r.key.Schema, r.key.FromRelation), relationKey(r.key.ToSchema, r.key.ToRelation), r.incoming)
}

// PreviewRelationships discovers both directions from authoritative metadata.
// Numbering is sorted before executing; denied/incomplete relationships retain
// their slot and explicit unavailable state instead of silently shifting digits.
func (a ForeignKeyJoinApplication) PreviewRelationships(ctx context.Context, record RecordSet, row map[string]any) ([]relatedRecord, error) {
	if !storedSourceNames(record.Source, a.Source) || a.Executor == nil {
		return nil, fmt.Errorf("related records are unavailable for this source")
	}
	query, err := tugql.Deserialize([]byte(record.DTQL))
	if err != nil {
		return nil, fmt.Errorf("this result has no authoritative relation provenance")
	}
	root := relationInstances(query.From())[0]
	snapshot, err := a.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	records := []relatedRecord{}
	for _, fk := range snapshot.Keys {
		if relationKey(fk.Schema, fk.FromRelation) == relationKey(root.Schema, root.Relation) {
			records = append(records, relatedRecord{key: fk})
		}
		if relationKey(fk.ToSchema, fk.ToRelation) == relationKey(root.Schema, root.Relation) {
			records = append(records, relatedRecord{key: fk, incoming: true})
		}
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].identity() < records[j].identity() })
	for i := range records {
		item := &records[i]
		target := item.target()
		if a.Secure && a.CanReadTarget == nil {
			item.err = fmt.Errorf("relationship access is unavailable")
			continue
		}
		if a.CanReadTarget != nil {
			if err := a.CanReadTarget(ctx, target); err != nil {
				item.err = fmt.Errorf("relationship access denied")
				continue
			}
		}
		fromFields, toFields := item.key.FromFields, item.key.ToFields
		prefix := relationKey(item.key.Schema, item.key.FromRelation) + "."
		if item.incoming {
			fromFields, toFields = toFields, fromFields
			prefix = relationKey(item.key.ToSchema, item.key.ToRelation) + "."
		}
		conditions := []dal.Condition{}
		if len(fromFields) == 0 || len(fromFields) != len(toFields) {
			item.err = fmt.Errorf("relationship key metadata is incomplete")
			continue
		}
		for j, field := range fromFields {
			value, ok := row[prefix+strings.ToLower(field)]
			if !ok || value == nil {
				item.err = fmt.Errorf("relationship key is missing or null")
				break
			}
			conditions = append(conditions, dal.NewComparison(dal.NewFieldRef("", toFields[j]), dal.Equal, dal.NewConstant(value)))
		}
		if item.err != nil {
			continue
		}
		collection := dal.NewRootCollectionRef(target.Relation, "")
		if target.Schema != "" && !strings.EqualFold(target.Schema, "main") {
			collection = dal.NewQualifiedRootCollectionRef(target.Schema, target.Relation, "")
		}
		doc, err := dtqlSerialize(dal.From(collection).NewQuery().Where(conditions...).Limit(5).SelectColumns())
		if err != nil {
			item.err = fmt.Errorf("relationship query is unsupported")
			continue
		}
		item.document = string(doc)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item.result, err = a.Executor.RunDTQL(ctx, a.Source, doc, nil)
		if err != nil {
			item.err = fmt.Errorf("related query failed: %s", publicQueryError(err, nil))
		}
	}
	return records, nil
}

func (c *SessionChat) previewRelationshipsActive(ctx context.Context, sessionID, recordID string, row map[string]any) ([]relatedRecord, error) {
	c.mu.Lock()
	if c.activeID != sessionID {
		c.mu.Unlock()
		return nil, ErrActiveSessionChanged
	}
	snapshot, err := c.store.Load(ctx, sessionID)
	application := c.joinApplication
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	record, ok := snapshot.RecordSets[recordID]
	if !ok {
		return nil, fmt.Errorf("selected result is no longer available")
	}
	var previews []relatedRecord
	switch a := application.(type) {
	case ForeignKeyJoinApplication:
		previews, err = a.PreviewRelationships(ctx, record, row)
	case *ForeignKeyJoinApplication:
		if a == nil {
			return nil, fmt.Errorf("related records are unsupported for this source")
		}
		previews, err = a.PreviewRelationships(ctx, record, row)
	default:
		return nil, fmt.Errorf("related records are unsupported for this source")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeID != sessionID {
		return nil, ErrActiveSessionChanged
	}
	return previews, err
}
