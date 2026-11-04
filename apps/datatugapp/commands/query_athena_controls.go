package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
)

var errAthenaControl = errors.New("athena control input is invalid")

const (
	athenaControlMaxFrameBytes = 4096
	athenaControlMaxBytes      = 64 << 10
	athenaControlMaxFrames     = 128
	athenaControlMaxNexts      = 99
)

type athenaControlBudget struct {
	bytes, frames, nexts int
}

func athenaControlFrame(ctx context.Context, frame []byte, used athenaControlBudget) (operation string, next athenaControlBudget, err error) {
	if ctx == nil {
		return "", used, errAthenaControl
	}
	next = used
	defer func() {
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			operation = ""
			next = used
			err = ctxErr
			return
		}
		if err != nil {
			operation = ""
			next = used
			err = errAthenaControl
		}
	}()
	if err = athenaFresh(ctx); err != nil {
		return "", used, err
	}
	if used.bytes < 0 || used.bytes > athenaControlMaxBytes ||
		used.frames < 0 || used.frames > athenaControlMaxFrames ||
		used.nexts < 0 || used.nexts > athenaControlMaxNexts {
		return "", used, errAthenaControl
	}
	if len(frame) > athenaControlMaxFrameBytes || len(frame) > athenaControlMaxBytes-used.bytes {
		return "", used, errAthenaControl
	}
	if len(frame) == 0 {
		return "", used, nil
	}
	body := frame
	if body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
		if len(body) > 0 && body[len(body)-1] == '\r' {
			body = body[:len(body)-1]
		}
	}
	for _, b := range body {
		if b == '\n' {
			return "", used, errAthenaControl
		}
	}
	blank := true
	for _, b := range body {
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			blank = false
			break
		}
	}
	if blank {
		next.bytes = used.bytes + len(frame)
		return "", next, nil
	}
	if !json.Valid(body) {
		return "", used, errAthenaControl
	}
	if used.frames >= athenaControlMaxFrames {
		return "", used, errAthenaControl
	}
	fields, err := athenaFields(body, "operation")
	if err != nil || len(fields) != 1 {
		return "", used, errAthenaControl
	}
	raw, ok := fields["operation"]
	if !ok {
		return "", used, errAthenaControl
	}
	if operation, err = athenaString(raw); err != nil {
		return "", used, errAthenaControl
	}
	switch operation {
	case "next":
		if used.nexts >= athenaControlMaxNexts {
			return "", used, errAthenaControl
		}
		next.nexts = used.nexts + 1
	case "cancel", "close":
	default:
		return "", used, errAthenaControl
	}
	next.bytes = used.bytes + len(frame)
	next.frames = used.frames + 1
	return operation, next, nil
}

func athenaSharedRequest(ctx context.Context, input *bufio.Reader) (raw []byte, tail *bufio.Reader, err error) {
	if ctx == nil {
		return nil, nil, errAthenaControl
	}
	defer func() {
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			raw = nil
			tail = nil
			err = ctxErr
			return
		}
		if err != nil {
			raw = nil
			tail = nil
			err = errAthenaControl
		}
	}()
	if err = athenaFresh(ctx); err != nil {
		return nil, nil, err
	}
	if input == nil {
		return nil, nil, errAthenaControl
	}
	decoder := json.NewDecoder(&io.LimitedReader{R: input, N: 65537})
	var message json.RawMessage
	if err = decoder.Decode(&message); err != nil {
		return nil, nil, errAthenaControl
	}
	consumed := decoder.InputOffset()
	if consumed < 0 || consumed > athenaControlMaxBytes {
		return nil, nil, errAthenaControl
	}
	var fields map[string]json.RawMessage
	if err = decodeDynamoDocument(message, &fields); err != nil || fields == nil {
		return nil, nil, errAthenaControl
	}
	tail = bufio.NewReader(io.MultiReader(decoder.Buffered(), input))
	for {
		b, readErr := tail.ReadByte()
		if readErr == io.EOF {
			if consumed > athenaControlMaxBytes {
				return nil, nil, errAthenaControl
			}
			return message, tail, nil
		}
		if readErr != nil {
			return nil, nil, errAthenaControl
		}
		consumed++
		if consumed > athenaControlMaxBytes {
			return nil, nil, errAthenaControl
		}
		switch b {
		case ' ', '\t', '\r':
			continue
		case '\n':
			return message, tail, nil
		default:
			return nil, nil, errAthenaControl
		}
	}
}
