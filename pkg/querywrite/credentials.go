package querywrite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// embeddedCredentialReason is the released datatug-core credential screen.
// Keeping the local name makes every CLI write path share that single policy.
var embeddedCredentialReason = datatug.EmbeddedCredentialReason

// CredentialReason reports why value appears to embed a secret - a URL or
// DSN password, a secret key/value pair or JSON member, an HTTP credential
// header - or ("", false) when it does not. It is the one credential screen
// every query write path applies to the text a query persists.
func CredentialReason(value string) (reason string, found bool) {
	return embeddedCredentialReason(value)
}

// QueryCredentialReason applies CredentialReason to every field of q that
// a query write persists as free text into git-tracked project files: its
// title, purpose and text (the SQL, DTQL, GraphQL or HTTP body - on these
// write paths code is screened too, unlike datatug-core's own
// QueryDef.Validate, which screens HTTP text only), each parameter's title
// and default (defaultValueCredentialReason) and each target's
// connection-string-like fields. It returns the first offending field,
// named the way the request names it ("parameters[0].defaultValue").
func QueryCredentialReason(q *datatug.QueryDef) (field, reason string, found bool) {
	type candidate struct{ field, value string }
	candidates := []candidate{{"title", q.Title}, {"purpose", queryPurpose(q)}, {"text", q.Text}}
	for i, p := range q.Parameters {
		candidates = append(candidates, candidate{fmt.Sprintf("parameters[%d].title", i), p.Title})
	}
	for i, t := range q.Targets {
		for _, f := range []candidate{{"driver", t.Driver}, {"catalog", t.Catalog}, {"protocol", t.Protocol}, {"host", t.Host}, {"username", t.Username}} {
			candidates = append(candidates, candidate{fmt.Sprintf("targets[%d].%s", i, f.field), f.value})
		}
	}
	for _, c := range candidates {
		if reason, found := CredentialReason(c.value); found {
			return c.field, reason, true
		}
	}
	for i, p := range q.Parameters {
		if reason, found := defaultValueCredentialReason(p.DefaultValue); found {
			return fmt.Sprintf("parameters[%d].defaultValue", i), reason, true
		}
	}
	return "", "", false
}

// defaultValueCredentialReason screens a parameter default: a string as
// itself; any other value in the JSON form it is persisted in, as a whole
// (which finds a secret JSON member such as {"password": "x"}) and string
// by string, every map key included, recursively.
//
// The JSON form is produced with HTML escaping off, as datatug-core's own
// default-value screen produces it. json.Marshal would escape "<", ">" and
// "&", so the placeholder {"password": "<value>"} reached the screen as
// "<value>", which is not the placeholder the screen allows, and
// the CLI refused a default core accepts. The failure was closed, but the
// two screens must agree.
func defaultValueCredentialReason(v any) (reason string, found bool) {
	switch v := v.(type) {
	case nil:
		return "", false
	case string:
		return CredentialReason(v)
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return "", false // not persistable either; the write reports that
	}
	data := bytes.TrimRight(encoded.Bytes(), "\n")
	if reason, found := CredentialReason(string(data)); found {
		return reason, true
	}
	var decoded any
	_ = json.Unmarshal(data, &decoded) // cannot fail on the encoder's own output
	return jsonStringsCredentialReason(decoded)
}

// jsonStringsCredentialReason screens every string and map key in a
// decoded JSON value, map keys in sorted order so the reason is
// deterministic.
func jsonStringsCredentialReason(v any) (reason string, found bool) {
	switch v := v.(type) {
	case string:
		return CredentialReason(v)
	case []any:
		for _, item := range v {
			if reason, found := jsonStringsCredentialReason(item); found {
				return reason, true
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if reason, found := CredentialReason(k); found {
				return reason, true
			}
			if reason, found := jsonStringsCredentialReason(v[k]); found {
				return reason, true
			}
		}
	}
	return "", false
}
