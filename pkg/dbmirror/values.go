package dbmirror

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/ingitdb/ingitdb-go/ingitdb"
	"google.golang.org/genproto/googleapis/type/latlng"
)

// Tags of Firestore values that JSON has no type for. Each is an object with
// one key, so a record file stays plain JSON and the value is kept exactly.
const (
	tagBytes     = "$bytes"  // base64 (standard, padded) of the bytes
	tagReference = "$ref"    // path of the referenced document, relative to the database
	tagGeoPoint  = "$geo"    // {"latitude": ..., "longitude": ...}
	tagVector    = "$vector" // the vector's numbers
	tagFloat     = "$float"  // "NaN", "Infinity" or "-Infinity"
	tagTime      = "$time"   // a timestamp outside a datetime column, as timeLayout text
	tagMap       = "$map"    // a map of the source that would read as one of these tags
	maskedString = "***"     // what a masked string field holds
	// timeLayout is RFC 3339 in UTC with the 6 fraction digits Firestore keeps,
	// always written, so timestamps sort as text.
	timeLayout = "2006-01-02T15:04:05.000000Z07:00"
)

var tags = []string{tagBytes, tagReference, tagGeoPoint, tagVector, tagFloat, tagTime, tagMap}

// convertRecord converts the fields of a document into those of its record:
// a timestamp in a datetime column (or an item of a []datetime column) is the
// column's text, every other value is jsonValue's.
func convertRecord(data map[string]any, def *ingitdb.CollectionDef) (map[string]any, error) {
	fields := make(map[string]any, len(data))
	for key, value := range data {
		typ := ""
		if column := def.Columns[key]; column != nil {
			typ = string(column.Type)
		}
		var err error
		switch typed := value.(type) {
		case time.Time:
			if typ == string(ingitdb.ColumnTypeDateTime) {
				fields[key] = typed.UTC().Format(timeLayout)
				continue
			}
		case []any:
			if typ == "[]"+string(ingitdb.ColumnTypeDateTime) {
				items := make([]any, len(typed))
				for i, item := range typed {
					if at, isTime := item.(time.Time); isTime {
						items[i] = at.UTC().Format(timeLayout)
					} else if items[i], err = jsonValue(item, key+"."+strconv.Itoa(i)); err != nil {
						return nil, err
					}
				}
				fields[key] = items
				continue
			}
		}
		if fields[key], err = jsonValue(value, key); err != nil {
			return nil, err
		}
	}
	return fields, nil
}

// jsonValue converts a value read from Firestore (as the Google SDK returns
// it) into the value its record file holds:
//
//   - null, booleans and strings as themselves, maps and arrays item by item;
//   - integers as JSON integers and doubles as JSON numbers that always carry a
//     decimal point or exponent, so 1.0 stays a double;
//   - timestamps as {"$time": RFC 3339 text in UTC with 6 fraction digits}
//     (convertRecord writes the bare text into a datetime column);
//   - bytes, references, geo points, vectors and the doubles NaN and ±Infinity
//     as one-key objects tagged $bytes, $ref, $geo, $vector and $float;
//   - a map whose only key is one of these tags as {"$map": the map}, so it is
//     not read back as the tagged value.
//
// path names the field for an error about a type Firestore added later.
func jsonValue(v any, path string) (any, error) {
	switch value := v.(type) {
	case nil, bool, string, int64:
		return value, nil
	case float64:
		switch {
		case math.IsNaN(value):
			return map[string]any{tagFloat: "NaN"}, nil
		case math.IsInf(value, 1):
			return map[string]any{tagFloat: "Infinity"}, nil
		case math.IsInf(value, -1):
			return map[string]any{tagFloat: "-Infinity"}, nil
		}
		return value, nil
	case time.Time:
		return map[string]any{tagTime: value.UTC().Format(timeLayout)}, nil
	case []byte:
		return map[string]any{tagBytes: base64.StdEncoding.EncodeToString(value)}, nil
	case *firestore.DocumentRef:
		return map[string]any{tagReference: relativePath(value.Path)}, nil
	case *latlng.LatLng:
		return map[string]any{tagGeoPoint: map[string]any{"latitude": value.GetLatitude(), "longitude": value.GetLongitude()}}, nil
	case firestore.Vector64:
		items := make([]any, len(value))
		for i, f := range value {
			items[i] = f
		}
		return map[string]any{tagVector: items}, nil
	case firestore.Vector32:
		items := make([]any, len(value))
		for i, f := range value {
			items[i] = float64(f)
		}
		return map[string]any{tagVector: items}, nil
	case []any:
		items := make([]any, len(value))
		for i, item := range value {
			converted, err := jsonValue(item, path+"."+strconv.Itoa(i))
			if err != nil {
				return nil, err
			}
			items[i] = converted
		}
		return items, nil
	case map[string]any:
		fields := make(map[string]any, len(value))
		for key, item := range value {
			converted, err := jsonValue(item, joinField(path, key))
			if err != nil {
				return nil, err
			}
			fields[key] = converted
		}
		if len(fields) == 1 {
			for key := range fields {
				if slices.Contains(tags, key) {
					return map[string]any{tagMap: fields}, nil
				}
			}
		}
		return fields, nil
	}
	return nil, fmt.Errorf("field %q holds a Firestore value of type %T that the mirror cannot write", strings.TrimPrefix(path, "."), v)
}

