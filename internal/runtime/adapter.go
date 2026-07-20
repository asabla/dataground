// Package runtime defines the normalized, provider-independent contract used
// between DataGround workers and native agent runtime adapters.
package runtime

import (
	"context"
	"errors"
)

var (
	ErrClosed           = errors.New("runtime adapter is closed")
	ErrProtocol         = errors.New("runtime protocol violation")
	ErrTurnFailed       = errors.New("runtime turn failed")
	ErrApprovalNotFound = errors.New("runtime approval not found")
	ErrApprovalMode     = errors.New("runtime approval mode is invalid")
	ErrApprovalDecision = errors.New("runtime approval decision is invalid")
	ErrConcurrentTurn   = errors.New("runtime adapter already has an active turn")
	ErrSandboxMode      = errors.New("runtime sandbox mode is invalid")
)

type ApprovalMode string

const (
	ApprovalLocked      ApprovalMode = "locked"
	ApprovalInteractive ApprovalMode = "interactive"
)

type ApprovalDecision string

const (
	ApprovalApprove ApprovalDecision = "approve"
	ApprovalDeny    ApprovalDecision = "deny"
)

type SandboxMode string

const (
	SandboxReadOnly       SandboxMode = "read-only"
	SandboxWorkspaceWrite SandboxMode = "workspace-write"
)

// StartRequest contains only normalized inputs. Runtime-native thread and turn
// identifiers remain private to the adapter.
type StartRequest struct {
	Prompt       string
	WorkingDir   string
	Model        string
	OutputSchema map[string]any
	ApprovalMode ApprovalMode
	SandboxMode  SandboxMode
}

// Event is emitted before product identity and persistence fields are attached
// by the invocation worker. Payloads must not contain runtime-native routing
// identifiers.
type Event struct {
	Sequence uint64
	Type     string
	Payload  map[string]any
}

// Turn is the stable worker-facing surface for one active runtime turn.
type Turn interface {
	Events() <-chan Event
	ResolveApproval(context.Context, string, ApprovalDecision) error
	Interrupt(context.Context) error
	Wait(context.Context) error
	Close() error
}
