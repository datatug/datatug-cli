package dbmirror

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/ingitdb/ingitdb-go/ingitdb"
	"gopkg.in/yaml.v3"
)

// isYAML reports whether a collection's record files are YAML.
func (s *Spec) isYAML() bool { return s.Def.RecordFile.Format != ingitdb.RecordFormatJSON }

// encode is the content of a record file of spec: one record, or (for a
// collection kept in one file) the records by id. The same data always gives
// the same bytes.
func (s *Spec) encode(data map[string]any) []byte {
	if s.isYAML() {
		return yamlWriter(data)
	}
	return encodeRecord(data)
}

// yamlWriter is encodeYAML (a test seam: a writer that breaks must stop the run).
var yamlWriter = encodeYAML

// decode reads record file content back with the types it was written with:
// YAML by inGitDB's own parser, JSON with its numbers as written (inGitDB's
// JSON reader turns whole numbers into doubles).
func (s *Spec) decode(content []byte) (map[string]any, error) {
	if s.isYAML() {
		return ingitdb.ParseRecordContent(content, s.Def.RecordFile.Format)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

// ErrRoundTrip is the failure of a run that would write a record file that
// does not read back as what it was meant to hold. The file is not written;
// the run stops there, like at an id too long (ErrIDTooLong).
var ErrRoundTrip = errors.New("a record file would not read back as written")

// encodeChecked encodes data and reads it back: content that does not decode,
// or decodes to anything but data, is an error, never a file.
func (s *Spec) encodeChecked(data map[string]any, what string) ([]byte, error) {
	content := s.encode(data)
	back, err := s.decode(content)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrRoundTrip, what, err)
	}
	if !reflect.DeepEqual(canonical(back), any(data)) {
		return nil, fmt.Errorf("%w: %s reads back as other data", ErrRoundTrip, what)
	}
	return content, nil
}

// encodeYAML is the canonical YAML of a record: keys in byte order, indented
// by two spaces, every string and every key double-quoted (with escapes), so
// no YAML 1.1 or 1.2 reader takes one for a bool, a number, a date, null, a
// merge key or a tag; a double always with a decimal point in its mantissa
// (1.0, 1.0e+21) so both read it as a double; integers in decimal; and a final
// newline.
func encodeYAML(data map[string]any) []byte {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	_ = encoder.Encode(yamlNode(data)) // a node tree of scalars, maps and lists always encodes
	_ = encoder.Close()
	return buf.Bytes()
}

func yamlString(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s, Style: yaml.DoubleQuotedStyle}
}

func yamlNode(v any) *yaml.Node {
	switch value := v.(type) {
	case map[string]any:
		node := &yaml.Node{Kind: yaml.MappingNode}
		if len(value) == 0 {
			node.Style = yaml.FlowStyle
		}
		for _, key := range slices.Sorted(maps.Keys(value)) {
			node.Content = append(node.Content, yamlString(key), yamlNode(value[key]))
		}
		return node
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode}
		if len(value) == 0 {
			node.Style = yaml.FlowStyle
		}
		for _, item := range value {
			node.Content = append(node.Content, yamlNode(item))
		}
		return node
	case string:
		return yamlString(value)
	case float64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: yamlFloat(value)}
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(value, 10)}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(value)}
	default: // nil: canonical returns no other type
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
}

// yamlFloat is a double as both YAML 1.1 (which needs a "." and a signed
// exponent) and YAML 1.2 read it: 1.0, -0.0, 1.5, 1.0e+21, 5.0e-324.
func yamlFloat(f float64) string {
	text := strconv.FormatFloat(f, 'g', -1, 64)
	mantissa, exponent, scientific := strings.Cut(text, "e")
	if !strings.Contains(mantissa, ".") {
		mantissa += ".0"
	}
	if scientific {
		return mantissa + "e" + exponent // strconv always signs the exponent
	}
	return mantissa
}

// canonical is a value read back from a record file in the types the mirror
// writes (int64, float64, string, bool, nil, maps and lists), so a record
// kept from an earlier run is written again exactly as it was.
func canonical(v any) any {
	switch value := v.(type) {
	case int:
		return int64(value)
	case json.Number:
		if i, err := value.Int64(); err == nil && !strings.ContainsAny(value.String(), ".eE") {
			return i
		}
		f, _ := value.Float64() // JSON numbers are numbers
		return f
	case map[string]any:
		fields := make(map[string]any, len(value))
		for key, item := range value {
			fields[key] = canonical(item)
		}
		return fields
	case []any:
		items := make([]any, len(value))
		for i, item := range value {
			items[i] = canonical(item)
		}
		return items
	}
	return v
}

// multiFile is a record file holding all records of a collection (or of one
// parent's sub-collection), keyed by document id, while a run reads it.
type multiFile struct {
	spec    *Spec
	records map[string]map[string]any // id -> record, this run's and the resumed runs'
	unsaved map[string]map[string]any // read since the last partial save
}

// pending returns the multi-record file at file, which is relative to the output.
func (r *run) pending(spec *Spec, file string) *multiFile {
	if r.multi[file] == nil {
		r.multi[file] = &multiFile{spec: spec, records: map[string]map[string]any{}, unsaved: map[string]map[string]any{}}
	}
	return r.multi[file]
}

// readRecords reads a file of records by id, or none when it does not exist.
func readRecords(path string, spec *Spec) (map[string]map[string]any, error) {
	content, err := outFS.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	data, err := spec.decode(content)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	records := make(map[string]map[string]any, len(data))
	for id, record := range data {
		fields, ok := canonical(record).(map[string]any)
		if !ok {
			return nil, fmt.Errorf("read %s: record %q is not a map", path, id)
		}
		records[id] = fields
	}
	return records, nil
}

