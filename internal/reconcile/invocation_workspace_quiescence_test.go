package reconcile

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/runtime/nativehost"
	"github.com/asabla/dataground/internal/workspace"
)

type cleanupStoreStub struct {
	entry                 persistence.InvocationWorkspaceQuiescenceEntry
	beginErr, completeErr error
	alter                 func(*persistence.InvocationWorkspaceQuiescenceEntry)
	completed             int
}

func (s *cleanupStoreStub) GetInvocationWorkspaceQuiescence(context.Context, string, string) (persistence.InvocationWorkspaceQuiescenceEntry, error) {
	if s.entry.Record.SchemaVersion == "" {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	return s.entry, nil
}

func (s *cleanupStoreStub) BeginInvocationWorkspaceQuiescence(_ context.Context, c persistence.OperationClaim, e persistence.EffectRecord, r execution.ExecutionRef, b string) (persistence.InvocationWorkspaceQuiescenceEntry, error) {
	if s.entry.Record.SchemaVersion == "" {
		s.entry = persistence.InvocationWorkspaceQuiescenceEntry{Record: persistence.InvocationWorkspaceQuiescence{SchemaVersion: persistence.WorkspaceQuiescenceContract, Source: workspace.SourceRecord{IsolationDomainID: r.IsolationDomainID}, InvocationID: c.ResourceID, OperationID: c.ID, EffectID: e.EffectID, ExecutionID: r.ID, OwnerBinding: b, RuntimeOwner: c.LeaseOwner, RuntimeToken: c.FencingToken, ActorID: c.ActorID, CorrelationID: c.CorrelationID}, ExpiresAt: time.Now().Add(time.Second)}
	}
	entry := s.entry
	if s.alter != nil {
		s.alter(&entry)
	}
	return entry, s.beginErr
}
func (s *cleanupStoreStub) CompleteInvocationWorkspaceQuiescence(ctx context.Context, _ persistence.OperationClaim, _ persistence.EffectRecord, r persistence.InvocationWorkspaceQuiescence) (persistence.InvocationWorkspaceQuiescenceEntry, error) {
	s.completed++
	if ctx.Err() != nil || r != s.entry.Record {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	s.entry.Confirmed = true
	return s.entry, s.completeErr
}

type cleanupTransport struct {
	in              *io.PipeWriter
	out, diagnostic *io.PipeReader
	done            chan struct{}
	once            sync.Once
	err             error
	closeIO         func()
	requests        atomic.Int32
}

func (s *cleanupTransport) Input() io.WriteCloser { return s.in }
func (s *cleanupTransport) Output() io.ReadCloser { return s.out }
func (s *cleanupTransport) Errors() io.ReadCloser { return s.diagnostic }
func (s *cleanupTransport) Wait() error           { <-s.done; return s.err }
func (s *cleanupTransport) finish(err error)      { s.once.Do(func() { s.err = err; close(s.done) }) }
func (s *cleanupTransport) Close() error {
	s.closeIO()
	s.finish(errors.New("test transport closed"))
	return nil
}

// This scripted trusted peer tests controller ordering, not Linux cleanup.
// Linux descendant cleanup and live gateway ownership have separate tests.
func newCleanupSession(t *testing.T, ctx context.Context, acknowledge bool) (*nativehost.Session, *cleanupTransport) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	s := &cleanupTransport{in: inW, out: outR, diagnostic: errR, done: make(chan struct{})}
	s.closeIO = func() {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
	}
	t.Cleanup(func() { s.Close() })
	go func() {
		read := func() (byte, []byte, error) {
			var header [5]byte
			if _, err := io.ReadFull(inR, header[:]); err != nil {
				return 0, nil, err
			}
			size := binary.BigEndian.Uint32(header[1:])
			if size > 65536 {
				return 0, nil, errors.New("oversize test frame")
			}
			data := make([]byte, size)
			_, err := io.ReadFull(inR, data)
			return header[0], data, err
		}
		write := func(kind byte, data []byte) error {
			var header [5]byte
			header[0] = kind
			binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
			if _, err := outW.Write(header[:]); err != nil {
				return err
			}
			_, err := outW.Write(data)
			return err
		}
		result := error(nil)
		defer func() { outW.Close(); errW.Close(); inR.Close(); s.finish(result) }()
		kind, data, err := read()
		if err != nil || kind != 1 || len(data) < 32 {
			result = errors.New("bad hello")
			return
		}
		nonce := append([]byte(nil), data[len(data)-32:]...)
		defer clear(nonce)
		if write(16, nonce) != nil {
			return
		}
		kind, data, err = read()
		if err != nil || kind != 4 || len(data) != 0 {
			result = errors.New("bad terminal request")
			return
		}
		s.requests.Add(1)
		if _, _, err = read(); err != io.EOF {
			result = errors.New("missing final EOF")
			return
		}
		for _, kind := range []byte{19, 20} {
			if write(kind, nil) != nil {
				return
			}
		}
		if write(21, []byte{1}) != nil {
			return
		}
		if acknowledge {
			result = write(22, nonce)
		}
	}()
	session, err := nativehost.NewSession(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	return session, s
}

func TestWorkspaceCleanupControllerRequiresExactTerminalExchange(t *testing.T) {
	for _, mode := range []string{"success", "lost acknowledgement", "intent failure", "foreign owner", "foreign execution", "expired intent", "entry denial", "effect denial", "completion denial", "lost commit acknowledgement", "cleanup before intent"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			f := newPreparationFixture(t)
			session, raw := newCleanupSession(t, ctx, mode != "lost acknowledgement")
			store := &cleanupStoreStub{}
			if mode == "cleanup before intent" {
				if err := session.Quiesce(ctx); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "intent failure":
				store.beginErr = errors.New("intent rejected")
			case "foreign owner":
				store.alter = func(e *persistence.InvocationWorkspaceQuiescenceEntry) { e.Record.OwnerBinding = "foreign" }
			case "foreign execution":
				store.alter = func(e *persistence.InvocationWorkspaceQuiescenceEntry) { e.Record.ExecutionID = "foreign" }
			case "expired intent":
				store.alter = func(e *persistence.InvocationWorkspaceQuiescenceEntry) { e.ExpiresAt = time.Now().Add(-time.Second) }
			case "lost commit acknowledgement":
				store.completeErr = errors.New("commit acknowledgement lost")
			}
			checks := 0
			check := func(context.Context) error {
				checks++
				if mode == "entry denial" && checks == 1 || mode == "effect denial" && checks == 2 || mode == "completion denial" && checks == 3 {
					return errors.New("authority withdrawn")
				}
				return nil
			}
			entry, err := ConfirmInvocationWorkspaceQuiescence(ctx, store, f.claim, f.effect, f.ref, session, check)
			if mode == "success" {
				if err != nil || !entry.Confirmed || store.completed != 1 || checks != 3 || raw.requests.Load() != 1 {
					t.Fatal("terminal completion", entry, err, checks, store.completed)
				}
			} else if err == nil || entry.Confirmed {
				t.Fatal("unconfirmed cleanup accepted", mode)
			}
			if mode != "success" && mode != "lost commit acknowledgement" && store.completed != 0 {
				t.Fatal("failure recorded completion")
			}
			if mode == "intent failure" || mode == "foreign owner" || mode == "foreign execution" || mode == "expired intent" || mode == "entry denial" || mode == "effect denial" {
				if raw.requests.Load() != 0 {
					t.Fatal("cleanup preceded exact authorized intent")
				}
			}
			if mode == "success" || mode == "lost commit acknowledgement" || mode == "completion denial" {
				store.completeErr = nil
				again, err := ConfirmInvocationWorkspaceQuiescence(ctx, store, f.claim, f.effect, f.ref, session, func(context.Context) error { return nil })
				if err != nil || !again.Confirmed || store.completed != 1 || raw.requests.Load() != 1 {
					t.Fatal("committed cleanup replay repeated effect", err)
				}
			}
		})
	}
}
