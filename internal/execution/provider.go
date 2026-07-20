package execution

import (
	"context"
	"errors"
	"io"
	"slices"
	"sort"
	"time"
)

type GatewayState string

const (
	GatewayActive      GatewayState = "active"
	GatewayDraining    GatewayState = "draining"
	GatewayUnavailable GatewayState = "unavailable"
	GatewayLost        GatewayState = "lost"
)

var (
	ErrNoGateway        = errors.New("no eligible execution gateway")
	ErrPlacementMissing = errors.New("execution placement not found")
	ErrExecutionMissing = errors.New("execution not found")
	ErrStateConflict    = errors.New("execution state conflicts with persisted state")
)

func ValidGatewayState(state GatewayState) bool {
	switch state {
	case GatewayActive, GatewayDraining, GatewayUnavailable, GatewayLost:
		return true
	default:
		return false
	}
}

func NormalizeCapabilities(capabilities []string) ([]string, error) {
	normalized := append([]string(nil), capabilities...)
	for _, capability := range normalized {
		if capability == "" {
			return nil, errors.New("capability names must not be empty")
		}
	}
	sort.Strings(normalized)
	return slices.Compact(normalized), nil
}

type GatewayRegistration struct {
	IsolationDomainID string
	ID                string
	Endpoint          string `json:"-"`
	Driver            string
	Capabilities      []string
}

type Gateway struct {
	IsolationDomainID string       `json:"isolationDomainId"`
	ID                string       `json:"id"`
	Driver            string       `json:"driver"`
	State             GatewayState `json:"state"`
	Capabilities      []string     `json:"capabilities"`
}

type PlacementRequest struct {
	IsolationDomainID    string
	OperationID          string
	RequiredCapabilities []string
}

type Placement struct {
	IsolationDomainID string `json:"isolationDomainId"`
	ID                string `json:"id"`
	GatewayID         string `json:"gatewayId"`
}

type CreateRequest struct {
	Placement         Placement
	IsolationDomainID string
	OperationID       string
	Image             string
	PolicyPath        string
	PolicySHA256      string
	ProviderProfiles  []string
}

type Execution struct {
	IsolationDomainID string `json:"isolationDomainId"`
	ID                string `json:"id"`
	GatewayID         string `json:"gatewayId"`
	State             string `json:"state"`
}

type ExecutionRef struct {
	IsolationDomainID string
	ID                string
}

type Observation struct {
	IsolationDomainID string    `json:"isolationDomainId"`
	ExecutionID       string    `json:"executionId"`
	State             string    `json:"state"`
	ObservedAt        time.Time `json:"observedAt"`
}

type LogRequest struct {
	IsolationDomainID string
	ExecutionID       string
	Lines             uint32
}

type ExportRequest struct {
	IsolationDomainID string
	ExecutionID       string
	SandboxPath       string
	Destination       string
}

type ExportResult struct {
	IsolationDomainID string `json:"isolationDomainId"`
	ExecutionID       string `json:"executionId"`
	Destination       string `json:"-"`
}

type Orphan struct {
	IsolationDomainID string `json:"isolationDomainId"`
	ID                string `json:"id"`
	GatewayID         string `json:"gatewayId"`
}

type GatewayRecord struct {
	Gateway  Gateway
	Endpoint string `json:"-"`
}

type ExecutionRecord struct {
	Execution   Execution
	PlacementID string
	OperationID string
	SandboxName string `json:"-"`
}

// StateStore persists provider-private routing state. Implementations must
// scope every lookup and uniqueness constraint by isolation domain.
type StateStore interface {
	RegisterGateway(context.Context, GatewayRegistration) (Gateway, error)
	SetGatewayState(context.Context, string, string, GatewayState) error
	ReservePlacement(context.Context, PlacementRequest) (Placement, error)
	GetPlacement(context.Context, string, string) (Placement, error)
	GetGateway(context.Context, string, string) (GatewayRecord, error)
	SaveExecution(context.Context, ExecutionRecord) error
	GetExecution(context.Context, ExecutionRef) (ExecutionRecord, error)
	UpdateExecutionState(context.Context, ExecutionRef, string) error
}

// RuntimeSession is a private transport to a native runtime protocol. It
// intentionally carries no gateway, sandbox, or runtime endpoint.
type RuntimeSession interface {
	Input() io.WriteCloser
	Output() io.ReadCloser
	Errors() io.ReadCloser
	Wait() error
	Close() error
}

// ExecutionProvider is DataGround's stable internal port for sandbox
// providers. Native provider resources and endpoints must not cross it.
type ExecutionProvider interface {
	RegisterGateway(context.Context, GatewayRegistration) (Gateway, error)
	SetGatewayState(context.Context, string, string, GatewayState) error
	SelectGateway(context.Context, PlacementRequest) (Placement, error)
	Create(context.Context, CreateRequest) (Execution, error)
	Observe(context.Context, ExecutionRef) (Observation, error)
	StartRuntime(context.Context, ExecutionRef) (RuntimeSession, error)
	Logs(context.Context, LogRequest) ([]byte, error)
	Export(context.Context, ExportRequest) (ExportResult, error)
	Terminate(context.Context, ExecutionRef) error
	ListOrphans(context.Context, string, string, map[string]struct{}) ([]Orphan, error)
}
