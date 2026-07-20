package execution

import (
	"context"
	"io"
	"time"
)

type GatewayState string

const (
	GatewayActive      GatewayState = "active"
	GatewayDraining    GatewayState = "draining"
	GatewayUnavailable GatewayState = "unavailable"
	GatewayLost        GatewayState = "lost"
)

type GatewayRegistration struct {
	ID           string
	Endpoint     string `json:"-"`
	Driver       string
	Capabilities []string
}

type Gateway struct {
	ID           string       `json:"id"`
	Driver       string       `json:"driver"`
	State        GatewayState `json:"state"`
	Capabilities []string     `json:"capabilities"`
}

type PlacementRequest struct {
	IsolationDomainID    string
	OperationID          string
	RequiredCapabilities []string
}

type Placement struct {
	ID        string `json:"id"`
	GatewayID string `json:"gatewayId"`
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
	ID        string `json:"id"`
	GatewayID string `json:"gatewayId"`
	State     string `json:"state"`
}

type Observation struct {
	ExecutionID string    `json:"executionId"`
	State       string    `json:"state"`
	ObservedAt  time.Time `json:"observedAt"`
}

type LogRequest struct {
	ExecutionID string
	Lines       uint32
}

type ExportRequest struct {
	ExecutionID string
	SandboxPath string
	Destination string
}

type ExportResult struct {
	ExecutionID string `json:"executionId"`
	Destination string `json:"-"`
}

type Orphan struct {
	ID        string `json:"id"`
	GatewayID string `json:"gatewayId"`
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
	SetGatewayState(context.Context, string, GatewayState) error
	SelectGateway(context.Context, PlacementRequest) (Placement, error)
	Create(context.Context, CreateRequest) (Execution, error)
	Observe(context.Context, string) (Observation, error)
	StartRuntime(context.Context, string) (RuntimeSession, error)
	Logs(context.Context, LogRequest) ([]byte, error)
	Export(context.Context, ExportRequest) (ExportResult, error)
	Terminate(context.Context, string) error
	ListOrphans(context.Context, string, map[string]struct{}) ([]Orphan, error)
}
