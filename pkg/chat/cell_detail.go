package chat

import (
	"context"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

type relatedRecord struct {
	key    ForeignKey
	result secureread.Result
}

type relatedPreviewMessage struct {
	sequence int
	related  []relatedRecord
	err      error
}

type cellDetail struct {
	sequence     int
	title        string
	column       string
	value        any
	columns      []string
	values       []any
	qualified    string
	dbType       string
	loading      bool
	related      []relatedRecord
	relatedError error
	offset       int
}

func (d *cellDetail) copyValue() string { return FormatValue(d.value) }

// serializePreviewQuery is dtql.Serialize by default; PreviewRelated always
// builds a well-formed, valid dal.StructuredQuery from fixed components, so
// this seam exists solely to let a test drive its otherwise-unreachable
// serialize-error branch.
var serializePreviewQuery = dtql.Serialize

// PreviewRelated resolves only an authoritative outgoing FK and reads up to
// five matching records through the same DTQL/policy executor as chat queries.
func (a ForeignKeyJoinApplication) PreviewRelated(ctx context.Context, record RecordSet, selected string, row map[string]any) ([]relatedRecord, error) {
	if record.Source != a.Source || a.Executor == nil {
		return nil, nil
	}
	snapshot, err := a.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	var previews []relatedRecord
	for _, fk := range snapshot.Keys {
		prefix := strings.ToLower(fk.Schema + "." + fk.FromRelation + ".")
		selectedField := strings.TrimPrefix(strings.ToLower(selected), prefix)
		if !strings.HasPrefix(strings.ToLower(selected), prefix) {
			continue
		}
		found := false
		for _, field := range fk.FromFields {
			if strings.EqualFold(field, selectedField) {
				found = true
				break
			}
		}
		if !found || len(fk.FromFields) != len(fk.ToFields) {
			continue
		}
		conditions := make([]dal.Condition, 0, len(fk.FromFields))
		complete := true
		for i, field := range fk.FromFields {
			value, ok := row[prefix+strings.ToLower(field)]
			if !ok || value == nil {
				complete = false
				break
			}
			conditions = append(conditions, dal.NewComparison(dal.NewFieldRef("", fk.ToFields[i]), dal.Equal, dal.NewConstant(value)))
		}
		if !complete {
			continue
		}
		target := RelationInstance{Schema: fk.ToSchema, Relation: fk.ToRelation}
		if a.CanReadTarget != nil && a.CanReadTarget(ctx, target) != nil {
			continue
		}
		if a.Secure && a.CanReadTarget == nil {
			continue
		}
		collection := dal.NewRootCollectionRef(fk.ToRelation, "")
		if fk.ToSchema != "" && !strings.EqualFold(fk.ToSchema, "main") {
			collection = dal.NewQualifiedRootCollectionRef(fk.ToSchema, fk.ToRelation, "")
		}
		query := dal.From(collection).NewQuery().Where(conditions...).Limit(5).SelectColumns()
		doc, err := serializePreviewQuery(query)
		if err != nil {
			return nil, err
		}
		result, err := a.Executor.RunDTQL(ctx, a.Source, doc, nil)
		if err != nil {
			return nil, err
		}
		previews = append(previews, relatedRecord{key: fk, result: result})
	}
	return previews, nil
}

func nonempty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
