package dbmirror

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	firestorepb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// This file is the only code of the package that holds a Firestore client. It
// calls only reading methods of the SDK (Collections, Documents, Get and
// aggregation queries), and the client it opens cannot send anything else:
// every request passes readOnlyCall, which refuses any RPC but the four that
// read (TestFirestoreClientRefusesWrites). TestNoWriteCallInPackage also fails
// if a writing method is named anywhere in the package.

// readRPCs are the only Firestore RPCs a mirror's client may send.
var readRPCs = map[string]bool{
	"/google.firestore.v1.Firestore/RunQuery":            true,
	"/google.firestore.v1.Firestore/RunAggregationQuery": true,
	"/google.firestore.v1.Firestore/BatchGetDocuments":   true,
	"/google.firestore.v1.Firestore/ListCollectionIds":   true,
}

// readOnlyCall refuses, before it is sent, any RPC that is not in readRPCs.
func readOnlyCall(method string) error {
	if readRPCs[method] {
		return nil
	}
	return status.Errorf(codes.PermissionDenied, "datatug db copy only reads its source: the request %s was not sent", method)
}

func readOnlyUnary(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if err := readOnlyCall(method); err != nil {
		return err
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}

func readOnlyStream(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	if err := readOnlyCall(method); err != nil {
		return nil, err
	}
	return streamer(ctx, desc, cc, method, opts...)
}

var (
	newFirestoreClient = firestore.NewClientWithDatabase
	newGRPCClient      = grpc.NewClient
)

// FirestoreSource reads one Firestore database through the Google SDK, with
// the credentials the SDK finds in the operator's environment (Application
// Default Credentials, or FIRESTORE_EMULATOR_HOST for an emulator).
type FirestoreSource struct {
	client *firestore.Client
	conn   *grpc.ClientConn // the emulator connection this source opened, if any
}

var _ Source = (*FirestoreSource)(nil)

// emulatorCredentials are the ones the SDK itself sends to an emulator.
type emulatorCredentials struct{}

func (emulatorCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer owner"}, nil
}
func (emulatorCredentials) RequireTransportSecurity() bool { return false }

// OpenFirestore opens a reader of database (use "(default)") of project.
func OpenFirestore(ctx context.Context, project, database string) (*FirestoreSource, error) {
	interceptors := []grpc.DialOption{grpc.WithChainUnaryInterceptor(readOnlyUnary), grpc.WithChainStreamInterceptor(readOnlyStream)}
	opts := []option.ClientOption{option.WithGRPCDialOption(interceptors[0]), option.WithGRPCDialOption(interceptors[1])}
	source := &FirestoreSource{}
	if host := os.Getenv("FIRESTORE_EMULATOR_HOST"); host != "" {
		// For an emulator the SDK dials its own connection and ignores dial
		// options, so the source dials it, the way the SDK does, with the filter.
		host = "passthrough:///" + strings.TrimPrefix(strings.TrimPrefix(host, "http://"), "https://")
		conn, err := newGRPCClient(host, append(interceptors,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithPerRPCCredentials(emulatorCredentials{}),
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(math.MaxInt32)))...)
		if err != nil {
			return nil, err
		}
		source.conn = conn
		opts = append(opts, option.WithGRPCConn(conn))
	}
	client, err := newFirestoreClient(ctx, project, database, opts...)
	if err != nil {
		if source.conn != nil {
			_ = source.conn.Close()
		}
		return nil, err
	}
	source.client = client
	return source, nil
}

// Close releases the client, and with it the emulator connection.
func (s *FirestoreSource) Close() error {
	return s.client.Close()
}

// ReadAt makes every later document read return the documents as they were at
// t (Firestore keeps them for an hour, or for 7 days with point-in-time
// recovery). Counts are not affected.
func (s *FirestoreSource) ReadAt(t time.Time) {
	s.client.WithReadOptions(firestore.ReadTime(t))
}

// RootCollections implements Source.
func (s *FirestoreSource) RootCollections(ctx context.Context) ([]string, error) {
	var ids []string
	it := s.client.Collections(ctx)
	for {
		ref, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return ids, nil
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, ref.ID)
	}
}

// query builds q without its window.
func (s *FirestoreSource) query(q Query) firestore.Query {
	var query firestore.Query
	if q.Group {
		query = s.client.CollectionGroup(q.Collection).Query
		if q.Under != "" {
			// Document names compare segment by segment, so every document
			// at or below Under sorts in [Under, Under+"\x00"): the next
			// sibling with a longer id (Under+"x") sorts after the bound.
			query = query.Where(firestore.DocumentID, ">=", s.client.Doc(q.Under)).
				Where(firestore.DocumentID, "<", s.client.Doc(q.Under+"\x00"))
		}
	} else {
		query = s.client.Collection(q.Collection).Query
	}
	return query.OrderBy(firestore.DocumentID, firestore.Asc)
}

// Page implements Source.
func (s *FirestoreSource) Page(ctx context.Context, q Query) ([]Doc, error) {
	query := s.query(q).Limit(q.Limit)
	if q.After != "" {
		query = query.StartAfter(s.client.Doc(q.After))
	}
	snapshots, err := query.Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	docs := make([]Doc, len(snapshots))
	for i, snapshot := range snapshots {
		docs[i] = Doc{Path: relativePath(snapshot.Ref.Path), Data: snapshot.Data()}
	}
	return docs, nil
}

// Get implements Source.
func (s *FirestoreSource) Get(ctx context.Context, path string) (*Doc, error) {
	ref := s.client.Doc(path)
	if ref == nil {
		return nil, fmt.Errorf("%q is not a document path", path)
	}
	snapshot, err := ref.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Doc{Path: path, Data: snapshot.Data()}, nil
}

// Count implements Source.
func (s *FirestoreSource) Count(ctx context.Context, q Query) (int64, error) {
	query := s.query(q)
	if q.Limit > 0 {
		query = query.Limit(q.Limit) // a count of a limited query counts (and bills) at most Limit documents
	}
	result, err := query.NewAggregationQuery().WithCount("n").Get(ctx)
	if err != nil {
		return 0, err
	}
	value, ok := result["n"].(*firestorepb.Value)
	if !ok {
		return 0, fmt.Errorf("firestore returned no count")
	}
	return value.GetIntegerValue(), nil
}
