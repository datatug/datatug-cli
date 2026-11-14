package dbmirror

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/genproto/googleapis/type/latlng"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeFirestore is an in-process Firestore gRPC server: enough of the reading
// API (collection ids, structured queries on document names, batch get and
// count) for the Google SDK, so FirestoreSource is tested without an emulator.
// Every write RPC is counted and refused: a test asserts the count stays 0.
type fakeFirestore struct {
	pb.UnimplementedFirestoreServer
	mu       sync.Mutex
	prefix   string // projects/P/databases/D/documents
	docs     map[string]map[string]*pb.Value
	writes   []string
	failNext map[string]error // RPC name -> error for its next call
	noCount  bool
}

func startFakeFirestore(t *testing.T, project string) *fakeFirestore {
	t.Helper()
	fake := &fakeFirestore{
		prefix:   "projects/" + project + "/databases/(default)/documents",
		docs:     map[string]map[string]*pb.Value{},
		failNext: map[string]error{},
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterFirestoreServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	t.Setenv("FIRESTORE_EMULATOR_HOST", listener.Addr().String())
	return fake
}

func (f *fakeFirestore) put(path string, data map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fields := map[string]*pb.Value{}
	for key, value := range data {
		fields[key] = f.value(value)
	}
	f.docs[path] = fields
}

func (f *fakeFirestore) value(v any) *pb.Value {
	switch value := v.(type) {
	case nil:
		return &pb.Value{ValueType: &pb.Value_NullValue{}}
	case string:
		return &pb.Value{ValueType: &pb.Value_StringValue{StringValue: value}}
	case int:
		return &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: int64(value)}}
	case float64:
		return &pb.Value{ValueType: &pb.Value_DoubleValue{DoubleValue: value}}
	case bool:
		return &pb.Value{ValueType: &pb.Value_BooleanValue{BooleanValue: value}}
	case time.Time:
		return &pb.Value{ValueType: &pb.Value_TimestampValue{TimestampValue: timestamppb.New(value)}}
	case []byte:
		return &pb.Value{ValueType: &pb.Value_BytesValue{BytesValue: value}}
	case refTo:
		return &pb.Value{ValueType: &pb.Value_ReferenceValue{ReferenceValue: f.prefix + "/" + string(value)}}
	case *latlng.LatLng:
		return &pb.Value{ValueType: &pb.Value_GeoPointValue{GeoPointValue: value}}
	case []any:
		values := make([]*pb.Value, len(value))
		for i, item := range value {
			values[i] = f.value(item)
		}
		return &pb.Value{ValueType: &pb.Value_ArrayValue{ArrayValue: &pb.ArrayValue{Values: values}}}
	case map[string]any:
		fields := map[string]*pb.Value{}
		for key, item := range value {
			fields[key] = f.value(item)
		}
		return &pb.Value{ValueType: &pb.Value_MapValue{MapValue: &pb.MapValue{Fields: fields}}}
	}
	panic(fmt.Sprintf("fake firestore: no value for %T", v))
}

// refTo is a document reference value in fake data.
type refTo string

func (f *fakeFirestore) fail(rpc string) error {
	if err := f.failNext[rpc]; err != nil {
		delete(f.failNext, rpc)
		return err
	}
	return nil
}

func (f *fakeFirestore) document(path string) *pb.Document {
	return &pb.Document{Name: f.prefix + "/" + path, Fields: f.docs[path], CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now()}
}

func (f *fakeFirestore) ListCollectionIds(_ context.Context, _ *pb.ListCollectionIdsRequest) (*pb.ListCollectionIdsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ListCollectionIds"); err != nil {
		return nil, err
	}
	var ids []string
	for path := range f.docs {
		root, _, _ := strings.Cut(path, "/")
		if !slices.Contains(ids, root) {
			ids = append(ids, root)
		}
	}
	slices.Sort(ids)
	return &pb.ListCollectionIdsResponse{CollectionIds: ids}, nil
}

// comparePaths orders document paths segment by segment, as Firestore does.
func comparePaths(a, b string) int {
	return slices.Compare(strings.Split(a, "/"), strings.Split(b, "/"))
}

func (f *fakeFirestore) match(parent string, q *pb.StructuredQuery) []string {
	from := q.GetFrom()[0]
	parentPath := strings.TrimPrefix(strings.TrimPrefix(parent, f.prefix), "/")
	var paths []string
	for path := range f.docs {
		parts := strings.Split(path, "/")
		collection := strings.Join(parts[:len(parts)-1], "/")
		switch {
		case parts[len(parts)-2] != from.GetCollectionId():
			continue
		case from.GetAllDescendants():
			if parentPath != "" && !strings.HasPrefix(path, parentPath+"/") {
				continue
			}
		case collection != strings.TrimPrefix(parentPath+"/"+from.GetCollectionId(), "/"):
			continue
		}
		if f.passes(path, q.GetWhere()) {
			paths = append(paths, path)
		}
	}
	slices.SortFunc(paths, comparePaths)
	if cursor := q.GetStartAt(); cursor != nil {
		after := strings.TrimPrefix(cursor.GetValues()[0].GetReferenceValue(), f.prefix+"/")
		paths = slices.DeleteFunc(paths, func(path string) bool { return comparePaths(path, after) <= 0 })
	}
	if limit := q.GetLimit(); limit != nil && int(limit.GetValue()) < len(paths) {
		paths = paths[:limit.GetValue()]
	}
	return paths
}

