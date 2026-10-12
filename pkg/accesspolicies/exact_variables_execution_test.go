package accesspolicies

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dal-go/dalgo/dal"
	"testing"
)

func TestExactNumericBindingsRefuseUnsafeScalarAndTypedListsBeforeExecution(t *testing.T) {
	query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("items", ""))).Where(dal.WhereField("Id", dal.In, dal.NewParam("ids"))).SelectColumns()
	for _, value := range []any{int64(9007199254740993), float32(9007199254740992), []int64{9007199254740993}, []json.Number{"9007199254740993"}, [1]uint64{9007199254740993}, json.Number("0.10000000000000000001")} {
		session := &stubSession{}
		_, err := Run(context.Background(), session, query, Options{Unrestricted: true, Variables: map[string]any{"ids": value}})
		if !errors.Is(err, ErrInvalidQuery) || session.query != nil {
			t.Fatalf("%T binding reached executor or was not explicitly refused: %v", value, err)
		}
	}
	session := &stubSession{}
	if _, err := Run(context.Background(), session, query, Options{Unrestricted: true, Variables: map[string]any{"ids": []json.Number{"0", "1"}}}); err != nil || session.query == nil {
		t.Fatalf("ordinary persisted integer list failed: %v", err)
	}
}
