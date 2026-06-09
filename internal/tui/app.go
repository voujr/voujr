// Package tui is the Bubble Tea terminal UI: a Claude Code-style chat with
// streamed tokens, tool-execution indicators, and a y/N approval modal for
// mutations. It speaks to the agent over events and implements tools.Approver so
// the agent's (synchronous) approval gate can pause for human input.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/voujr/voujr/internal/agent"
	"github.com/voujr/voujr/internal/tools"
)

// Runner is the subset of the agent the UI drives.
type Runner interface {
	Run(ctx context.Context, userMsg string, emit agent.Emit) (string, error)
}

// Model is the root Bubble Tea model.
type Model struct {
	ctx     context.Context
	agent   Runner
	vp      viewport.Model
	input   textinput.Model
	cluster string

	transcript strings.Builder
	streaming  bool
	spinner    string

	// events carries translated agent events from the turn goroutine into the
	// Bubble Tea Update loop (drained by waitForEvent).
	events chan tea.Msg

	// approval bridge: the agent (on its own goroutine) sends a request and
	// blocks on resp until the user answers via the modal.
	approvalReq  chan tools.ApprovalRequest
	approvalResp chan bool
	pending      *tools.ApprovalRequest
}

// New constructs the UI model.
func New(ctx context.Context, a Runner, cluster string) *Model {
	in := textinput.New()
	in.Placeholder = "Ask about your cluster… (e.g. \"why are prod pods restarting?\")"
	in.Focus()

	vp := viewport.New(0, 0)
	m := &Model{
		ctx: ctx, agent: a, input: in, vp: vp, cluster: cluster,
		approvalReq:  make(chan tools.ApprovalRequest),
		approvalResp: make(chan bool),
	}
	m.write(fmt.Sprintf("Connected to %s. Read-only by default — mutations need your approval.\n", cluster))
	return m
}

// SetAgent injects the runner after construction. This breaks the wiring cycle:
// the tool registry needs the UI as its Approver, the agent needs the registry,
// and the UI needs the agent — so the UI is built first (agent nil) and the agent
// is attached once it exists.
func (m *Model) SetAgent(a Runner) { m.agent = a }

func (m *Model) Init() tea.Cmd {
	// Arm a single, long-lived approval listener; it re-arms after each decision.
	return tea.Batch(textinput.Blink, m.waitForApproval())
}

// --- agent → UI messages ---

type tokenMsg string
type toolStartMsg string
type toolDoneMsg struct {
	tool string
	err  error
	text string
}
type routingMsg string
type turnDoneMsg struct{ err error }
type approvalMsg tools.ApprovalRequest

// Update handles input, agent events, and the approval modal.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.vp.Width = msg.Width
		m.vp.Height = msg.Height - 3
		m.input.Width = msg.Width - 4
		return m, nil

	case tea.KeyMsg:
		// Approval modal captures y/n first.
		if m.pending != nil {
			switch strings.ToLower(msg.String()) {
			case "y":
				m.resolveApproval(true)
				return m, m.waitForApproval() // re-arm for the next mutation
			case "n", "esc":
				m.resolveApproval(false)
				return m, m.waitForApproval()
			}
			return m, nil
		}
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEnter:
			if m.streaming || strings.TrimSpace(m.input.Value()) == "" {
				return m, nil
			}
			return m, m.submit()
		}

	case tokenMsg:
		m.write(string(msg))
		return m, m.waitForEvent()
	case routingMsg:
		m.spinner = "▸ " + string(msg)
		return m, m.waitForEvent()
	case toolStartMsg:
		m.write(fmt.Sprintf("\n  ⟳ %s…", string(msg)))
		return m, m.waitForEvent()
	case toolDoneMsg:
		if msg.err != nil {
			m.write(fmt.Sprintf("  ✗ %s: %v\n", msg.tool, msg.err))
		} else {
			m.write(fmt.Sprintf("  ✓ %s — %s\n", msg.tool, msg.text))
		}
		return m, m.waitForEvent()
	case approvalMsg:
		// Approval arrives on its own command channel, not the event stream. The
		// in-flight waitForEvent remains blocked until the agent (unblocked by the
		// user's y/N) emits more events, so we must NOT start a second drainer.
		req := tools.ApprovalRequest(msg)
		m.pending = &req
		return m, nil
	case turnDoneMsg:
		m.streaming = false
		m.spinner = ""
		if msg.err != nil {
			m.write(fmt.Sprintf("\n[error] %v\n", msg.err))
		}
		m.write("\n")
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// Approve implements tools.Approver. It runs on the agent goroutine: it hands a
// request to the UI and blocks until the modal answers.
func (m *Model) Approve(ctx context.Context, req tools.ApprovalRequest) (bool, string, error) {
	select {
	case m.approvalReq <- req:
	case <-ctx.Done():
		return false, "", ctx.Err()
	}
	select {
	case ok := <-m.approvalResp:
		return ok, "operator", nil
	case <-ctx.Done():
		return false, "", ctx.Err()
	}
}

func (m *Model) resolveApproval(ok bool) {
	m.pending = nil
	if ok {
		m.write("  → approved\n")
	} else {
		m.write("  → rejected\n")
	}
	go func() { m.approvalResp <- ok }()
}

// submit kicks off an agent turn on its own goroutine, bridging events back into
// the Bubble Tea loop through a channel drained by waitForEvent.
func (m *Model) submit() tea.Cmd {
	user := m.input.Value()
	m.input.Reset()
	m.write(fmt.Sprintf("\n> %s\n\n", user))
	m.streaming = true

	m.events = make(chan tea.Msg, 64)
	go func() {
		emit := func(e agent.Event) { m.events <- translate(e) }
		_, err := m.agent.Run(m.ctx, user, emit)
		m.events <- turnDoneMsg{err: err}
		close(m.events)
	}()
	// The approval listener is already armed in Init and re-armed on each
	// decision, so we only need to start draining turn events here.
	return m.waitForEvent()
}

func translate(e agent.Event) tea.Msg {
	switch e.Kind {
	case agent.EventToken:
		return tokenMsg(e.Text)
	case agent.EventRouting:
		return routingMsg(e.Text)
	case agent.EventToolStart:
		return toolStartMsg(e.Tool)
	case agent.EventToolDone:
		return toolDoneMsg{tool: e.Tool, err: e.Err, text: e.Text}
	case agent.EventDone, agent.EventError:
		return turnDoneMsg{err: e.Err}
	default:
		return nil
	}
}

func (m *Model) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-m.events
		if !ok {
			return turnDoneMsg{}
		}
		return msg
	}
}

func (m *Model) waitForApproval() tea.Cmd {
	return func() tea.Msg { return approvalMsg(<-m.approvalReq) }
}

func (m *Model) write(s string) {
	m.transcript.WriteString(s)
	m.vp.SetContent(m.transcript.String())
	m.vp.GotoBottom()
}

// View renders the transcript, an optional approval modal, and the input line.
func (m *Model) View() string {
	var b strings.Builder
	b.WriteString(m.vp.View())
	b.WriteString("\n")
	if m.pending != nil {
		b.WriteString(approvalView(*m.pending))
	} else if m.streaming {
		b.WriteString(faint(m.spinner))
	} else {
		b.WriteString(m.input.View())
	}
	return b.String()
}
