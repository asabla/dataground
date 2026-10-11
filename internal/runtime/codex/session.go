package codex

import (
	"context"
	"fmt"
	"sync"

	"github.com/asabla/dataground/internal/execution"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

const maximumSessionTurns = 1024
const maximumSessionRequests = 16384

// Session owns one native thread and its transport. Each returned turn owns its
// event queue, terminal result and interaction handles for its entire lifetime.
// It does not supply durable platform ownership, source materialization or resume.
type Session struct {
	mu        sync.Mutex
	transport *connection
	current   *Client
	options   dgruntime.StartRequest
	turns     int
}

var _ dgruntime.SessionAdapter = (*Session)(nil)

func NewSession(transport execution.RuntimeSession) (*Session, error) {
	client, err := newClient(transport, false, true)
	if err != nil {
		return nil, err
	}
	return &Session{transport: client.connection}, nil
}

func (session *Session) Close() error { return session.transport.Close() }

func (session *Session) StartTurn(ctx context.Context, request dgruntime.StartRequest) (dgruntime.Turn, error) {
	request, err := normalizeStartRequest(request)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !session.mu.TryLock() {
		return nil, dgruntime.ErrConcurrentTurn
	}
	defer session.mu.Unlock()
	transport := session.transport
	select {
	case <-transport.done:
		return nil, transport.failure()
	default:
	}
	if session.turns >= maximumSessionTurns {
		return nil, dgruntime.ErrSessionLimit
	}
	if session.current != nil {
		if !sameSessionOptions(session.options, request) {
			return nil, dgruntime.ErrSessionConfiguration
		}
		select {
		case <-session.current.terminalDone:
		default:
			return nil, dgruntime.ErrConcurrentTurn
		}
		session.current.stateMu.Lock()
		inFlight := session.current.inFlightCommands
		session.current.stateMu.Unlock()
		if inFlight != 0 {
			return nil, dgruntime.ErrConcurrentTurn
		}
		if len(session.current.events) != 0 {
			return nil, dgruntime.ErrTurnEventsPending
		}
	}
	if session.turns == 0 {
		session.options = request
		// Retain configuration only, never prompt, schema or artifact content.
		session.options.Prompt = ""
		session.options.OutputSchema = nil
		session.options.Artifacts = nil
		session.current = transport.active
		session.turns++
		turn, err := session.current.Start(ctx, request)
		if err != nil {
			transport.fail(err)
		}
		return turn, err
	}
	transport.dispatchMu.Lock()
	previous := session.current
	previous.stateMu.Lock()
	threadID, workingDir, previousTurnID := previous.threadID, previous.workingDir, previous.turnID
	previous.stateMu.Unlock()
	client := newTurnClient(transport)
	client.sequential = true
	client.started = true
	client.threadID, client.workingDir = threadID, workingDir
	client.approvalMode, client.questionMode, client.questionTimeout = request.ApprovalMode, request.QuestionMode, request.QuestionTimeout
	transport.connectionMu.Lock()
	select {
	case <-transport.closed:
		transport.connectionMu.Unlock()
		transport.dispatchMu.Unlock()
		return nil, dgruntime.ErrClosed
	case <-transport.done:
		transport.connectionMu.Unlock()
		transport.dispatchMu.Unlock()
		return nil, transport.failure()
	default:
	}
	transport.retiredTurns[previousTurnID] = struct{}{}
	transport.active = client
	transport.connectionMu.Unlock()
	transport.dispatchMu.Unlock()
	session.current = client
	session.turns++
	turn, err := client.startTurn(ctx, request)
	if err != nil {
		transport.fail(err)
	}
	return turn, err
}

func sameSessionOptions(left, right dgruntime.StartRequest) bool {
	return left.WorkingDir == right.WorkingDir && left.Model == right.Model &&
		left.ApprovalMode == right.ApprovalMode && left.SandboxMode == right.SandboxMode &&
		left.QuestionMode == right.QuestionMode && left.QuestionTimeout == right.QuestionTimeout
}

func (client *connection) retiredTurn(id string) bool {
	client.connectionMu.Lock()
	defer client.connectionMu.Unlock()
	_, retired := client.retiredTurns[id]
	return retired
}

// Request IDs can be resolved without a turn ID by the native protocol. Never
// permit their reuse to give an old resolution authority over a later request.
func (client *Client) registerSessionRequest(message wireMessage) bool {
	key, valid := nativeRequestKey(message.ID)
	if !valid || len(key) > 263 {
		client.fail(fmt.Errorf("%w: server request identifier is invalid", dgruntime.ErrProtocol))
		return false
	}
	if _, exists := client.seenRequests[key]; exists || len(client.seenRequests) >= maximumSessionRequests {
		client.fail(fmt.Errorf("%w: session request identity is unavailable", dgruntime.ErrProtocol))
		return false
	}
	client.seenRequests[key] = struct{}{}
	return true
}
