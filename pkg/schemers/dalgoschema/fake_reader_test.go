package dalgoschema

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
)

// fakeReader is a dbschema.SchemaReader over fixed answers. It counts the calls
// it gets, and can be made to fail any method.
type fakeReader struct {
	mu          sync.Mutex
	calls       map[string]int
	collections []string
	defs        map[string]*dbschema.CollectionDef
	indexes     map[string][]dbschema.IndexDef
	constraints map[string][]dbschema.ConstraintDef
	referrers   map[string][]dbschema.Referrer
	errs        map[string]error // method name -> error
	refs        []dal.CollectionRef
	flight      *flightRecorder // when set, every call is held in flight for its delay
}

// flightRecorder counts the calls that are in flight at once and keeps the peak.
type flightRecorder struct {
	mu      sync.Mutex
	current int
	peak    int
	delay   time.Duration
}

// hold marks one call in flight, keeps it there for the delay, and returns what
// ends it.
func (r *flightRecorder) hold() (release func()) {
	r.mu.Lock()
	r.current++
	r.peak = max(r.peak, r.current)
	r.mu.Unlock()
	time.Sleep(r.delay)
	return func() {
		r.mu.Lock()
		r.current--
		r.mu.Unlock()
	}
}

func (r *flightRecorder) peakInFlight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

func (f *fakeReader) record(method string, ref *dal.CollectionRef) error {
	if f.flight != nil {
		defer f.flight.hold()()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[method]++
	if ref != nil {
		f.refs = append(f.refs, *ref)
	}
	return f.errs[method]
}

func (f *fakeReader) callCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeReader) ListCollections(_ context.Context, _ *record.Key) ([]dal.CollectionRef, error) {
	if err := f.record("ListCollections", nil); err != nil {
		return nil, err
	}
	refs := make([]dal.CollectionRef, len(f.collections))
	for i, name := range f.collections {
		refs[i] = dal.NewRootCollectionRef(name, "")
	}
	return refs, nil
}

func (f *fakeReader) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	if err := f.record("DescribeCollection", ref); err != nil {
		return nil, err
	}
	def, ok := f.defs[ref.Name()]
	if !ok {
		return nil, fmt.Errorf("collection %q not found", ref.Name())
	}
	return def, nil
}

func (f *fakeReader) ListIndexes(_ context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	if err := f.record("ListIndexes", ref); err != nil {
		return nil, err
	}
	return f.indexes[ref.Name()], nil
}

func (f *fakeReader) ListConstraints(_ context.Context, ref *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	if err := f.record("ListConstraints", ref); err != nil {
		return nil, err
	}
	return f.constraints[ref.Name()], nil
}

func (f *fakeReader) ListReferrers(_ context.Context, ref *dal.CollectionRef) ([]dbschema.Referrer, error) {
	if err := f.record("ListReferrers", ref); err != nil {
		return nil, err
	}
	return f.referrers[ref.Name()], nil
}

var _ dbschema.SchemaReader = (*fakeReader)(nil)

func intPtr(n int) *int { return &n }

func fields(names ...string) []dal.FieldName {
	out := make([]dal.FieldName, len(names))
	for i, name := range names {
		out[i] = dal.FieldName(name)
	}
	return out
}

// shopReader describes a small PostgreSQL shop database in schema "public",
// under mixed-case names that only an exact reader can look up.
func shopReader() *fakeReader {
	return &fakeReader{
		collections: []string{"Customer", "Order", "OrderLine"},
		defs: map[string]*dbschema.CollectionDef{
			"Customer": {
				Name: "Customer",
				Fields: []dbschema.FieldDef{
					{Name: "CustomerId", Type: dbschema.Int},
					{Name: "Email", Type: dbschema.String, Length: intPtr(255)},
					{Name: "Name", Type: dbschema.String, Nullable: true},
					{Name: "Balance", Type: dbschema.Decimal, Precision: &dbschema.Precision{Total: 10, Scale: 2}, Nullable: true},
					{Name: "Notes", Type: dbschema.String, Nullable: true},
					{Name: "Raw", Type: dbschema.Bytes, Length: intPtr(16), Nullable: true},
				},
				PrimaryKey: fields("CustomerId"),
			},
			"Order": {
				Name: "Order",
				Fields: []dbschema.FieldDef{
					{Name: "OrderId", Type: dbschema.Int},
					{Name: "CustomerId", Type: dbschema.Int},
					{Name: "BillingCustomerId", Type: dbschema.Int, Nullable: true},
					{Name: "Total", Type: dbschema.Float, Nullable: true},
					{Name: "PlacedAt", Type: dbschema.Time},
					{Name: "Paid", Type: dbschema.Bool},
				},
				PrimaryKey: fields("OrderId"),
				ForeignKeys: []dbschema.ForeignKeyDef{
					{Name: "fk_order_customer", Fields: fields("CustomerId"), ReferencedCollection: "Customer", ReferencedFields: fields("CustomerId"), OnUpdate: "NO ACTION", OnDelete: "CASCADE"},
					{Name: "fk_order_billing", Fields: fields("BillingCustomerId"), ReferencedCollection: "Customer", ReferencedFields: fields("CustomerId"), OnUpdate: "NO ACTION", OnDelete: "SET NULL"},
					{Name: "fk_order_audit", Fields: fields("OrderId"), ReferencedNamespace: "audit", ReferencedCollection: "Trail", ReferencedFields: fields("OrderId")},
				},
			},
			"OrderLine": {
				Name: "OrderLine",
				Fields: []dbschema.FieldDef{
					{Name: "LineNo", Type: dbschema.Int},
					{Name: "OrderId", Type: dbschema.Int},
					{Name: "Sku", Type: dbschema.String},
				},
				PrimaryKey: fields("OrderId", "LineNo"),
				ForeignKeys: []dbschema.ForeignKeyDef{
					{Name: "fk_line_order", Fields: fields("OrderId"), ReferencedCollection: "Order", ReferencedFields: fields("OrderId"), OnUpdate: "NO ACTION", OnDelete: "NO ACTION"},
				},
			},
		},
		indexes: map[string][]dbschema.IndexDef{
			"Customer": {
				{Name: "idx_customer_name", Collection: "Customer", Fields: fields("Name")},
				{Name: "uq_customer_email", Collection: "Customer", Fields: fields("Email"), Unique: true},
				{Name: "uq_customer_name_email", Collection: "Customer", Fields: fields("Name", "Email"), Unique: true},
			},
			"Order": {
				{Name: "idx_order_customer", Collection: "Order", Fields: fields("CustomerId", "PlacedAt")},
			},
		},
		constraints: map[string][]dbschema.ConstraintDef{
			"Customer": {
				{Name: "Customer_pkey", Type: "primary-key"},
				{Name: "uq_customer_email", Type: "unique"},
				{Name: "uq_customer_name_email", Type: "unique"},
				{Name: "uq_customer_phantom", Type: "unique"}, // no index of that name
			},
		},
		referrers: map[string][]dbschema.Referrer{
			"Customer": {
				{Collection: dal.NewRootCollectionRef("Order", ""), Fields: fields("BillingCustomerId")},
				{Collection: dal.NewRootCollectionRef("Order", ""), Fields: fields("CustomerId")},
			},
			"Order": {
				{Collection: dal.NewRootCollectionRef("OrderLine", ""), Fields: fields("OrderId")},
			},
		},
	}
}
