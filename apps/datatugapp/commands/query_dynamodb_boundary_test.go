package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/dal-go/dalgo2dynamodb"
)

// An owner can advertise its deadline before its notification is serviced.
// Err/Done remain the real cancellable parent's; no fabricated Err sequence.
type dynamoPendingDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c dynamoPendingDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

type dynamoProbeReader struct{ attempted *bool }

func (r dynamoProbeReader) Read([]byte) (int, error) { *r.attempted = true; return 0, io.EOF }

func TestDynamoAdmissionHonorsElapsedDeadlineBeforeNotification(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := dynamoPendingDeadlineContext{Context: parent, deadline: time.Now().Add(-time.Second)}
	attempted := false
	if data, err := readDynamoDocument(ctx, dynamoProbeReader{&attempted}); !errors.Is(err, context.DeadlineExceeded) || data != nil || attempted {
		t.Fatal("elapsed deadline admitted input before notification")
	}
	// An already observed caller cancellation retains priority over elapsed time.
	cancel()
	if _, err := readDynamoDocument(ctx, dynamoProbeReader{&attempted}); !errors.Is(err, context.Canceled) || attempted {
		t.Fatal("caller cancellation priority changed")
	}
	reopen, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if data, err := readDynamoDocument(reopen, strings.NewReader("{}")); err != nil || string(data) != "{}" {
		t.Fatal("elapsed refusal leaked an admission slot")
	}
}

// Revocation can happen when the caller's cancellation channel is subscribed.
// Both select outcomes are valid; every invocation must refuse I/O. No method
// count controls Err, and cancellation is always the real parent's state.
type dynamoCancelOnSubscribeContext struct {
	context.Context
	cancel context.CancelFunc
}

func (c dynamoCancelOnSubscribeContext) Done() <-chan struct{} { c.cancel(); return c.Context.Done() }
func TestDynamoAdmissionRaceNeverReadsAfterCallerRevocation(t *testing.T) {
	for range 32 {
		parent, cancel := context.WithCancel(context.Background())
		ctx := dynamoCancelOnSubscribeContext{Context: parent, cancel: cancel}
		attempted := false
		data, err := readDynamoDocument(ctx, dynamoProbeReader{&attempted})
		cancel()
		if !errors.Is(err, context.Canceled) || data != nil || attempted {
			t.Fatal("admission race read input after revocation")
		}
	}
}

// This caller-owned metadata accessor causes REAL cancellation while a
// credential provider queries its deadline. Err is the standard parent's Err.
type dynamoCancelOnDeadlineContext struct {
	context.Context
	cancel  context.CancelFunc
	entered *bool
}

func (c dynamoCancelOnDeadlineContext) Deadline() (time.Time, bool) {
	*c.entered = true
	c.cancel()
	return c.Context.Deadline()
}
func TestDynamoCredentialDeliveryObservesCallerMetadataCancellation(t *testing.T) {
	dynamoTestCredentials(t)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := false
	ctx := dynamoCancelOnDeadlineContext{Context: parent, cancel: cancel, entered: &entered}
	credentials, err := (dynamoEnvCredentials{}).Retrieve(ctx)
	if !entered || !errors.Is(err, context.Canceled) || credentials.AccessKeyID != "" {
		t.Fatal("credential completion ignored genuine caller cancellation")
	}
}

type dynamoEOFReader struct {
	reader    io.Reader
	completed chan struct{}
	once      sync.Once
}

func (r *dynamoEOFReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF {
		r.once.Do(func() { close(r.completed) })
	}
	return n, err
}

// The owner revokes the operation when its real EOF witness is observed. This
// delegates to a real context, has no Err-call counter and invents no errors.
type dynamoReadCompletionContext struct {
	context.Context
	completed <-chan struct{}
	cancel    context.CancelFunc
}

