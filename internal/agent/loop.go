package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/voujr/voujr/internal/ai"
)

// runLoop is the bounded reason→act→observe loop for a single user turn.
func (a *Agent) runLoop(ctx context.Context, userMsg string, emit Emit) (string, error) {
	a.assemble(ctx, userMsg)

	var finalText strings.Builder

	for step := 0; step < a.maxSteps; step++ {
		select {
		case <-ctx.Done():
			return finalText.String(), ctx.Err()
		default:
		}

		// 1. classify + route
		inLoop := step > 0
		cls := ai.Classify(userMsg, estTokens(a.history), inLoop, a.budget, a.spent)
		dec := a.router.Route(cls)
		emit(Event{Kind: EventRouting, Text: fmt.Sprintf("%s — %s", dec.Routing.Model, dec.Routing.Reason)})

		req := ai.Request{
			Messages:    a.history,
			Tools:       a.buildToolSpecs(),
			Temperature: 0.2,
			MaxTokens:   dec.MaxTokens,
			Routing:     dec.Routing,
			Metadata:    map[string]string{"cluster": a.sp.Cluster, "mode": a.sp.Mode},
		}

		// 2. reason (streamed)
		assistant, usage, err := a.streamTurn(ctx, req, emit, &finalText)
		if err != nil {
			emit(Event{Kind: EventError, Err: err})
			return finalText.String(), err
		}
		a.spent += usage.CostCents
		a.history = append(a.history, assistant)

		// 3. no tool calls → the turn is complete
		if len(assistant.ToolCalls) == 0 {
			emit(Event{Kind: EventDone})
			return finalText.String(), nil
		}

		// 4. act: dispatch each requested tool, observe results
		for _, tc := range assistant.ToolCalls {
			emit(Event{Kind: EventToolStart, Tool: tc.Name})
			obs := a.dispatch(ctx, tc)
			emit(Event{Kind: EventToolDone, Tool: tc.Name, Text: obs.summary, Err: obs.err})
			a.history = append(a.history, ai.Message{
				Role:       ai.RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    obs.view,
			})
		}
		// 5. loop: feed observations back to the model for the next reasoning step
	}

	// Step budget exhausted: ask the model for a wrap-up rather than looping.
	emit(Event{Kind: EventError, Err: errors.New("step budget exhausted")})
	return finalText.String(), errStepBudget
}

var errStepBudget = errors.New("agent: step budget exhausted")

type observation struct {
	view    string // fed back to the model
	summary string // shown in the UI
	err     error
}

// dispatch runs one tool call through the registry's safety chain and shapes the
// observation. Errors are returned as observations (not fatal) so the model can
// adapt — e.g. retry with corrected args or choose a different tool.
func (a *Agent) dispatch(ctx context.Context, tc ai.ToolCall) observation {
	res, err := a.registry.Dispatch(ctx, a.sp, tc.Name, tc.Args)
	if err != nil {
		return observation{
			view:    fmt.Sprintf("tool %s failed: %v", tc.Name, err),
			summary: fmt.Sprintf("%s: %v", tc.Name, err),
			err:     err,
		}
	}
	return observation{
		view:    summarizeToolResult(res, 6000),
		summary: res.Summary,
	}
}

// streamTurn consumes a provider stream, emitting token events and assembling the
// assistant message (text + any tool calls).
func (a *Agent) streamTurn(ctx context.Context, req ai.Request, emit Emit, final *strings.Builder) (ai.Message, ai.Usage, error) {
	stream, err := a.provider.Stream(ctx, req)
	if err != nil {
		return ai.Message{}, ai.Usage{}, err
	}
	defer stream.Close()

	msg := ai.Message{Role: ai.RoleAssistant}
	var usage ai.Usage
	for {
		d, err := stream.Recv()
		if err != nil {
			// io.EOF-style end is signaled via Done; a real error aborts.
			if d.Done {
				break
			}
			return msg, usage, err
		}
		switch {
		case d.Text != "":
			msg.Content += d.Text
			final.WriteString(d.Text)
			emit(Event{Kind: EventToken, Text: d.Text})
		case d.ToolCall != nil:
			msg.ToolCalls = append(msg.ToolCalls, *d.ToolCall)
		}
		if d.Usage != nil {
			usage = *d.Usage
		}
		if d.Done {
			break
		}
	}
	return msg, usage, nil
}
