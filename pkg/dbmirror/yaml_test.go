package dbmirror

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ingitdb/ingitdb-go/ingitdb"
	"gopkg.in/yaml.v3"
)

var yamlSpec = &Spec{Def: &ingitdb.CollectionDef{RecordFile: &ingitdb.RecordFileDef{Format: ingitdb.RecordFormatYAML}}}

// yamlStrings are strings YAML readers are known to take for something else:
// YAML 1.1 bools, base-60 numbers, merge and value keys, numbers, dates, null,
// indicators, line breaks and spaces at the ends, controls and odd code points.
var yamlStrings = []string{
	"", " ", "  ", "\n", "\n\n", "\nlead", "\n\nstarts with blank lines", "trail\n", "trail\n\n", " lead", "trail ", " \n ", "a\n b", "a \nb",
	"<<", "=", "~", "-", "?", ":", "#", "&a", "*a", "!tag", "%TAG", "@at", "`bt", "|", ">", "- x", "? x", "a: b", "a #b", "[x]", "{x}", `"`, "'", `\`,
	"y", "Y", "yes", "Yes", "YES", "n", "N", "no", "No", "NO", "on", "On", "ON", "off", "Off", "OFF", "true", "True", "TRUE", "false", "False", "FALSE",
	"null", "Null", "NULL", "1:20", "190:20:30", "-1:20", "10:30", "1:20.5", "010", "0o17", "0x1F", "0b101", "1_000", "+1", "-1", "1e3", "1e+21", "1.0", ".5",
	".inf", "-.inf", ".nan", ".NaN", "Infinity", "NaN", "2026-01-01", "2026-01-01T00:00:00Z", "2026-01-01 10:30:00", "0", "00", "-0", "123", "9223372036854775808",
	"tab\tin", "\ttab", "cr\r\n", "cr\r", "bell\a", "nul\x00", "esc\x1b", "del\x7f", "c1\u0085", "ls ", "ps ", "bom\uFEFF", "\uFEFF", "fffe\uFFFE", "emoji 🙂", "é", "日本語",
	strings.Repeat("long words ", 40), strings.Repeat("x", 300), "spaces  between   words", "a" + strings.Repeat(" ", 100) + "b",
}

// yamlCorpus holds every string as a value, a key, a list item and a nested
// key, and the numbers YAML readers disagree on.
func yamlCorpus() map[string]any {
	items := make([]any, len(yamlStrings))
	keys := map[string]any{}
	nested := map[string]any{}
	for i, s := range yamlStrings {
		items[i] = s
		keys[s] = s
		nested["k"+strconv.Itoa(i)] = map[string]any{s: int64(i)}
	}
	return map[string]any{
		"strings": items, "keys": keys, "nested": nested, "<<": map[string]any{"<<": "x", "own": "y"},
		"floats": []any{1.0, math.Copysign(0, -1), 0.1, 1.5, 1e21, 3e15, 1e16, 5e-324, 1e-7, 1e100, -2.5e-300, math.MaxFloat64, math.SmallestNonzeroFloat64, 123456789.125},
		"ints":   []any{int64(0), int64(10), int64(-10), int64(math.MaxInt64), int64(math.MinInt64), int64(1) << 53},
		"other":  []any{true, false, nil, []any{}, map[string]any{}},
	}
}

// M7: every string and key reads back through inGitDB's own parser.
func TestYAMLCorpusRoundTrips(t *testing.T) {
	data := yamlCorpus()
	content, err := yamlSpec.encodeChecked(data, "corpus")
	if err != nil {
		t.Fatalf("%v\n%s", err, yamlSpec.encode(data))
	}
	// As the ids of a file kept by id, too.
	records := map[string]map[string]any{}
	for i, s := range yamlStrings {
		if s != "" {
			records[s] = map[string]any{"n": int64(i)}
		}
	}
	if _, err := encodeRecords(yamlSpec, records, "ids.yaml"); err != nil {
		t.Error(err)
	}
	// M8: a YAML 1.1 reader takes nothing for another type.
	assertYAML11Rules(t, string(content))
	readByPyYAML(t, content, data)
}

// Random strings of any code point read back too.
func TestYAMLRandomStringsRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	runes := []rune{'a', ' ', '\n', '\t', '\r', ':', '#', '-', '?', '<', '=', '~', '"', '\'', '\\', '\u0085', ' ', '\uFEFF', 0, 0x1b, 0x7f, 'y', 'n', '0', '1', '.', 'e', '+', '🙂', 0x10FFFF, 0xFFFD}
	for i := 0; i < 3000; i++ {
		var b strings.Builder
		for n := rng.IntN(12); n > 0; n-- {
			if rng.IntN(4) == 0 {
				b.WriteRune(rune(rng.IntN(0x10FFFF)))
			} else {
				b.WriteRune(runes[rng.IntN(len(runes))])
			}
		}
		s := strings.ToValidUTF8(b.String(), "?") // Firestore strings are UTF-8
		if !utf8.ValidString(s) {
			continue
		}
		data := map[string]any{"v": s, "m": map[string]any{s: s}, "l": []any{s}}
		if _, err := yamlSpec.encodeChecked(data, "random"); err != nil {
			t.Fatalf("%q: %v\n%s", s, err, yamlSpec.encode(data))
		}
	}
}

var (
	yaml11Float = regexp.MustCompile(`^[-+]?([0-9][0-9_]*)?\.[0-9.]*([eE][-+][0-9]+)?$`)
	yaml12Float = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
	decimalInt  = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
)

// assertYAML11Rules checks every scalar of the written YAML against the YAML
// 1.1 resolution rules (yaml.org/type): every string and key is
// double-quoted, so no rule applies to it; a double matches the float rule of
// both YAML 1.1 and 1.2; an integer is decimal (no octal 010, no base 60);
// bools and null are the forms both versions read alike.
func assertYAML11Rules(t *testing.T, content string) {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil {
		t.Fatal(err)
	}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		for _, child := range n.Content {
			walk(child)
		}
		if n.Kind != yaml.ScalarNode {
			return
		}
		switch n.Tag {
		case "!!str":
			if n.Style != yaml.DoubleQuotedStyle {
				t.Errorf("a string not double-quoted: %q", n.Value)
			}
		case "!!float":
			if !yaml11Float.MatchString(n.Value) || !yaml12Float.MatchString(n.Value) {
				t.Errorf("%q is not a double for YAML 1.1 and 1.2", n.Value)
			}
		case "!!int":
			if !decimalInt.MatchString(n.Value) {
				t.Errorf("%q is not a decimal integer", n.Value)
			}
		case "!!bool", "!!null":
			if !slices.Contains([]string{"true", "false", "null"}, n.Value) {
				t.Errorf("%q", n.Value)
			}
		default:
			t.Errorf("a scalar tagged %s: %q", n.Tag, n.Value)
		}
	}
	walk(&root)
	for _, f := range yamlCorpus()["floats"].([]any) {
		if text := yamlFloat(f.(float64)); !yaml11Float.MatchString(text) || !yaml12Float.MatchString(text) {
			t.Errorf("%v is written %q", f, text)
		}
	}
}

// requirePyYAML is the variable that makes a missing PyYAML a failure, not a
// skip: CI sets it where it installs PyYAML, so the read-back is binding there.
const requirePyYAML = "DATATUG_REQUIRE_PYYAML"

// readByPyYAML reads content with PyYAML (YAML 1.1) when python3 has it, and
// compares the types and values with data.
func readByPyYAML(t *testing.T, content []byte, data map[string]any) {
	t.Helper()
	if exec.Command("python3", "-c", "import yaml").Run() != nil {
		if os.Getenv(requirePyYAML) != "" {
			t.Fatalf("PyYAML is not available, and %s is set: install it (python3 -m pip install PyYAML)", requirePyYAML)
		}
		t.Log("PyYAML is not available: the YAML 1.1 rule table above stands in for it")
		return
	}
	file := filepath.Join(t.TempDir(), "corpus.yaml")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
	script := `import json, math, sys, yaml
def typed(v):
    if isinstance(v, bool): return ["bool", v]
    if v is None: return ["null"]
    if isinstance(v, int): return ["int", str(v)]
    if isinstance(v, float): return ["float", repr(v)]
    if isinstance(v, str): return ["str", v]
    if isinstance(v, list): return ["list", [typed(x) for x in v]]
    if isinstance(v, dict): return ["map", {k if isinstance(k, str) else "NOT A STRING KEY " + repr(k): typed(x) for k, x in v.items()}]
    return ["other", repr(v)]
print(json.dumps(typed(yaml.safe_load(open(sys.argv[1], encoding="utf-8")))))`
	out, err := exec.Command("python3", "-I", "-c", script, file).Output()
	if err != nil {
		t.Fatalf("PyYAML: %v", err)
	}
	var got any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(pyTyped(data))
	var wantAny any
	_ = json.Unmarshal(want, &wantAny)
	if gotJSON, _ := json.Marshal(got); string(gotJSON) != string(want) {
		t.Errorf("PyYAML reads other data:\ngot  %s\nwant %s", gotJSON, want)
	}
}

// pyTyped is the typed form the PyYAML script prints.
func pyTyped(v any) any {
	switch value := v.(type) {
	case bool:
		return []any{"bool", value}
	case nil:
		return []any{"null"}
	case int64:
		return []any{"int", strconv.FormatInt(value, 10)}
	case float64:
		return []any{"float", pyRepr(value)}
	case string:
		return []any{"str", value}
	case []any:
		items := make([]any, len(value))
		for i, item := range value {
			items[i] = pyTyped(item)
		}
		return []any{"list", items}
	case map[string]any:
		fields := map[string]any{}
		for key, item := range value {
			fields[key] = pyTyped(item)
		}
		return []any{"map", fields}
	}
	return nil
}

// pyRepr is Python's repr of a double: the shortest digits, an exponent below
// 1e-4 and from 1e16, and always a "." or an exponent.
func pyRepr(f float64) string {
	if f == 0 {
		return map[bool]string{true: "-0.0", false: "0.0"}[math.Signbit(f)]
	}
	exp := math.Floor(math.Log10(math.Abs(f)))
	if exp < -4 || exp >= 16 {
		text := strconv.FormatFloat(f, 'e', -1, 64) // 1e+21, 5e-324
		mantissa, e, _ := strings.Cut(text, "e")
		sign, digits := e[:1], strings.TrimLeft(e[1:], "0")
		if len(digits) < 2 {
			digits = strings.Repeat("0", 2-len(digits)) + digits
		}
		return mantissa + "e" + sign + digits
	}
	text := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(text, ".") {
		text += ".0"
	}
	return text
}

// The review's repro: ids and values that broke the YAML before are written
// and read back exactly, and a second run changes nothing.
func TestRunWritesYAMLThatReadsBack(t *testing.T) {
	src := newMemSource()
	src.docs["tags/<<"] = map[string]any{"title": "merge"}
	src.docs["tags/yes"] = map[string]any{"title": "yes"}
	src.docs["tags/on"] = map[string]any{"title": "on"}
	src.docs["tags/ok"] = map[string]any{"title": "\nlead"}
	src.docs["groups/g1"] = map[string]any{"title": "10:30", "note": "\n\nx"}
	src.docs["groups/g2"] = map[string]any{"title": "=", "data": map[string]any{"<<": "x"}}
	src.docs["groups/g3"] = map[string]any{"title": "no", "data": map[string]any{"<<": map[string]any{"k": "v"}, "own": "y"}}
	out := t.TempDir()
	report := runYAML(t, src, Options{Output: out})
	if len(report.Rejected) != 0 || len(report.Validation) != 0 || report.Records != 7 {
		t.Fatalf("%+v", report)
	}
	tags, err := readRecords(filepath.Join(out, "tags/tags.yaml"), yamlDefinitions(t).Spec("tags"))
	if err != nil || len(tags) != 4 || tags["<<"]["title"] != "merge" || tags["ok"]["title"] != "\nlead" || tags["yes"]["title"] != "yes" {
		t.Errorf("%v %v", err, tags)
	}
	g3, err := ingitdb.ParseRecordContent([]byte(readFile(t, filepath.Join(out, "groups/g3.yaml"))), ingitdb.RecordFormatYAML)
	if err != nil || g3["title"] != "no" || len(g3["data"].(map[string]any)) != 2 {
		t.Errorf("%v %v", err, g3)
	}
	if again := runYAML(t, src, Options{Output: out}); again.Written != 0 || again.Complete != report.Complete {
		t.Errorf("%+v", again)
	}
}

// M7/m2: a writer that does not read back stops the run, and writes no file.
func TestRunStopsWhenAFileWouldNotReadBack(t *testing.T) {
	broken := func(when func(map[string]any) bool, write func(map[string]any) []byte) func(map[string]any) []byte {
		return func(data map[string]any) []byte {
			if when(data) {
				return write(data)
			}
			return encodeYAML(data)
		}
	}
	unreadable := func(map[string]any) []byte { return []byte("a: [") }
	other := func(map[string]any) []byte { return []byte("{}\n") }
	always := func(map[string]any) bool { return true }
	for name, writer := range map[string]func(map[string]any) []byte{
		"unreadable":            broken(always, unreadable),
		"other data":            broken(always, other),
		"a file kept by id":     broken(func(d map[string]any) bool { _, ok := d["Work"]; return ok }, other),
		"the file of all of it": broken(func(d map[string]any) bool { return len(d) >= 4 && d["Work"] != nil }, other),
	} {
		withSeam(t, &yamlWriter, writer)
		out := t.TempDir()
		report, err := Run(context.Background(), yamlSource(), yamlDefinitions(t), yamlPolicy(), Options{Output: out, Collections: []string{"tags"}})
		if !errors.Is(err, ErrRoundTrip) || report == nil || report.Complete || !strings.Contains(strings.Join(report.Incomplete, ";"), "stopped: ") {
			t.Errorf("%s: %v %+v", name, err, report)
		}
		if _, err := os.Stat(filepath.Join(out, "tags/tags.yaml")); !os.IsNotExist(err) {
			t.Errorf("%s: a record file was written", name)
		}
	}
	// A partial chunk and a pruned file are checked too.
	withSeam(t, &yamlWriter, broken(func(d map[string]any) bool { return len(d) == 2 && d["Work"] != nil }, other))
	if _, err := Run(context.Background(), yamlSource(), yamlDefinitions(t), yamlPolicy(), Options{Output: t.TempDir(), Collections: []string{"tags"}, MaxReads: 3, PageSize: 2}); !errors.Is(err, ErrRoundTrip) {
		t.Errorf("chunk: %v", err)
	}
	withSeam(t, &yamlWriter, encodeYAML)
	out := t.TempDir()
	src := yamlSource()
	runYAML(t, src, Options{Output: out, Collections: []string{"tags"}})
	delete(src.docs, "tags/123")
	calls := 0
	withSeam(t, &yamlWriter, func(d map[string]any) []byte {
		if calls++; len(d) == 3 && d["Work"] != nil && d["work"] != nil && calls > 5 {
			return other(d)
		}
		return encodeYAML(d)
	})
	if _, err := Run(context.Background(), src, yamlDefinitions(t), yamlPolicy(), Options{Output: out, Collections: []string{"tags"}, Prune: true}); !errors.Is(err, ErrRoundTrip) {
		t.Errorf("prune: %v (%d calls)", err, calls)
	}
}

// m1: chunks go once their collection is written, and a run that starts over
// loads none of an earlier run's.
func TestRunClearsPartialChunks(t *testing.T) {
	out := t.TempDir()
	src := yamlSource()
	opts := Options{Output: out, Collections: []string{"tags"}, MaxReads: 3, PageSize: 2}
	runYAML(t, src, opts)
	chunks := filepath.Join(partialDir(out), "tags/tags.yaml.parts")
	if entries, _ := os.ReadDir(chunks); len(entries) != 1 {
		t.Fatalf("%v", entries)
	}
	// (a) The run that completes the collection removes them.
	runYAML(t, src, Options{Output: out, Collections: []string{"tags"}})
	if _, err := os.Stat(partialDir(out)); !os.IsNotExist(err) {
		t.Error("a completed collection left its chunks")
	}
	// (b) A restart clears the chunks of the run it abandons: a record only
	// that run read, and no longer in the source, is not written.
	out = t.TempDir()
	opts.Output = out
	runYAML(t, src, opts)
	first, _ := os.ReadDir(filepath.Join(partialDir(out), "tags/tags.yaml.parts"))
	gone := ""
	for id := range src.docs {
		if strings.HasPrefix(id, "tags/") && strings.Contains(readFile(t, filepath.Join(partialDir(out), "tags/tags.yaml.parts", first[0].Name())), strconv.Quote(strings.TrimPrefix(id, "tags/"))) {
			gone = id
		}
	}
	delete(src.docs, gone)
	restarted := opts
	restarted.Restart, restarted.MaxReads = true, 1 // stops before it reads a page: it saves no chunk of its own
	runYAML(t, src, restarted)
	runYAML(t, src, Options{Output: out, Collections: []string{"tags"}})
	records, err := readRecords(filepath.Join(out, "tags/tags.yaml"), yamlDefinitions(t).Spec("tags"))
	if err != nil || len(records) != 3 || records[strings.TrimPrefix(gone, "tags/")] != nil {
		t.Errorf("%v: %d records, %s kept: %v", err, len(records), gone, records[strings.TrimPrefix(gone, "tags/")] != nil)
	}
}

func TestYAMLFloat(t *testing.T) {
	for f, want := range map[float64]string{1: "1.0", 1e21: "1.0e+21", 5e-324: "5.0e-324", 1.5e-7: "1.5e-07", 0.1: "0.1", 123.25: "123.25"} {
		if got := yamlFloat(f); got != want {
			t.Errorf("%v: %q", f, got)
		}
	}
}

// JSON reads back with its numbers as written: an int stays an int, so a file
// kept by id is rewritten as it was.
func TestJSONReadsBackItsNumbers(t *testing.T) {
	spec := &Spec{Def: &ingitdb.CollectionDef{RecordFile: &ingitdb.RecordFileDef{Format: ingitdb.RecordFormatJSON}}}
	data := map[string]any{"i": int64(3), "f": 3.0, "big": int64(math.MaxInt64), "e": 1e21, "s": "<<", "l": []any{int64(1), 1.5}}
	if _, err := spec.encodeChecked(data, "json"); err != nil {
		t.Error(err)
	}
	if _, err := spec.decode([]byte("{")); err == nil {
		t.Error("decoded broken JSON")
	}
}