// encodeRecords is the checked content of a file of records by id.
func encodeRecords(spec *Spec, records map[string]map[string]any, file string) ([]byte, error) {
	data := make(map[string]any, len(records))
	for id, record := range records {
		data[id] = record
	}
	return spec.encodeChecked(data, filepath.ToSlash(file))
}

func partialDir(out string) string { return filepath.Join(out, stateDirName, "partial") }

// savePartials keeps the records of collections kept in one file that were
// read since the last save, as chunks under .datatug-mirror/partial (which
// inGitDB does not read): the record file itself is written only once its
// collection is read to the end, so a stopped run never leaves a record file
// that looks complete. Each save writes only the new records, so a large
// collection costs one write of each record, not one rewrite per page.
func (r *run) savePartials() error {
	for _, file := range slices.Sorted(maps.Keys(r.multi)) {
		multi := r.multi[file]
		if len(multi.unsaved) == 0 {
			continue
		}
		r.state.Chunks++
		chunk := filepath.Join(partialDir(r.opts.Output), file+".parts", fmt.Sprintf("%08d%s", r.state.Chunks, filepath.Ext(file)))
		content, err := encodeRecords(multi.spec, multi.unsaved, file)
		if err != nil {
			return err
		}
		if _, err := writeFileIfChanged(chunk, content); err != nil {
			return err
		}
		multi.unsaved = map[string]map[string]any{}
	}
	return nil
}

// loadPartials reads the chunks a stopped run saved, so the run that resumes
// it writes their records too.
func (r *run) loadPartials() error {
	root := partialDir(r.opts.Output)
	byFile := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		dir, _ := filepath.Rel(root, filepath.Dir(path))
		file := strings.TrimSuffix(dir, ".parts")
		byFile[file] = append(byFile[file], path)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for file, chunks := range byFile {
		spec := r.defs.specOfFile(file)
		if spec == nil {
			continue // a collection no longer defined: the definitions changed, and the run starts over
		}
		multi := r.pending(spec, file)
		slices.Sort(chunks) // in the order they were saved
		for _, chunk := range chunks {
			records, err := readRecords(chunk, spec)
			if err != nil {
				return err
			}
			maps.Copy(multi.records, records)
		}
	}
	return nil
}

// specOfFile is the collection kept in one file whose record file is file
// (relative to the output).
func (d *Definitions) specOfFile(file string) *Spec {
	for _, spec := range d.Specs {
		if spec.multi && spec.Unsupported == "" && filepath.Base(file) == spec.Def.RecordFile.Name {
			if _, ok := spec.documentOfFile(file, ""); ok {
				return spec
			}
		}
	}
	return nil
}

// documentOfFile is the document path of record id in the record file file
// of a collection kept in one file, and false when file is not one of its
// record files.
func (s *Spec) documentOfFile(file, id string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(file), "/")
	var doc []string
	at := 0
	if s.rootRel != "." && s.rootRel != "" {
		root := strings.Split(filepath.ToSlash(s.rootRel), "/")
		if len(parts) < len(root) || !slices.Equal(parts[:len(root)], root) {
			return "", false
		}
		at = len(root)
	}
	for level, col := range s.chain[:len(s.chain)-1] {
		if base := col.RecordFile.RecordsBasePath(); base != "" {
			if at >= len(parts) || parts[at] != base {
				return "", false
			}
			at++
		}
		if at+1 >= len(parts) || parts[at+1] != s.Segments[level+1] {
			return "", false
		}
		parentID, ok := keyEncoding.Decode(parts[at])
		if !ok {
			return "", false
		}
		doc = append(doc, s.Segments[level], parentID)
		at += 2
	}
	if at != len(parts)-1 || parts[at] != s.Def.RecordFile.Name {
		return "", false
	}
	return strings.Join(append(doc, s.Segments[len(s.Segments)-1], id), "/"), true
}

// flushPending writes every collection kept in one file that the run has read
// to the end: its records merged into the file's earlier ones, which stay
// until a --prune run finds them gone from the source.
func (r *run) flushPending() error {
	for _, file := range slices.Sorted(maps.Keys(r.multi)) {
		multi := r.multi[file]
		path := filepath.Join(r.opts.Output, file)
		records, err := readRecords(path, multi.spec)
		if err != nil {
			return err
		}
		for id, record := range multi.records {
			if reflect.DeepEqual(records[id], record) {
				r.report.Unchanged++
			} else {
				r.report.Written++
			}
			records[id] = record
		}
		content, err := encodeRecords(multi.spec, records, file)
		if err != nil {
			return err
		}
		if _, err := writeFileIfChanged(path, content); err != nil {
			return err
		}
		r.noteWritten(file)
	}
	r.multi = map[string]*multiFile{}
	return removeAll(partialDir(r.opts.Output))
}

// pruneRecords deletes records by id from a record file kept in one file, and
// the file when no record is left.
func pruneRecords(out, file string, spec *Spec, ids []string) error {
	path := filepath.Join(out, file)
	records, err := readRecords(path, spec)
	if err != nil {
		return err
	}
	for _, id := range ids {
		delete(records, id)
	}
	if len(records) == 0 {
		return removeFile(path)
	}
	content, err := encodeRecords(spec, records, file)
	if err != nil {
		return err
	}
	_, err = writeFileIfChanged(path, content)
	return err
}
