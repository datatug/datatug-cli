package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"time"
	"unicode/utf8"
)

const dynamoDocumentBytes = 64 << 10

var errDynamoInput = errors.New("DynamoDB input is invalid; expected one bounded strict JSON object")
var errDynamoReadInput = errors.New("DynamoDB input could not be read")

// Input readers are borrowed. Cancellation never closes shared stdin. A reader
// ignoring cancellation retains one of two global slots until it really ends.
var dynamoInputSlots = make(chan struct{}, 2)

type dynamoReadResult struct {
	data []byte
	err  error
}

func dynamoFresh(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}
func readDynamoDocument(ctx context.Context, reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, errDynamoReadInput
	}
	return readDynamoBytes(ctx, func() ([]byte, error) { return io.ReadAll(io.LimitReader(reader, dynamoDocumentBytes+1)) })
}
func readDynamoBytes(ctx context.Context, read func() ([]byte, error)) ([]byte, error) {
	// Keep both checks: elapsed time must refuse even a full admission queue
	// before cancellation notification, and admission can race caller revocation.
	if err := dynamoFresh(ctx); err != nil {
		return nil, err
	}
	select {
	case dynamoInputSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := dynamoFresh(ctx); err != nil {
		<-dynamoInputSlots
		return nil, err
	}
	result := make(chan dynamoReadResult, 1)
	go func() { data, err := read(); <-dynamoInputSlots; result <- dynamoReadResult{data: data, err: err} }()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case read := <-result:
		if err := dynamoFresh(ctx); err != nil {
			return nil, err
		}
		if read.err != nil {
			return nil, errDynamoReadInput
		}
		if len(read.data) > dynamoDocumentBytes {
			return nil, errDynamoInput
		}
		return read.data, nil
	}
}
func readDynamoFile(ctx context.Context, path string) ([]byte, error) {
	return readDynamoBytes(ctx, func() ([]byte, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return nil, errDynamoReadInput
		}
		return io.ReadAll(io.LimitReader(file, dynamoDocumentBytes+1))
	})
}

// encoding/json replaces invalid UTF-8 and lone UTF-16 surrogate escapes. Keys
// must not silently address U+FFFD instead of the literal supplied identity.
func dynamoUnicode(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	quoted := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		unit, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if unit >= 0xDC00 && unit <= 0xDFFF {
			return false
		}
		if unit < 0xD800 || unit > 0xDBFF {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xDC00 || low > 0xDFFF {
			return false
		}
		i += 6
	}
	return true
}

func decodeDynamoDocument(data []byte, target any) error {
	trimmed := bytes.TrimSpace(data)
	if len(data) > dynamoDocumentBytes || len(trimmed) == 0 || trimmed[0] != '{' || !dynamoUnicode(data) {
		return errDynamoInput
	}
	unique := json.NewDecoder(bytes.NewReader(trimmed))
	unique.UseNumber()
	nodes := 0
	var walk func(int) bool
	walk = func(depth int) bool {
		nodes++
		if depth > 32 || nodes > 100_000 {
			return false
		}
		token, err := unique.Token()
		if err != nil {
			return false
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return true
		}
		if delimiter == '{' {
			seen := map[string]bool{}
			for unique.More() {
				key, err := unique.Token()
				if err != nil {
					return false
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !walk(depth + 1) {
					return false
				}
			}
			end, err := unique.Token()
			return err == nil && end == json.Delim('}')
		}
		// At a value position Token can return only '{' or '[' as a Delim;
		// closing delimiters are consumed above/below, and Token rejects an
		// out-of-place close. Thus the remaining composite value is an array.
		for unique.More() {
			if !walk(depth + 1) {
				return false
			}
		}
		end, err := unique.Token()
		return err == nil && end == json.Delim(']')
	}
	if !walk(0) {
		return errDynamoInput
	}
	if _, err := unique.Token(); err != io.EOF {
		return errDynamoInput
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return errDynamoInput
	}
	return nil
}