func (c dynamoReadCompletionContext) Err() error {
	select {
	case <-c.completed:
		c.cancel()
	default:
	}
	return c.Context.Err()
}
func TestDynamoReadCompletionRechecksCallerBeforeDelivery(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan struct{})
	ctx := dynamoReadCompletionContext{Context: parent, completed: completed, cancel: cancel}
	reader := &dynamoEOFReader{reader: strings.NewReader("{}"), completed: completed}
	if data, err := readDynamoDocument(ctx, reader); !errors.Is(err, context.Canceled) || data != nil {
		t.Fatal("completed input delivered after caller revocation")
	}
	select {
	case <-completed:
	default:
		t.Fatal("input never actually completed")
	}
}

// Fault injection is at the real decoded-provider boundary. Production remains
// the pinned official-SDK-backed provider (compile-time assignment in command).
type dynamoMalformedReadClient struct{ item dalgo2dynamodb.Item }

func (c dynamoMalformedReadClient) Get(context.Context, string, dalgo2dynamodb.Item) (dalgo2dynamodb.Item, error) {
	return c.item, nil
}
func (c dynamoMalformedReadClient) Exists(context.Context, string, dalgo2dynamodb.Item) (bool, error) {
	return false, nil
}
func (c dynamoMalformedReadClient) GetMany(context.Context, string, []dalgo2dynamodb.Item) ([]dalgo2dynamodb.Item, error) {
	return []dalgo2dynamodb.Item{{"valid": &types.AttributeValueMemberS{Value: "first"}}, c.item}, nil
}
func (c dynamoMalformedReadClient) QueryPartition(context.Context, dalgo2dynamodb.QueryRequest) (dalgo2dynamodb.QueryPage, error) {
	return dalgo2dynamodb.QueryPage{Items: []dalgo2dynamodb.Item{c.item}}, nil
}
func TestDynamoDecodedProviderFaultsFailClosedBeforeStdout(t *testing.T) {
	config := dynamoNativeConfig{Tables: map[string]dalgo2dynamodb.Table{"items": {Name: "existing", PartitionKey: "account"}}}
	documents := []string{
		`{"operation":"get","collection":"items","key":{"account":{"S":"one"}}}`,
		`{"operation":"getMany","collection":"items","keys":[{"account":{"S":"one"}}]}`,
		`{"operation":"query","collection":"items","partition":{"S":"one"},"limit":1}`,
	}
	faults := []types.AttributeValue{
		&types.UnknownUnionMember{Tag: "future-private-tag", Value: []byte("synthetic-private-detail")},
		&types.AttributeValueMemberL{Value: []types.AttributeValue{nil}},
		&types.AttributeValueMemberS{Value: string([]byte{0xff})},
	}
	for i, document := range documents {
		var request dynamoNativeRequest
		if err := json.Unmarshal([]byte(document), &request); err != nil {
			t.Fatal(err)
		}
		admitted, err := admitDynamoRead(config, request)
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		err = deliverDynamoRead(context.Background(), &output, dynamoMalformedReadClient{item: dalgo2dynamodb.Item{"value": faults[i]}}, admitted)
		if err == nil || output.Len() != 0 || strings.Contains(err.Error(), "private") {
			t.Fatal("malformed provider value escaped redaction or wrote a prefix")
		}
	}
}

func TestQueryDynamoTransportRefusalAtActualCommandBoundary(t *testing.T) {
	dynamoTestCredentials(t)
	var called atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called.Store(true) }))
	t.Cleanup(server.Close)
	config := dynamoTestJSON(t, "invalid-origin.json", dynamoNativeConfig{
		Endpoint: strings.Replace(server.URL, "https://", "https://user:synthetic-private-detail@", 1),
		Region:   "us-east-1", Tables: map[string]dalgo2dynamodb.Table{"items": {Name: "existing", PartitionKey: "account"}},
	})
	output, diagnostic, err := dynamoTestCommand(t, context.Background(), config, `{"operation":"get","collection":"items","key":{"account":{"S":"one"}}}`, "2s")
	if err == nil || output != "" || called.Load() || strings.Contains(err.Error()+diagnostic, "private") {
		t.Fatal("invalid transport reached service or leaked")
	}
}
