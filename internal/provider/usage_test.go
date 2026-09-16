package provider

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestObservedJSONResponseExtractsUsage(t *testing.T) {
	recorder := newUsageRecorder()
	body := observeJSON(io.NopCloser(strings.NewReader(`{"usage":{"prompt_tokens":3,"completion_tokens":2}}`)), recorder)
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	usage := recorder.Snapshot()
	if !usage.Complete || usage.InputTokens != 3 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestObservedJSONResponseStopsBufferingAtLimit(t *testing.T) {
	recorder := newUsageRecorder()
	content := strings.Repeat("x", maxObservedResponseBytes+1)
	body := observeJSON(io.NopCloser(strings.NewReader(content)), recorder)
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatal("observed body changed the response")
	}
	if usage := recorder.Snapshot(); usage.Complete {
		t.Fatalf("oversized usage = %+v", usage)
	}
}

func TestObservedSSEExtractsUsageWithoutChangingBody(t *testing.T) {
	content := "data: {\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":6}}\n\ndata: [DONE]\n\n"
	recorder := newUsageRecorder()
	body := observeOpenAISSE(io.NopCloser(strings.NewReader(content)), recorder)
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("stream = %q", got)
	}
	usage := recorder.Snapshot()
	if !usage.Complete || usage.InputTokens != 4 || usage.OutputTokens != 6 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestObservedSSEReturnsFirstChunkBeforeStreamEnds(t *testing.T) {
	source := &delayedReader{first: []byte("data: first\n\n"), closed: make(chan struct{})}
	body := observeOpenAISSE(source, newUsageRecorder())
	started := time.Now()
	buffer := make([]byte, 32)
	count, err := body.Read(buffer)
	if err != nil || string(buffer[:count]) != "data: first\n\n" || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("first read count=%d err=%v data=%q", count, err, buffer[:count])
	}
	_ = body.Close()
}

type delayedReader struct {
	first  []byte
	closed chan struct{}
}

func (reader *delayedReader) Read(buffer []byte) (int, error) {
	if len(reader.first) > 0 {
		count := copy(buffer, reader.first)
		reader.first = reader.first[count:]
		return count, nil
	}
	<-reader.closed
	return 0, io.ErrClosedPipe
}

func (reader *delayedReader) Close() error {
	select {
	case <-reader.closed:
	default:
		close(reader.closed)
	}
	return nil
}
