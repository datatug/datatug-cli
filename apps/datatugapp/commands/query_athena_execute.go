package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	athenasdk "github.com/dal-go/dalgo2athena"
)

func shutdownAthenaClient(client *athenasdk.Client, closeTransport func()) error {
	var shutdownErr error
	if client != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		shutdownErr = client.Shutdown(cleanup)
		cancel()
	}
	if closeTransport != nil {
		closeTransport()
	}
	if shutdownErr != nil {
		return errAthenaSessionCleanup
	}
	return nil
}

func athenaReadError(ctx context.Context, readErr, cleanupErr error) error {
	if fresh := athenaFresh(ctx); fresh != nil && !errors.Is(readErr, context.Canceled) {
		if errors.Is(fresh, context.Canceled) || !errors.Is(readErr, context.DeadlineExceeded) {
			readErr = fresh
		}
	}
	if readErr == nil && cleanupErr != nil {
		readErr = cleanupErr
	}
	return readErr
}

func executeAthenaRead(ctx context.Context, client *athenasdk.Client, closeTransport func(), request admittedAthenaRead) ([]byte, error) {
	if ctx == nil || client == nil {
		_ = shutdownAthenaClient(client, closeTransport)
		if ctx != nil {
			if fresh := athenaFresh(ctx); fresh != nil {
				return nil, fresh
			}
		}
		return nil, errAthenaSessionInput
	}
	if request.operation == "query" {
		return executeAthenaQuery(ctx, client, closeTransport, request)
	}
	frame, readErr := executeAthenaPoint(ctx, client, request)
	cleanupErr := shutdownAthenaClient(client, closeTransport)
	if readErr = athenaReadError(ctx, readErr, cleanupErr); readErr != nil {
		return nil, readErr
	}
	return frame, nil
}

func executeAthenaPoint(ctx context.Context, client *athenasdk.Client, request admittedAthenaRead) ([]byte, error) {
	used := athenaWireBudget{}
	switch request.operation {
	case "get":
		document, err := client.Get(ctx, request.collection, request.id)
		if err != nil {
			return nil, err
		}
		frame, _, err := athenaGetFrame(ctx, request, document, used)
		return frame, err
	case "exists", "testConnection":
		exists, err := client.Exists(ctx, request.collection, request.id)
		if err != nil {
			return nil, err
		}
		if request.operation == "testConnection" {
			frame, _, err := athenaTestConnectionFrame(ctx, request, exists, used)
			return frame, err
		}
		frame, _, err := athenaExistsFrame(ctx, request, exists, used)
		return frame, err
	case "getMany":
		documents, err := client.GetMany(ctx, request.collection, append([]string{}, request.ids...))
		if err != nil {
			return nil, err
		}
		frame, _, err := athenaGetManyFrame(ctx, request, documents, used)
		return frame, err
	default:
		return nil, errAthenaInput
	}
}

func executeAthenaQuery(ctx context.Context, client *athenasdk.Client, closeTransport func(), request admittedAthenaRead) ([]byte, error) {
	session, readErr := newAthenaPageSession(ctx, client, closeTransport, request)
	if readErr != nil {
		cleanupErr := shutdownAthenaClient(client, closeTransport)
		return nil, athenaReadError(ctx, readErr, cleanupErr)
	}
	var buffer bytes.Buffer
	for {
		readErr = session.Next()
		if errors.Is(readErr, errAthenaSessionComplete) {
			readErr = nil
			break
		}
		if readErr != nil {
			break
		}
		if readErr = session.WritePending(&buffer); readErr != nil {
			break
		}
	}
	cleanupErr := session.Dispose()
	if readErr = athenaReadError(ctx, readErr, cleanupErr); readErr != nil {
		return nil, readErr
	}
	return buffer.Bytes(), nil
}

func finishAthenaOutput(ctx context.Context, output io.Writer, frame []byte) error {
	if ctx == nil {
		return errAthenaSessionInput
	}
	if err := athenaFresh(ctx); err != nil {
		return err
	}
	if output == nil {
		return errAthenaSessionWriter
	}
	n, writeErr := output.Write(frame)
	if writeErr != nil || n != len(frame) {
		if err := athenaFresh(ctx); err != nil {
			return err
		}
		return errAthenaSessionOutput
	}
	return athenaFresh(ctx)
}
