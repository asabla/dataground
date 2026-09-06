package codex_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

func TestClientPerformsHandshakeAndNormalizesTurn(t *testing.T) {
	t.Parallel()

	session := newScriptedSession(t, func(server *scriptServer) {
		initialize := server.read()
		server.requireMethod(initialize, "initialize")
		if string(initialize.ID) != "1" || strings.Contains(string(initialize.Raw), "jsonrpc") {
			t.Fatalf("unexpected initialize envelope: %s", initialize.Raw)
		}
		server.respond(initialize.ID, map[string]any{"userAgent": "codex", "codexHome": "/private", "platformFamily": "unix", "platformOs": "linux"})

		initialized := server.read()
		server.requireMethod(initialized, "initialized")
		if len(initialized.ID) != 0 {
			t.Fatalf("initialized must be a notification: %s", initialized.Raw)
		}

		thread := server.read()
		server.requireMethod(thread, "thread/start")
		var threadParams map[string]any
		server.decodeParams(thread, &threadParams)
		if threadParams["ephemeral"] != true || threadParams["approvalsReviewer"] != "user" || threadParams["sandbox"] != "workspace-write" {
			t.Fatalf("thread policy is not deterministic: %#v", threadParams)
		}
		if threadParams["approvalPolicy"] != "on-request" {
			t.Fatalf("interactive approvals are not explicit: %#v", threadParams["approvalPolicy"])
		}
		server.respond(thread.ID, map[string]any{"thread": map[string]any{"id": "native-thread-sensitive"}})

		turn := server.read()
		server.requireMethod(turn, "turn/start")
		var turnParams struct {
			ThreadID string `json:"threadId"`
			Input    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"input"`
		}
		server.decodeParams(turn, &turnParams)
		if turnParams.ThreadID != "native-thread-sensitive" || len(turnParams.Input) != 1 || turnParams.Input[0].Text != "build the result" {
			t.Fatalf("unexpected turn params: %#v", turnParams)
		}
		server.notify("turn/started", map[string]any{"threadId": turnParams.ThreadID, "turn": map[string]any{"id": "native-turn-sensitive", "status": "inProgress", "items": []any{}}})
		server.respond(turn.ID, map[string]any{"turn": map[string]any{"id": "native-turn-sensitive", "status": "inProgress", "items": []any{}}})
		server.notify("item/agentMessage/delta", map[string]any{"threadId": turnParams.ThreadID, "turnId": "native-turn-sensitive", "itemId": "native-item-sensitive", "delta": "done"})
		server.notify("item/started", map[string]any{"threadId": turnParams.ThreadID, "turnId": "native-turn-sensitive", "item": map[string]any{"id": "native-command-sensitive", "type": "commandExecution"}})
		server.notify("item/completed", map[string]any{"threadId": turnParams.ThreadID, "turnId": "native-turn-sensitive", "item": map[string]any{"id": "native-command-sensitive", "type": "commandExecution"}})
		server.notify("turn/completed", map[string]any{"threadId": turnParams.ThreadID, "turn": map[string]any{"id": "native-turn-sensitive", "status": "completed", "items": []any{}}})
	})

	client, err := codex.New(session)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer client.Close()
	turn, err := client.Start(context.Background(), dgruntime.StartRequest{
		Prompt: "build the result", ApprovalMode: dgruntime.ApprovalInteractive, SandboxMode: dgruntime.SandboxWorkspaceWrite,
	})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}

	events := collectEvents(t, turn.Events(), 5)
	wantTypes := []string{"lifecycle.started", "output.text.delta", "activity.process.started", "activity.process.completed", "lifecycle.succeeded"}
	for index, event := range events {
		if event.Sequence != uint64(index+1) || event.Type != wantTypes[index] {
			t.Fatalf("event %d = %#v, want type %s", index, event, wantTypes[index])
		}
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			t.Fatalf("marshal event: %v", marshalErr)
		}
		if strings.Contains(string(encoded), "native-") {
			t.Fatalf("native identifier escaped normalization: %s", encoded)
		}
	}
	if err := turn.Wait(context.Background()); err != nil {
		t.Fatalf("wait for turn: %v", err)
	}
	if err := turn.Wait(context.Background()); err != nil {
		t.Fatalf("repeated wait for turn: %v", err)
	}
	if err := session.scriptError(); err != nil {
		t.Fatal(err)
	}
}

func TestClientRoutesApprovalWithoutAutomaticallyGrantingIt(t *testing.T) {
	t.Parallel()

	approvalResponse := make(chan wireRecord, 1)
	session := newScriptedSession(t, func(server *scriptServer) {
		server.completeStart("native-thread", "native-turn")
		server.request("native-approval-request", "item/commandExecution/requestApproval", map[string]any{
			"threadId": "native-thread", "turnId": "native-turn", "itemId": "native-item", "command": "printenv SECRET",
		})
		approvalResponse <- server.read()
		server.notify("turn/completed", map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "native-turn", "status": "completed", "items": []any{}}})
	})

	client, err := codex.New(session)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer client.Close()
	turn, err := client.Start(context.Background(), dgruntime.StartRequest{Prompt: "inspect", ApprovalMode: dgruntime.ApprovalInteractive})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	event := waitForEvent(t, turn.Events(), "interaction.approval.requested")
	if event.Payload["approvalId"] != "approval-1" || event.Payload["action"] != "process.execute" {
		t.Fatalf("unexpected normalized approval: %#v", event)
	}
	encoded, _ := json.Marshal(event)
	if strings.Contains(string(encoded), "native-") || strings.Contains(string(encoded), "SECRET") {
		t.Fatalf("approval leaked native or sensitive content: %s", encoded)
	}

	select {
	case response := <-approvalResponse:
		t.Fatalf("approval was answered before a platform decision: %s", response.Raw)
	case <-time.After(25 * time.Millisecond):
	}
	if err := turn.ResolveApproval(context.Background(), "approval-1", dgruntime.ApprovalDeny); err != nil {
		t.Fatalf("deny approval: %v", err)
	}
	response := <-approvalResponse
	if string(response.ID) != `"native-approval-request"` || !strings.Contains(string(response.Result), `"decline"`) {
		t.Fatalf("unexpected approval response: %s", response.Raw)
	}
	if err := turn.ResolveApproval(context.Background(), "approval-1", dgruntime.ApprovalDeny); !errors.Is(err, dgruntime.ErrApprovalNotFound) {
		t.Fatalf("duplicate decision did not fail closed: %v", err)
	}
	if err := turn.Wait(context.Background()); err != nil {
		t.Fatalf("wait for turn: %v", err)
	}
}

func TestClientRejectsDuplicateNativeApprovalRequests(t *testing.T) {
	t.Parallel()

	duplicateResponse := make(chan wireRecord, 1)
	decisionResponse := make(chan wireRecord, 1)
	session := newScriptedSession(t, func(server *scriptServer) {
		server.completeStart("thread", "turn")
		params := map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "item"}
		server.request("same-request", "item/fileChange/requestApproval", params)
		server.request("same-request", "item/fileChange/requestApproval", params)
		duplicateResponse <- server.read()
		decisionResponse <- server.read()
		server.notify("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed", "items": []any{}}})
	})

	client, err := codex.New(session)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer client.Close()
	turn, err := client.Start(context.Background(), dgruntime.StartRequest{Prompt: "write", ApprovalMode: dgruntime.ApprovalInteractive})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	event := waitForEvent(t, turn.Events(), "interaction.approval.requested")
	duplicate := <-duplicateResponse
	if duplicate.Error == nil || duplicate.Error.Code != -32600 {
		t.Fatalf("duplicate approval request was not rejected: %s", duplicate.Raw)
	}
	if err := turn.ResolveApproval(context.Background(), event.Payload["approvalId"].(string), dgruntime.ApprovalDeny); err != nil {
		t.Fatalf("deny original approval: %v", err)
	}
	decision := <-decisionResponse
	if !strings.Contains(string(decision.Result), `"decline"`) {
		t.Fatalf("original approval decision was not delivered: %s", decision.Raw)
	}
}

func TestClientInterruptsOnlyTheActiveNativeTurn(t *testing.T) {
	t.Parallel()

	session := newScriptedSession(t, func(server *scriptServer) {
		server.completeStart("native-thread", "native-turn")
		interrupt := server.read()
		server.requireMethod(interrupt, "turn/interrupt")
		var params map[string]string
		server.decodeParams(interrupt, &params)
		if params["threadId"] != "native-thread" || params["turnId"] != "native-turn" {
			t.Fatalf("interrupt targeted the wrong turn: %#v", params)
		}
		server.respond(interrupt.ID, map[string]any{})
		server.notify("turn/completed", map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "native-turn", "status": "interrupted", "items": []any{}}})
	})

	client, err := codex.New(session)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer client.Close()
	turn, err := client.Start(context.Background(), dgruntime.StartRequest{Prompt: "wait"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if err := turn.Interrupt(context.Background()); err != nil {
		t.Fatalf("interrupt turn: %v", err)
	}
	event := waitForEvent(t, turn.Events(), "lifecycle.cancelled")
	if event.Payload["reason"] != "runtime interruption" {
		t.Fatalf("unexpected cancellation: %#v", event)
	}
	if err := turn.Wait(context.Background()); err != nil {
		t.Fatalf("interrupted turn should be terminal without an adapter error: %v", err)
	}
}

func TestClientUsesLockedApprovalPolicy(t *testing.T) {
	t.Parallel()

	session := newScriptedSession(t, func(server *scriptServer) {
		initialize := server.read()
		server.respond(initialize.ID, map[string]any{})
		server.read() // initialized
		thread := server.read()
		var params map[string]any
		server.decodeParams(thread, &params)
		if params["approvalPolicy"] != "never" || params["sandbox"] != "read-only" {
			t.Fatalf("locked mode was weakened: %#v", params["approvalPolicy"])
		}
		server.respond(thread.ID, map[string]any{"thread": map[string]any{"id": "thread"}})
		turn := server.read()
		server.respond(turn.ID, map[string]any{"turn": map[string]any{"id": "turn"}})
		server.request("locked-approval", "item/fileChange/requestApproval", map[string]any{
			"threadId": "thread", "turnId": "turn", "itemId": "item",
		})
		response := server.read()
		if string(response.ID) != `"locked-approval"` || !strings.Contains(string(response.Result), `"decline"`) {
			t.Fatalf("locked mode did not deny approval: %s", response.Raw)
		}
	})

	client, err := codex.New(session)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer client.Close()
	turn, err := client.Start(context.Background(), dgruntime.StartRequest{Prompt: "read", ApprovalMode: dgruntime.ApprovalLocked})
	if err != nil {
		t.Fatalf("start locked turn: %v", err)
	}
	if err := session.scriptError(); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-turn.Events():
		if event.Type == "interaction.approval.requested" {
			t.Fatalf("locked approval became actionable: %#v", event)
		}
	case <-time.After(25 * time.Millisecond):
	}
}

func TestClientFailsClosedOnMalformedOrMisScopedInput(t *testing.T) {
	t.Parallel()

	t.Run("malformed JSON", func(t *testing.T) {
		release := make(chan struct{})
		session := newScriptedSession(t, func(server *scriptServer) {
			server.completeStart("thread", "turn")
			<-release
			server.writeRaw([]byte("{not-json}\n"))
		})
		client, err := codex.New(session)
		if err != nil {
			t.Fatalf("create client: %v", err)
		}
		defer client.Close()
		turn, err := client.Start(context.Background(), dgruntime.StartRequest{Prompt: "read"})
		if err != nil {
			t.Fatalf("start turn: %v", err)
		}
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
			t.Fatalf("malformed input did not fail the adapter: %v", err)
		}
	})

	t.Run("cross-turn approval", func(t *testing.T) {
		response := make(chan wireRecord, 1)
		session := newScriptedSession(t, func(server *scriptServer) {
			server.completeStart("thread", "turn")
			server.request(99, "item/fileChange/requestApproval", map[string]any{"threadId": "other-thread", "turnId": "turn", "itemId": "item"})
			response <- server.read()
		})
		client, err := codex.New(session)
		if err != nil {
			t.Fatalf("create client: %v", err)
		}
		defer client.Close()
		turn, err := client.Start(context.Background(), dgruntime.StartRequest{Prompt: "write", ApprovalMode: dgruntime.ApprovalInteractive})
		if err != nil {
			t.Fatalf("start turn: %v", err)
		}
		reply := <-response
		if reply.Error == nil || reply.Error.Code != -32602 {
			t.Fatalf("mis-scoped approval was not rejected: %s", reply.Raw)
		}
		select {
		case event := <-turn.Events():
			if event.Type == "interaction.approval.requested" {
				t.Fatalf("mis-scoped approval became actionable: %#v", event)
			}
		case <-time.After(25 * time.Millisecond):
		}
	})
}

func TestClientCancellationClosesABlockedProtocolWrite(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	session := newScriptedSession(t, func(_ *scriptServer) { <-release })
	client, err := codex.New(session)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer close(release)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "blocked"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked write did not honor cancellation: %v", err)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := client.Wait(waitCtx); !errors.Is(err, dgruntime.ErrProtocol) {
		t.Fatalf("ambiguous blocked write did not close the adapter: %v", err)
	}
}

type wireRecord struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error,omitempty"`
	Raw json.RawMessage `json:"-"`
}

type scriptedSession struct {
	inputWriter  *io.PipeWriter
	outputReader *io.PipeReader
	errorReader  *io.PipeReader
	done         chan struct{}
	closeOnce    sync.Once
	scriptDone   chan error
}

func newScriptedSession(t *testing.T, script func(*scriptServer)) *scriptedSession {
	t.Helper()
	clientInput, serverInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	clientErrors, serverErrors := io.Pipe()
	session := &scriptedSession{
		inputWriter:  serverInput,
		outputReader: clientOutput,
		errorReader:  clientErrors,
		done:         make(chan struct{}),
		scriptDone:   make(chan error, 1),
	}
	server := &scriptServer{t: t, reader: bufio.NewReader(clientInput), writer: serverOutput}
	go func() {
		defer func() {
			_ = clientInput.Close()
			_ = serverOutput.Close()
			_ = serverErrors.Close()
		}()
		script(server)
		session.scriptDone <- nil
		<-session.done
	}()
	return session
}

func (session *scriptedSession) Input() io.WriteCloser { return session.inputWriter }
func (session *scriptedSession) Output() io.ReadCloser { return session.outputReader }
func (session *scriptedSession) Errors() io.ReadCloser { return session.errorReader }
func (session *scriptedSession) Wait() error           { <-session.done; return nil }
func (session *scriptedSession) Close() error {
	session.closeOnce.Do(func() {
		close(session.done)
		_ = session.inputWriter.Close()
		_ = session.outputReader.Close()
		_ = session.errorReader.Close()
	})
	return nil
}

func (session *scriptedSession) scriptError() error {
	select {
	case err := <-session.scriptDone:
		return err
	case <-time.After(time.Second):
		return errors.New("script did not finish")
	}
}

type scriptServer struct {
	t      *testing.T
	reader *bufio.Reader
	writer io.Writer
}

func (server *scriptServer) read() wireRecord {
	server.t.Helper()
	line, err := server.reader.ReadBytes('\n')
	if err != nil {
		server.t.Fatalf("read client frame: %v", err)
	}
	var record wireRecord
	if err := json.Unmarshal(line, &record); err != nil {
		server.t.Fatalf("decode client frame: %v", err)
	}
	record.Raw = append(json.RawMessage(nil), line...)
	return record
}

func (server *scriptServer) requireMethod(record wireRecord, method string) {
	server.t.Helper()
	if record.Method != method {
		server.t.Fatalf("method = %q, want %q: %s", record.Method, method, record.Raw)
	}
}

func (server *scriptServer) decodeParams(record wireRecord, target any) {
	server.t.Helper()
	if err := json.Unmarshal(record.Params, target); err != nil {
		server.t.Fatalf("decode params: %v", err)
	}
}

func (server *scriptServer) respond(id json.RawMessage, result any) {
	server.t.Helper()
	var nativeID any
	if err := json.Unmarshal(id, &nativeID); err != nil {
		server.t.Fatalf("decode response id: %v", err)
	}
	server.write(map[string]any{"id": nativeID, "result": result})
}

func (server *scriptServer) request(id any, method string, params any) {
	server.t.Helper()
	server.write(map[string]any{"id": id, "method": method, "params": params})
}

func (server *scriptServer) notify(method string, params any) {
	server.t.Helper()
	server.write(map[string]any{"method": method, "params": params})
}

func (server *scriptServer) write(value any) {
	server.t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		server.t.Fatalf("encode server frame: %v", err)
	}
	server.writeRaw(append(encoded, '\n'))
}

func (server *scriptServer) writeRaw(value []byte) {
	server.t.Helper()
	if _, err := server.writer.Write(value); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		server.t.Fatalf("write server frame: %v", err)
	}
}

func (server *scriptServer) completeStart(threadID, turnID string) {
	server.t.Helper()
	initialize := server.read()
	server.requireMethod(initialize, "initialize")
	server.respond(initialize.ID, map[string]any{})
	server.requireMethod(server.read(), "initialized")
	thread := server.read()
	server.requireMethod(thread, "thread/start")
	server.respond(thread.ID, map[string]any{"thread": map[string]any{"id": threadID}})
	turn := server.read()
	server.requireMethod(turn, "turn/start")
	server.notify("turn/started", map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}})
	server.respond(turn.ID, map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}})
}

func collectEvents(t *testing.T, events <-chan dgruntime.Event, count int) []dgruntime.Event {
	t.Helper()
	result := make([]dgruntime.Event, 0, count)
	for len(result) < count {
		select {
		case event := <-events:
			result = append(result, event)
		case <-time.After(time.Second):
			t.Fatalf("received %d of %d events", len(result), count)
		}
	}
	return result
}

func waitForEvent(t *testing.T, events <-chan dgruntime.Event, eventType string) dgruntime.Event {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.Type == eventType {
				return event
			}
		case <-time.After(time.Second):
			t.Fatalf("event %q was not emitted", eventType)
		}
	}
}
