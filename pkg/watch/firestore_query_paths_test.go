package watch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

type cloudOpaqueQuery struct{ dal.Query }
type cloudQueryReaderProbe struct {
	dal.ReadwriteTransaction
	reader dal.RecordsReader
	err    error
}

func (p cloudQueryReaderProbe) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return p.reader, p.err
}

type cloudCloseReader struct {
	dal.RecordsReader
	closed bool
	err    error
}

func (r *cloudCloseReader) Close() error { r.closed = true; _ = r.RecordsReader.Close(); return r.err }

func TestFirestoreOverlayQueryRejectsUnscopedAndUnsupportedPlans(t *testing.T) {
	_, tx, _ := cloudGuardSession(t)
	ctx := context.Background()
	builder := dal.From(dal.NewRootCollectionRef(definitions, "")).NewQuery().Where(dal.WhereField("namespace", dal.Equal, testScope.Namespace))
	cases := []struct {
		query dal.Query
		want  error
	}{
		{cloudOpaqueQuery{}, dal.ErrNotSupported},
		{builder.Clone().Offset(1).SelectColumns(), dal.ErrNotSupported},
		{dal.From(dal.NewCollectionRef(definitions, "", record.NewKeyWithID("parent", "p"))).NewQuery().Where(dal.WhereField("namespace", dal.Equal, testScope.Namespace)).SelectColumns(), dal.ErrNotSupported},
		{dal.From(dal.NewRootCollectionRef(definitions, "")).NewQuery().Where(dal.WhereField("unknown", dal.Equal, "x")).SelectColumns(), dal.ErrNotSupported},
		{dal.From(dal.NewRootCollectionRef(definitions, "")).NewQuery().Where(dal.WhereField("namespace", dal.Equal, "other")).SelectColumns(), ErrForbidden},
		{builder.Clone().Where(dal.WhereField("project", dal.Equal, "other")).SelectColumns(), ErrForbidden},
		{dal.From(dal.NewRootCollectionRef("unknown", "")).NewQuery().Where(dal.WhereField("namespace", dal.Equal, testScope.Namespace)).SelectColumns(), ErrInvalid},
		{builder.Clone().OrderBy(dal.Ascending(dal.Field("payload"))).SelectColumns(), dal.ErrNotSupported},
	}
	for _, tc := range cases {
		_, e := tx.ExecuteQueryToRecordsReader(ctx, tc.query)
		cloudRequireError(t, e, tc.want)
	}
}
func TestFirestoreOverlayReaderFailuresAreClosedAndFailClosed(t *testing.T) {
	ctx := context.Background()
	cases := []string{"query", "next", "close", "nonmap", "foreign-scope", "read-budget", "unbounded-query"}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			s, tx, _ := cloudGuardSession(t)
			physical, e := tx.physical(key(definitions, testScope, "row"))
			if e != nil {
				t.Fatal(e)
			}
			rows := []record.Record{}
			want := errWatchInjected
			switch mode {
			case "nonmap":
				rows = append(rows, record.NewRecordWithData(physical, "not a map"))
				want = ErrInvalid
			case "foreign-scope":
				data := cloudGuardData()
				data["namespace"] = "foreign"
				rows = append(rows, record.NewRecordWithData(physical, data))
				want = ErrForbidden
			case "read-budget":
				rows = append(rows, record.NewRecordWithData(physical, cloudGuardData()))
				tx.readCount = cloudReadRows
				want = ErrCapacity
			case "unbounded-query":
				for i := 0; i <= cloudQueryRows; i++ {
					rows = append(rows, record.NewRecordWithData(record.NewKeyWithParentAndID(physical.Parent(), definitions, fmt.Sprint(i)), cloudGuardData()))
				}
				want = ErrCapacity
			}
			base := dal.NewRecordsReader(rows)
			closed := &cloudCloseReader{RecordsReader: base}
			var reader dal.RecordsReader = closed
			if mode == "next" {
				reader = &statePayloadReader{err: errWatchInjected}
			}
			if mode == "close" {
				closed.err = errWatchInjected
			}
			probe := cloudQueryReaderProbe{reader: reader}
			if mode == "query" {
				probe.err = errWatchInjected
			}
			tx.reader = probe
			_, e = tx.ExecuteQueryToRecordsReader(ctx, cloudQuery(definitions, Scope{Namespace: s.config.Namespace}, "", nil, 0))
			cloudRequireError(t, e, want)
			if mode != "query" && mode != "next" && !closed.closed {
				t.Fatal("physical reader leaked", mode)
			}
		})
	}
}
func TestFirestoreOverlayQuerySeesStagedDeletesFiltersAndBounds(t *testing.T) {
	ctx := context.Background()
	s, tx, raw := cloudGuardSession(t)
	k := key(definitions, testScope, "existing")
	physical, _ := tx.physical(k)
	raw.rows[physical.String()] = contractFirestoreRow{physical, cloudGuardData()}
	if e := tx.Delete(ctx, k); e != nil {
		t.Fatal(e)
	}
	rows, e := cloudRecords(ctx, tx, cloudQuery(definitions, testScope, "guard", nil, 0))
	if e != nil || len(rows) != 0 {
		t.Fatal("staged delete invisible", len(rows), e)
	}
	// Staged rows excluded by equality filters cannot leak through cached queries.
	data := cloudGuardData()
	data["watch"] = "different"
	rowKey, _ := tx.physical(key(definitions, testScope, "new"))
	tx.pending[rowKey.String()] = &cloudRow{key: rowKey, data: data, exists: true}
	rows, e = cloudRecords(ctx, tx, cloudQuery(definitions, testScope, "guard", nil, 0))
	if e != nil || len(rows) != 0 {
		t.Fatal("staged filter ignored", len(rows), e)
	}
	tx = newFirestoreSession(s.cloud, &contractFirestoreTX{rows: map[string]contractFirestoreRow{}})
	for i := 0; i <= cloudQueryRows; i++ {
		rowKey, _ := tx.physical(key(definitions, testScope, fmt.Sprint(i)))
		tx.pending[rowKey.String()] = &cloudRow{key: rowKey, data: cloudGuardData(), exists: true}
	}
	_, e = tx.ExecuteQueryToRecordsReader(ctx, cloudQuery(definitions, testScope, "", nil, 0))
	cloudRequireError(t, e, ErrCapacity)
}
func TestFirestoreOverlaySortIsDeterministicForLegacyMetadata(t *testing.T) {
	ctx := context.Background()
	cases := [][2]any{{int64(1), "wrong"}, {"wrong", int64(1)}, {float64(1), float64(2)}, {"a", "b"}}
	for _, values := range cases {
		s, tx, _ := cloudGuardSession(t)
		tx.reader = cloudQueryReaderProbe{reader: dal.NewRecordsReader([]record.Record{})}
		ids := []string{}
		for i, value := range values {
			k, _ := tx.physical(key(definitions, testScope, fmt.Sprint(i)))
			data := cloudGuardData()
			data["ready"] = value
			tx.pending[k.String()] = &cloudRow{key: k, data: data, exists: true}
			ids = append(ids, k.ID.(string))
		}
		rows, e := cloudRecords(ctx, tx, cloudQuery(definitions, Scope{Namespace: s.config.Namespace}, "", nil, 0, dal.Descending(dal.Field("ready"))))
		if e != nil || len(rows) != 2 {
			t.Fatal(len(rows), e)
		}
		if values[0] == "a" {
			if rows[0].Data().(map[string]any)["ready"] != "b" {
				t.Fatal("string order ignored")
			}
		} else {
			sort.Strings(ids)
			if rows[0].Key().ID != ids[0] {
				t.Fatal("legacy metadata order unstable")
			}
		}
	}
}
func TestFirestoreCloudItemsRejectsCorruptPayloadAndReaderErrors(t *testing.T) {
	ctx := context.Background()
	key := record.NewKeyWithID(definitions, "x")
	for _, data := range []any{"notmap", map[string]any{"payload": 1}, map[string]any{"payload": "{"}} {
		reader := &statePayloadReader{records: []record.Record{record.NewRecordWithData(key, data)}}
		_, e := cloudItems[Definition](ctx, statePayloadQuery{reader: reader}, nil)
		if e == nil || !reader.closed {
			t.Fatal("invalid payload accepted or reader leaked", data, e)
		}
	}
	for _, q := range []statePayloadQuery{{err: errWatchInjected}, {reader: &statePayloadReader{err: errWatchInjected}}} {
		_, e := cloudItems[Definition](ctx, q, nil)
		if !errors.Is(e, errWatchInjected) {
			t.Fatal(e)
		}
	}
}

func TestFirestoreScalarComparisonRejectsUnlikeLegacyTypes(t *testing.T) {
	for _, tc := range []struct {
		left, right      any
		less, comparable bool
	}{
		{int64(1), int64(2), true, true}, {int64(2), int64(1), false, true},
		{"a", "b", true, true}, {"b", "a", false, true},
		{int64(1), "wrong", false, false}, {"wrong", int64(1), false, false},
		{float64(1), float64(2), false, false},
	} {
		less, comparable := cloudScalarLess(tc.left, tc.right)
		if less != tc.less || comparable != tc.comparable {
			t.Fatal(tc, less, comparable)
		}
	}
}