func (f *fakeFirestore) passes(path string, filter *pb.StructuredQuery_Filter) bool {
	if filter == nil {
		return true
	}
	if composite := filter.GetCompositeFilter(); composite != nil {
		for _, part := range composite.GetFilters() {
			if !f.passes(path, part) {
				return false
			}
		}
		return true
	}
	field := filter.GetFieldFilter()
	bound := strings.TrimPrefix(field.GetValue().GetReferenceValue(), f.prefix+"/")
	switch field.GetOp() {
	case pb.StructuredQuery_FieldFilter_GREATER_THAN_OR_EQUAL:
		return comparePaths(path, bound) >= 0
	case pb.StructuredQuery_FieldFilter_LESS_THAN:
		return comparePaths(path, bound) < 0
	}
	panic("fake firestore: unexpected filter " + field.String())
}

func (f *fakeFirestore) RunQuery(req *pb.RunQueryRequest, stream pb.Firestore_RunQueryServer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("RunQuery"); err != nil {
		return err
	}
	for _, path := range f.match(req.GetParent(), req.GetStructuredQuery()) {
		if err := stream.Send(&pb.RunQueryResponse{Document: f.document(path), ReadTime: timestamppb.Now()}); err != nil {
			return err
		}
	}
	return stream.Send(&pb.RunQueryResponse{ReadTime: timestamppb.Now()})
}

func (f *fakeFirestore) RunAggregationQuery(req *pb.RunAggregationQueryRequest, stream pb.Firestore_RunAggregationQueryServer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("RunAggregationQuery"); err != nil {
		return err
	}
	aggregation := req.GetStructuredAggregationQuery()
	n := int64(len(f.match(req.GetParent(), aggregation.GetStructuredQuery())))
	fields := map[string]*pb.Value{}
	if !f.noCount {
		fields[aggregation.GetAggregations()[0].GetAlias()] = &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: n}}
	}
	return stream.Send(&pb.RunAggregationQueryResponse{Result: &pb.AggregationResult{AggregateFields: fields}, ReadTime: timestamppb.Now()})
}

func (f *fakeFirestore) BatchGetDocuments(req *pb.BatchGetDocumentsRequest, stream pb.Firestore_BatchGetDocumentsServer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("BatchGetDocuments"); err != nil {
		return err
	}
	for _, name := range req.GetDocuments() {
		path := strings.TrimPrefix(name, f.prefix+"/")
		response := &pb.BatchGetDocumentsResponse{ReadTime: timestamppb.Now(), Result: &pb.BatchGetDocumentsResponse_Missing{Missing: name}}
		if _, ok := f.docs[path]; ok {
			response.Result = &pb.BatchGetDocumentsResponse_Found{Found: f.document(path)}
		}
		if err := stream.Send(response); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeFirestore) refuseWrite(rpc string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, rpc)
	return status.Error(codes.PermissionDenied, "the fake firestore refuses every write")
}

func (f *fakeFirestore) UpdateDocument(context.Context, *pb.UpdateDocumentRequest) (*pb.Document, error) {
	return nil, f.refuseWrite("UpdateDocument")
}
func (f *fakeFirestore) DeleteDocument(context.Context, *pb.DeleteDocumentRequest) (*emptypb.Empty, error) {
	return nil, f.refuseWrite("DeleteDocument")
}
func (f *fakeFirestore) CreateDocument(context.Context, *pb.CreateDocumentRequest) (*pb.Document, error) {
	return nil, f.refuseWrite("CreateDocument")
}
func (f *fakeFirestore) Commit(context.Context, *pb.CommitRequest) (*pb.CommitResponse, error) {
	return nil, f.refuseWrite("Commit")
}
func (f *fakeFirestore) BatchWrite(context.Context, *pb.BatchWriteRequest) (*pb.BatchWriteResponse, error) {
	return nil, f.refuseWrite("BatchWrite")
}
func (f *fakeFirestore) BeginTransaction(context.Context, *pb.BeginTransactionRequest) (*pb.BeginTransactionResponse, error) {
	return nil, f.refuseWrite("BeginTransaction")
}
func (f *fakeFirestore) Write(pb.Firestore_WriteServer) error { return f.refuseWrite("Write") }