func joinField(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// relativePath is a document resource name
// (projects/P/databases/D/documents/a/b) without its database prefix.
func relativePath(name string) string {
	if _, rest, ok := strings.Cut(name, "/documents/"); ok {
		return rest
	}
	return name
}

// encodeRecord is the canonical bytes of a record file: JSON indented by two
// spaces, object keys in byte order, no HTML escaping, and a final newline.
// The same data always gives the same bytes, so a re-run over unchanged data
// changes no file.
func encodeRecord(data map[string]any) []byte {
	var buf bytes.Buffer
	writeJSON(&buf, data, "")
	buf.WriteByte('\n')
	return buf.Bytes()
}

func writeJSON(buf *bytes.Buffer, v any, indent string) {
	switch value := v.(type) {
	case map[string]any:
		if len(value) == 0 {
			buf.WriteString("{}")
			return
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		buf.WriteString("{\n")
		for i, key := range keys {
			buf.WriteString(indent + "  ")
			writeString(buf, key)
			buf.WriteString(": ")
			writeJSON(buf, value[key], indent+"  ")
			if i < len(keys)-1 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(indent + "}")
	case []any:
		if len(value) == 0 {
			buf.WriteString("[]")
			return
		}
		buf.WriteString("[\n")
		for i, item := range value {
			buf.WriteString(indent + "  ")
			writeJSON(buf, item, indent+"  ")
			if i < len(value)-1 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(indent + "]")
	case string:
		writeString(buf, value)
	case float64:
		text := strconv.FormatFloat(value, 'g', -1, 64)
		if !strings.ContainsAny(text, ".e") {
			text += ".0"
		}
		buf.WriteString(text)
	case int64:
		buf.WriteString(strconv.FormatInt(value, 10))
	case bool:
		buf.WriteString(strconv.FormatBool(value))
	default: // nil: jsonValue returns no other type
		buf.WriteString("null")
	}
}

func writeString(buf *bytes.Buffer, s string) {
	encoder := json.NewEncoder(buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(s) // a string always encodes
	buf.Truncate(buf.Len() - 1)
}

// decodeRecord reads record file bytes back the way inGitDB reads them, so a
// record is checked against its definition exactly as `ingitdb validate` will.
func decodeRecord(content []byte) map[string]any {
	var data map[string]any
	_ = json.Unmarshal(content, &data) // encodeRecord always writes valid JSON
	return data
}

// applyMasks replaces each masked field of data: a string by "***", any other
// value is removed (an item of an array becomes null). A path is field names
// joined by "."; "*" stands for every key of a map or every item of an array.
// It returns the masked paths that matched a value.
func applyMasks(data map[string]any, masks []string) []string {
	var hit []string
	for _, mask := range masks {
		if maskPath(data, strings.Split(mask, ".")) {
			hit = append(hit, mask)
		}
	}
	return hit
}

func maskPath(container any, path []string) bool {
	matched := false
	visit := func(get func() any, set func(any), remove func()) {
		value := get()
		if len(path) == 1 {
			if _, ok := value.(string); ok {
				set(maskedString)
			} else {
				remove()
			}
			matched = true
			return
		}
		if maskPath(value, path[1:]) {
			matched = true
		}
	}
	switch typed := container.(type) {
	case map[string]any:
		keys := []string{path[0]}
		if path[0] == "*" {
			keys = slices.Sorted(maps.Keys(typed))
		}
		for _, key := range keys {
			if _, ok := typed[key]; !ok {
				continue
			}
			visit(func() any { return typed[key] }, func(v any) { typed[key] = v }, func() { delete(typed, key) })
		}
	case []any:
		for i := range typed {
			if path[0] != "*" && path[0] != strconv.Itoa(i) {
				continue
			}
			visit(func() any { return typed[i] }, func(v any) { typed[i] = v }, func() { typed[i] = nil })
		}
	}
	return matched
}

// temporalLayouts are the texts a date, time or datetime column accepts.
var temporalLayouts = map[ingitdb.ColumnType][]string{
	ingitdb.ColumnTypeDateTime: {time.RFC3339Nano},
	ingitdb.ColumnTypeDate:     {time.DateOnly},
	ingitdb.ColumnTypeTime:     {"15:04", time.TimeOnly},
}

// temporalProblems checks what inGitDB does not: that the text in each date,
// time and datetime column of a record is one. It names the fields, never
// their values.
func temporalProblems(def *ingitdb.CollectionDef, data map[string]any) []string {
	var problems []string
	for _, key := range sortedKeys(def.Columns) {
		layouts := temporalLayouts[def.Columns[key].Type]
		text, isText := data[key].(string)
		if len(layouts) == 0 || !isText {
			continue
		}
		parsed := false
		for _, layout := range layouts {
			if _, err := time.Parse(layout, text); err == nil {
				parsed = true
			}
		}
		if !parsed {
			problems = append(problems, fmt.Sprintf("field %q: not a valid %s (the value is not shown)", key, def.Columns[key].Type))
		}
	}
	return problems
}
