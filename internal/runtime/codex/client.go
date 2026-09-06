// Package codex adapts the pinned Codex app-server JSONL protocol to
// DataGround's normalized runtime contract.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/execution"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

const (
	maxFrameBytes        = 8 << 20
	maxInlineTextBytes   = 64 << 10
	inboundLimit         = 256
	eventLimit           = 256
	protocolWriteTimeout = 5 * time.Second
)

type wireError struct {
	Code int `json:"code"`
}

type wireMessage struct {
	JSONRPC json.RawMessage `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

type approval struct {
	requestID json.RawMessage
	method    string
	resolving bool
	nativeKey string
}

// Client owns one app-server transport and supports one active turn. A worker
// creates a new client for each RuntimeSession returned by ExecutionProvider.
type Client struct {
	session           execution.RuntimeSession
	input             io.WriteCloser
	openShellProvider bool

	writeMu sync.Mutex
	stateMu sync.Mutex
	nextID  uint64
	pending map[uint64]chan wireMessage

	started            bool
	threadID           string
	turnID             string
	approvalMode       dgruntime.ApprovalMode
	interactionsClosed bool
	nextSequence       uint64
	nextApproval       uint64
	approvals          map[string]approval
	nativeRequests     map[string]struct{}

	questionMode    dgruntime.QuestionMode
	questionTimeout time.Duration
	nextQuestion    uint64
	questions       map[string]*pendingQuestion

	inbound chan wireMessage
	events  chan dgruntime.Event
	done    chan struct{}
	closed  chan struct{}

	failOnce     sync.Once
	closeOnce    sync.Once
	terminalOnce sync.Once
	errMu        sync.Mutex
	err          error
	terminalDone chan struct{}
	terminalErr  error
}

func New(session execution.RuntimeSession) (*Client, error) {
	return newClient(session, false)
}

func newClient(session execution.RuntimeSession, openShellProvider bool) (*Client, error) {
	if session == nil {
		return nil, errors.New("runtime session streams are required")
	}
	input, output, errorOutput := session.Input(), session.Output(), session.Errors()
	if input == nil || output == nil || errorOutput == nil {
		return nil, errors.New("runtime session streams are required")
	}
	client := &Client{
		questions:         make(map[string]*pendingQuestion),
		session:           session,
		input:             input,
		openShellProvider: openShellProvider,
		pending:           make(map[uint64]chan wireMessage),
		approvals:         make(map[string]approval),
		nativeRequests:    make(map[string]struct{}),
		inbound:           make(chan wireMessage, inboundLimit),
		events:            make(chan dgruntime.Event, eventLimit),
		done:              make(chan struct{}),
		closed:            make(chan struct{}),
		terminalDone:      make(chan struct{}),
	}
	go client.readLoop(output)
	go client.inboundLoop()
	go func() {
		_, _ = io.Copy(io.Discard, errorOutput)
	}()
	go client.waitProcess()
	return client, nil
}

func (client *Client) Start(ctx context.Context, request dgruntime.StartRequest) (dgruntime.Turn, error) {
	if request.Prompt == "" {
		return nil, errors.New("runtime prompt is required")
	}
	if request.QuestionMode == "" {
		request.QuestionMode = dgruntime.QuestionDisabled
	}
	if (request.QuestionMode != dgruntime.QuestionDisabled && request.QuestionMode != dgruntime.QuestionInteractive) ||
		(request.QuestionMode == dgruntime.QuestionDisabled && request.QuestionTimeout != 0) ||
		(request.QuestionMode == dgruntime.QuestionInteractive && (request.QuestionTimeout <= 0 || request.QuestionTimeout > 15*time.Minute)) {
		return nil, dgruntime.ErrQuestionMode
	}
	if request.ApprovalMode == "" {
		request.ApprovalMode = dgruntime.ApprovalLocked
	}
	if request.ApprovalMode != dgruntime.ApprovalLocked && request.ApprovalMode != dgruntime.ApprovalInteractive {
		return nil, dgruntime.ErrApprovalMode
	}
	if request.SandboxMode == "" {
		request.SandboxMode = dgruntime.SandboxReadOnly
	}
	if request.SandboxMode != dgruntime.SandboxReadOnly && request.SandboxMode != dgruntime.SandboxWorkspaceWrite {
		return nil, dgruntime.ErrSandboxMode
	}
	if request.WorkingDir != "" && (!path.IsAbs(request.WorkingDir) || path.Clean(request.WorkingDir) != request.WorkingDir || strings.ContainsRune(request.WorkingDir, '\x00')) {
		return nil, errors.New("runtime working directory must be a clean absolute sandbox path")
	}

	client.stateMu.Lock()
	if client.started {
		client.stateMu.Unlock()
		return nil, dgruntime.ErrConcurrentTurn
	}
	client.started = true
	client.approvalMode = request.ApprovalMode
	client.questionMode = request.QuestionMode
	client.questionTimeout = request.QuestionTimeout
	client.stateMu.Unlock()

	var initialized struct{}
	if err := client.request(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "dataground", "title": "DataGround", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": request.QuestionMode == dgruntime.QuestionInteractive},
	}, &initialized); err != nil {
		return nil, err
	}
	if err := client.notify(ctx, "initialized", nil); err != nil {
		return nil, err
	}

	threadParams := map[string]any{
		"ephemeral":         true,
		"approvalsReviewer": "user",
		"approvalPolicy":    approvalPolicy(request.ApprovalMode),
		"sandbox":           request.SandboxMode,
	}
	if client.openShellProvider {
		threadParams["modelProvider"] = openShellModelProvider
		threadParams["config"] = openShellProviderConfig()
	}
	if request.WorkingDir != "" {
		threadParams["cwd"] = request.WorkingDir
	}
	if request.Model != "" {
		threadParams["model"] = request.Model
	}
	var threadResponse struct {
		ModelProvider string `json:"modelProvider"`
		Thread        struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := client.request(ctx, "thread/start", threadParams, &threadResponse); err != nil {
		return nil, err
	}
	if threadResponse.Thread.ID == "" {
		return nil, client.protocolFailure("thread/start response is missing its identifier")
	}
	if client.openShellProvider && threadResponse.ModelProvider != openShellModelProvider {
		return nil, client.protocolFailure("thread/start did not select the required provider")
	}
	client.stateMu.Lock()
	client.threadID = threadResponse.Thread.ID
	client.stateMu.Unlock()

	turnParams := map[string]any{
		"threadId": threadResponse.Thread.ID,
		"input":    []map[string]any{{"type": "text", "text": request.Prompt}},
	}
	if request.OutputSchema != nil {
		turnParams["outputSchema"] = request.OutputSchema
	}
	var turnResponse struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := client.request(ctx, "turn/start", turnParams, &turnResponse); err != nil {
		return nil, err
	}
	if turnResponse.Turn.ID == "" {
		return nil, client.protocolFailure("turn/start response is missing its identifier")
	}
	client.stateMu.Lock()
	if client.turnID != "" && client.turnID != turnResponse.Turn.ID {
		client.stateMu.Unlock()
		return nil, client.protocolFailure("turn/start response conflicts with the active turn")
	}
	client.turnID = turnResponse.Turn.ID
	client.stateMu.Unlock()
	return client, nil
}

func (client *Client) Events() <-chan dgruntime.Event { return client.events }

func (client *Client) Interrupt(ctx context.Context) error {
	client.stateMu.Lock()
	threadID, turnID := client.threadID, client.turnID
	client.closeInteractionsLocked()
	client.stateMu.Unlock()
	if threadID == "" || turnID == "" {
		return dgruntime.ErrClosed
	}
	var response struct{}
	return client.request(ctx, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID}, &response)
}

func (client *Client) ApprovalPending(ctx context.Context, approvalID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	pending, exists := client.approvals[approvalID]
	return exists && !client.interactionsClosed && !pending.resolving, nil
}

func (client *Client) ResolveApproval(ctx context.Context, approvalID string, decision dgruntime.ApprovalDecision) error {
	if decision != dgruntime.ApprovalApprove && decision != dgruntime.ApprovalDeny {
		return dgruntime.ErrApprovalDecision
	}
	client.stateMu.Lock()
	pending, ok := client.approvals[approvalID]
	if client.interactionsClosed || (ok && pending.resolving) {
		ok = false
	}
	if ok {
		pending.resolving = true
		client.approvals[approvalID] = pending
	}
	client.stateMu.Unlock()
	if !ok {
		return dgruntime.ErrApprovalNotFound
	}

	nativeDecision := "decline"
	if decision == dgruntime.ApprovalApprove {
		nativeDecision = "accept"
	}
	response := map[string]any{"decision": nativeDecision}
	if pending.method != "item/commandExecution/requestApproval" && pending.method != "item/fileChange/requestApproval" {
		return dgruntime.ErrApprovalNotFound
	}
	// Recheck after acquiring the protocol writer: a queued decision must not
	// outlive a terminal notification or an explicit interruption request.
	guard := func() error {
		client.stateMu.Lock()
		defer client.stateMu.Unlock()
		current, exists := client.approvals[approvalID]
		if client.interactionsClosed || !exists || !current.resolving || current.nativeKey != pending.nativeKey {
			return dgruntime.ErrApprovalNotFound
		}
		return nil
	}
	if err := client.writeGuarded(ctx, map[string]any{"id": pending.requestID, "result": response}, guard); err != nil {
		client.stateMu.Lock()
		if current, exists := client.approvals[approvalID]; exists && !client.interactionsClosed && current.nativeKey == pending.nativeKey {
			current.resolving = false
			client.approvals[approvalID] = current
		}
		client.stateMu.Unlock()
		return err
	}
	client.stateMu.Lock()
	delete(client.approvals, approvalID)
	delete(client.nativeRequests, pending.nativeKey)
	client.stateMu.Unlock()
	return nil
}

func (client *Client) Wait(ctx context.Context) error {
	select {
	case <-client.terminalDone:
		client.stateMu.Lock()
		err := client.terminalErr
		client.stateMu.Unlock()
		return err
	default:
	}
	select {
	case <-client.terminalDone:
		client.stateMu.Lock()
		err := client.terminalErr
		client.stateMu.Unlock()
		return err
	case <-client.done:
		return client.failure()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (client *Client) Close() error {
	client.closeOnce.Do(func() {
		close(client.closed)
		client.fail(dgruntime.ErrClosed)
	})
	return nil
}

func approvalPolicy(mode dgruntime.ApprovalMode) any {
	if mode == dgruntime.ApprovalLocked {
		return "never"
	}
	return map[string]any{"granular": map[string]bool{
		"mcp_elicitations":    true,
		"request_permissions": true,
		"rules":               true,
		"sandbox_approval":    true,
		"skill_approval":      true,
	}}
}

func (client *Client) request(ctx context.Context, method string, params any, target any) error {
	client.stateMu.Lock()
	client.nextID++
	id := client.nextID
	response := make(chan wireMessage, 1)
	client.pending[id] = response
	client.stateMu.Unlock()

	if err := client.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case message := <-response:
		if message.Error != nil {
			return fmt.Errorf("codex app-server rejected %s with code %d", method, message.Error.Code)
		}
		if target != nil && len(message.Result) > 0 {
			if err := json.Unmarshal(message.Result, target); err != nil {
				return client.protocolFailure("response result is invalid")
			}
		}
		return nil
	case <-client.done:
		return client.failure()
	case <-ctx.Done():
		client.fail(fmt.Errorf("%w: request cancelled", dgruntime.ErrProtocol))
		return ctx.Err()
	}
}

func (client *Client) notify(ctx context.Context, method string, params any) error {
	message := map[string]any{"method": method}
	if params != nil {
		message["params"] = params
	}
	return client.write(ctx, message)
}

func (client *Client) respond(ctx context.Context, id json.RawMessage, result any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if !validRequestID(id) {
		return client.protocolFailure("server request identifier is invalid")
	}
	return client.write(ctx, map[string]any{"id": id, "result": result})
}

func (client *Client) write(ctx context.Context, message any) error {
	return client.writeGuarded(ctx, message, nil)
}

func (client *Client) writeGuarded(ctx context.Context, message any, guard func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(encoded) > maxFrameBytes {
		return client.protocolFailure("outbound frame exceeds the size limit")
	}
	encoded = append(encoded, '\n')
	type writeResult struct {
		err      error
		rejected bool
	}
	result := make(chan writeResult, 1)
	go func() {
		client.writeMu.Lock()
		defer client.writeMu.Unlock()
		if err := ctx.Err(); err != nil {
			result <- writeResult{err: err, rejected: true}
			return
		}
		select {
		case <-client.done:
			result <- writeResult{err: client.failure()}
			return
		default:
		}
		if guard != nil {
			if err := guard(); err != nil {
				result <- writeResult{err: err, rejected: true}
				return
			}
		}
		if err := ctx.Err(); err != nil {
			result <- writeResult{err: err, rejected: true}
			return
		}
		_, err := client.input.Write(encoded)
		result <- writeResult{err: err}
	}()
	select {
	case completed := <-result:
		if completed.rejected {
			return completed.err
		}
		if completed.err != nil {
			client.fail(fmt.Errorf("%w: write failed", dgruntime.ErrProtocol))
			return client.failure()
		}
		return nil
	case <-client.done:
		return client.failure()
	case <-ctx.Done():
		client.fail(fmt.Errorf("%w: write cancelled", dgruntime.ErrProtocol))
		return ctx.Err()
	}
}

func (client *Client) readLoop(output io.Reader) {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64<<10), maxFrameBytes)
	for scanner.Scan() {
		var message wireMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			client.fail(fmt.Errorf("%w: invalid JSONL frame", dgruntime.ErrProtocol))
			return
		}
		if len(message.JSONRPC) > 0 {
			client.fail(fmt.Errorf("%w: jsonrpc field is not part of the pinned protocol", dgruntime.ErrProtocol))
			return
		}
		if message.Method != "" {
			if len(message.Result) > 0 || message.Error != nil {
				client.fail(fmt.Errorf("%w: method frame contains response fields", dgruntime.ErrProtocol))
				return
			}
			select {
			case client.inbound <- message:
			case <-client.done:
				return
			default:
				client.fail(fmt.Errorf("%w: inbound buffer exhausted", dgruntime.ErrProtocol))
				return
			}
			continue
		}
		id, ok := numericID(message.ID)
		if !ok {
			client.fail(fmt.Errorf("%w: response identifier is invalid", dgruntime.ErrProtocol))
			return
		}
		if (len(message.Result) > 0) == (message.Error != nil) {
			client.fail(fmt.Errorf("%w: response must contain exactly one result or error", dgruntime.ErrProtocol))
			return
		}
		client.stateMu.Lock()
		response, found := client.pending[id]
		if found {
			delete(client.pending, id)
		}
		client.stateMu.Unlock()
		if !found {
			client.fail(fmt.Errorf("%w: response identifier is unknown", dgruntime.ErrProtocol))
			return
		}
		response <- message
	}
	if err := scanner.Err(); err != nil {
		client.fail(fmt.Errorf("%w: app-server output failed", dgruntime.ErrProtocol))
		return
	}
	select {
	case <-client.closed:
	default:
		client.fail(fmt.Errorf("%w: app-server output closed", dgruntime.ErrProtocol))
	}
}

func (client *Client) inboundLoop() {
	for {
		select {
		case message := <-client.inbound:
			if len(message.ID) > 0 {
				client.handleServerRequest(message)
			} else {
				client.handleNotification(message)
			}
		case <-client.done:
			return
		case <-client.closed:
			return
		}
	}
}

func (client *Client) handleServerRequest(message wireMessage) {
	if !validRequestID(message.ID) {
		client.fail(fmt.Errorf("%w: server request identifier is invalid", dgruntime.ErrProtocol))
		return
	}
	requestKey, _ := nativeRequestKey(message.ID)
	client.stateMu.Lock()
	_, duplicate := client.nativeRequests[requestKey]
	client.stateMu.Unlock()
	if duplicate {
		if err := client.respondError(message.ID, -32600, "duplicate server request"); err != nil {
			client.fail(err)
		}
		return
	}
	if message.Method == "item/tool/requestUserInput" {
		client.handleQuestionRequest(message)
		return
	}
	action := ""
	switch message.Method {
	case "item/commandExecution/requestApproval":
		action = "process.execute"
	case "item/fileChange/requestApproval":
		action = "workspace.change"
	default:
		if err := client.respondError(message.ID, -32601, "unsupported server request"); err != nil {
			client.fail(err)
		}
		return
	}
	client.stateMu.Lock()
	locked := client.approvalMode == dgruntime.ApprovalLocked
	closed := client.interactionsClosed
	client.stateMu.Unlock()
	if closed {
		if err := client.respondError(message.ID, -32600, "approval turn is no longer active"); err != nil {
			client.fail(err)
		}
		return
	}
	if locked {
		if err := client.respond(context.Background(), message.ID, map[string]any{"decision": "decline"}); err != nil {
			client.fail(err)
		}
		return
	}
	var scope struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	if err := json.Unmarshal(message.Params, &scope); err != nil || !client.matchesActiveTurn(scope.ThreadID, scope.TurnID) {
		if err := client.respondError(message.ID, -32602, "invalid approval scope"); err != nil {
			client.fail(err)
		}
		return
	}
	nativeKey, _ := nativeRequestKey(message.ID)
	client.stateMu.Lock()
	if _, duplicate := client.nativeRequests[nativeKey]; duplicate || client.interactionsClosed {
		client.stateMu.Unlock()
		if err := client.respondError(message.ID, -32600, "duplicate approval request"); err != nil {
			client.fail(err)
		}
		return
	}
	client.nextApproval++
	approvalID := fmt.Sprintf("approval-%d", client.nextApproval)
	client.nativeRequests[nativeKey] = struct{}{}
	client.approvals[approvalID] = approval{
		requestID: append(json.RawMessage(nil), message.ID...), method: message.Method, nativeKey: nativeKey,
	}
	client.stateMu.Unlock()
	client.emit("interaction.approval.requested", map[string]any{"approvalId": approvalID, "action": action})
}

func (client *Client) handleNotification(message wireMessage) {
	switch message.Method {
	case "serverRequest/resolved":
		client.handleResolvedRequest(message)
	case "turn/started":
		var params struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(message.Params, &params) != nil || params.ThreadID == "" || params.Turn.ID == "" {
			client.fail(fmt.Errorf("%w: turn/started payload is invalid", dgruntime.ErrProtocol))
			return
		}
		client.stateMu.Lock()
		if client.threadID != params.ThreadID || (client.turnID != "" && client.turnID != params.Turn.ID) {
			client.stateMu.Unlock()
			client.fail(fmt.Errorf("%w: turn/started scope does not match", dgruntime.ErrProtocol))
			return
		}
		client.turnID = params.Turn.ID
		client.stateMu.Unlock()
		client.emit("lifecycle.started", map[string]any{"message": "Runtime turn started."})
	case "item/agentMessage/delta":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Delta    string `json:"delta"`
		}
		if json.Unmarshal(message.Params, &params) != nil || !client.matchesActiveTurn(params.ThreadID, params.TurnID) {
			client.fail(fmt.Errorf("%w: agent message scope does not match", dgruntime.ErrProtocol))
			return
		}
		if len(params.Delta) > maxInlineTextBytes {
			client.fail(fmt.Errorf("%w: agent message delta exceeds the inline limit", dgruntime.ErrProtocol))
			return
		}
		client.emit("output.text.delta", map[string]any{"text": params.Delta})
	case "item/started", "item/completed":
		client.handleItemLifecycle(message)
	case "turn/completed":
		client.handleTurnCompleted(message)
	case "error":
		client.emit("error.occurred", map[string]any{"code": "RUNTIME_UPSTREAM_ERROR", "message": "The runtime reported an error.", "retryable": false})
	default:
		// Unknown notifications are optional capability drift. The pinned
		// conformance suite decides which methods become normalized events.
	}
}

func (client *Client) handleItemLifecycle(message wireMessage) {
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.Item.Type == "" || !client.matchesActiveTurn(params.ThreadID, params.TurnID) {
		client.fail(fmt.Errorf("%w: item lifecycle scope does not match", dgruntime.ErrProtocol))
		return
	}
	eventPrefix, normalizedKind := normalizeItemType(params.Item.Type)
	if eventPrefix == "" {
		return
	}
	phase := "started"
	if message.Method == "item/completed" {
		phase = "completed"
	}
	client.emit(eventPrefix+"."+phase, map[string]any{"kind": normalizedKind})
}

func (client *Client) handleTurnCompleted(message wireMessage) {
	var params struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(message.Params, &params) != nil || !client.matchesActiveTurn(params.ThreadID, params.Turn.ID) {
		client.fail(fmt.Errorf("%w: turn completion scope does not match", dgruntime.ErrProtocol))
		return
	}
	if params.Turn.Status != "completed" && params.Turn.Status != "interrupted" && params.Turn.Status != "failed" {
		client.fail(fmt.Errorf("%w: terminal turn status is invalid", dgruntime.ErrProtocol))
		return
	}
	client.stateMu.Lock()
	client.closeInteractionsLocked()
	client.stateMu.Unlock()
	var terminalErr error
	switch params.Turn.Status {
	case "completed":
		client.emit("lifecycle.succeeded", map[string]any{"message": "Runtime turn completed."})
	case "interrupted":
		client.emit("lifecycle.cancelled", map[string]any{"reason": "runtime interruption"})
	case "failed":
		terminalErr = dgruntime.ErrTurnFailed
		client.emit("lifecycle.failed", map[string]any{"code": "RUNTIME_TURN_FAILED", "retryable": false})
	}
	client.terminalOnce.Do(func() {
		client.stateMu.Lock()
		client.terminalErr = terminalErr
		client.stateMu.Unlock()
		close(client.terminalDone)
	})
}

func normalizeItemType(native string) (string, string) {
	switch native {
	case "commandExecution":
		return "activity.process", "command"
	case "fileChange":
		return "activity.file", "change"
	case "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "webSearch":
		return "activity.tool", "tool"
	case "plan":
		return "activity.plan", "plan"
	case "reasoning":
		return "activity.reasoning", "reasoning"
	case "hookPrompt", "imageView", "imageGeneration", "enteredReviewMode", "exitedReviewMode", "contextCompaction":
		return "activity.runtime", "runtime"
	case "userMessage", "agentMessage":
		return "", ""
	default:
		return "", ""
	}
}

func (client *Client) matchesActiveTurn(threadID, turnID string) bool {
	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	return threadID != "" && turnID != "" && threadID == client.threadID && turnID == client.turnID
}

func (client *Client) emit(eventType string, payload map[string]any) {
	client.stateMu.Lock()
	client.nextSequence++
	event := dgruntime.Event{Sequence: client.nextSequence, Type: eventType, Payload: payload}
	client.stateMu.Unlock()
	select {
	case client.events <- event:
	case <-client.done:
	case <-client.closed:
	}
}

func (client *Client) respondError(id json.RawMessage, code int, message string) error {
	if !validRequestID(id) {
		return client.protocolFailure("server request identifier is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocolWriteTimeout)
	defer cancel()
	return client.write(ctx, map[string]any{"id": id, "error": map[string]any{"code": code, "message": message}})
}

func (client *Client) waitProcess() {
	err := client.session.Wait()
	select {
	case <-client.closed:
		return
	default:
	}
	if err == nil {
		err = errors.New("app-server exited unexpectedly")
	}
	client.fail(fmt.Errorf("%w: %v", dgruntime.ErrProtocol, err))
}

func (client *Client) protocolFailure(message string) error {
	err := fmt.Errorf("%w: %s", dgruntime.ErrProtocol, message)
	client.fail(err)
	return err
}

func (client *Client) closeInteractionsLocked() {
	client.interactionsClosed = true
	clear(client.approvals)
	clear(client.nativeRequests)
	for id, pending := range client.questions {
		if pending.timer != nil {
			pending.timer.Stop()
		}
		delete(client.questions, id)
	}
}

func (client *Client) fail(err error) {
	client.failOnce.Do(func() {
		client.stateMu.Lock()
		client.closeInteractionsLocked()
		client.stateMu.Unlock()
		client.errMu.Lock()
		client.err = err
		client.errMu.Unlock()
		close(client.done)
		_ = client.input.Close()
		_ = client.session.Close()
	})
}

func (client *Client) failure() error {
	client.errMu.Lock()
	defer client.errMu.Unlock()
	if client.err != nil {
		return client.err
	}
	return dgruntime.ErrClosed
}

func numericID(raw json.RawMessage) (uint64, bool) {
	var id uint64
	if len(raw) == 0 || json.Unmarshal(raw, &id) != nil || id == 0 {
		return 0, false
	}
	return id, true
}

func validRequestID(raw json.RawMessage) bool {
	_, ok := nativeRequestKey(raw)
	return ok
}

// Request identity follows the pinned string-or-int64 contract, not raw JSON
// spelling. Escaped strings and signed zero must not alias separate handles.
func nativeRequestKey(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	var text string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &text) == nil {
		return "string:" + text, true
	}
	var number int64
	if len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &number) == nil {
		return "integer:" + strconv.FormatInt(number, 10), true
	}
	return "", false
}

func (client *Client) handleResolvedRequest(message wireMessage) {
	var params struct {
		ThreadID  string          `json:"threadId"`
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		client.protocolFailure("resolved request payload is invalid")
		return
	}
	key, valid := nativeRequestKey(params.RequestID)
	client.stateMu.Lock()
	if !valid || params.ThreadID == "" || params.ThreadID != client.threadID {
		client.stateMu.Unlock()
		client.protocolFailure("resolved request scope is invalid")
		return
	}
	for id, pending := range client.approvals {
		if pending.nativeKey == key {
			delete(client.approvals, id)
		}
	}
	for id, pending := range client.questions {
		if pending.nativeKey == key {
			if pending.timer != nil {
				pending.timer.Stop()
			}
			delete(client.questions, id)
		}
	}
	// A confirmation can arrive after our response already removed its handle.
	delete(client.nativeRequests, key)
	client.stateMu.Unlock()
}
