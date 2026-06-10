package ai

import (
	"context"
	"errors"
	"testing"
)

// fakeProvider is a configurable Provider stub for failover tests.
type fakeProvider struct {
	name     string
	chatErr  error
	chatText string
	embedErr error
	embedVec [][]float32
}

func (f fakeProvider) Name() string        { return f.name }
func (f fakeProvider) Models() []ModelInfo { return nil }
func (f fakeProvider) Chat(_ context.Context, _ Request) (Response, error) {
	if f.chatErr != nil {
		return Response{}, f.chatErr
	}
	return Response{Message: Message{Role: RoleAssistant, Content: f.chatText}}, nil
}
func (f fakeProvider) Stream(_ context.Context, _ Request) (Stream, error) {
	if f.chatErr != nil {
		return nil, f.chatErr
	}
	return nil, errors.New("stream not exercised")
}
func (f fakeProvider) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return f.embedVec, f.embedErr
}

func TestFailoverUsesNextOnError(t *testing.T) {
	f := NewFailover(
		fakeProvider{name: "primary", chatErr: errors.New("gateway down")},
		fakeProvider{name: "secondary", chatText: "answer"},
	)
	resp, err := f.Chat(context.Background(), Request{})
	if err != nil {
		t.Fatalf("expected fallback to succeed: %v", err)
	}
	if resp.Message.Content != "answer" {
		t.Fatalf("expected secondary's response, got %q", resp.Message.Content)
	}
}

func TestFailoverAllFail(t *testing.T) {
	f := NewFailover(
		fakeProvider{chatErr: errors.New("a")},
		fakeProvider{chatErr: errors.New("b")},
	)
	if _, err := f.Chat(context.Background(), Request{}); err == nil {
		t.Fatal("expected an error when every provider fails")
	}
}

func TestFailoverEmbedFallback(t *testing.T) {
	f := NewFailover(
		fakeProvider{embedErr: errors.New("no embeddings")},
		fakeProvider{embedVec: [][]float32{{1, 2, 3}}},
	)
	v, err := f.Embed(context.Background(), []string{"hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 || len(v[0]) != 3 {
		t.Fatalf("unexpected embedding fallback result: %v", v)
	}
}
