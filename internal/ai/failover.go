package ai

import (
	"context"
	"errors"
	"fmt"
)

// Failover wraps an ordered list of providers and tries the next one when a call
// fails, giving a Go-level break-glass path that complements the gateway's own
// fallback. The first provider is "primary" (e.g. the Portkey gateway); a direct
// SDK adapter can be a secondary so the agent still works if the gateway is down.
//
// Note: streaming can only fail over before the first token — once a stream is
// established mid-response there is no safe way to switch providers.
type Failover struct {
	providers []Provider
}

// NewFailover builds a failover chain. At least one provider is required.
func NewFailover(providers ...Provider) *Failover {
	return &Failover{providers: providers}
}

func (f *Failover) Name() string { return "failover" }

func (f *Failover) Models() []ModelInfo {
	if len(f.providers) > 0 {
		return f.providers[0].Models()
	}
	return nil
}

func (f *Failover) Chat(ctx context.Context, req Request) (Response, error) {
	var lastErr error
	for _, p := range f.providers {
		resp, err := p.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil { // caller cancelled — don't keep trying
			return Response{}, ctx.Err()
		}
	}
	return Response{}, wrapAllFailed(lastErr)
}

func (f *Failover) Stream(ctx context.Context, req Request) (Stream, error) {
	var lastErr error
	for _, p := range f.providers {
		s, err := p.Stream(ctx, req)
		if err == nil {
			return s, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, wrapAllFailed(lastErr)
}

func (f *Failover) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	var lastErr error
	for _, p := range f.providers {
		v, err := p.Embed(ctx, inputs)
		if err == nil {
			return v, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, wrapAllFailed(lastErr)
}

func wrapAllFailed(last error) error {
	if last == nil {
		last = errors.New("no providers configured")
	}
	return fmt.Errorf("all providers failed: %w", last)
}
