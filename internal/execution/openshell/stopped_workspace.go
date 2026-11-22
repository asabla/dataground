package openshell

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/url"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell/retentionrpc"
	"github.com/asabla/dataground/internal/workspace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const stoppedWorkspaceChunkBytes = 64 << 10

var (
	stoppedWorkspaceName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	stoppedSandboxID     = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
	stoppedGeneration    = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
)

// StoppedWorkspaceBinding pins the native identity and route for one execution.
// The caller must supply the original provider generation from a trusted
// lifecycle binding established before running the workload.
type StoppedWorkspaceBinding struct {
	Execution       execution.ExecutionRef
	GatewayID       string
	GatewayEndpoint string `json:"-"`
	Workspace       string `json:"-"`
	SandboxID       string `json:"-"`
	Generation      string `json:"-"`
}

// StoppedWorkspaceChangeProvider implements the worker's existing workspace
// change export port. The claim owner still supplies current export authority,
// an accepted gateway, and a verified base archive.
type StoppedWorkspaceChangeProvider struct {
	store   execution.StateStore
	binding StoppedWorkspaceBinding
	conn    *grpc.ClientConn
}

// NewStoppedWorkspaceChangeProvider connects only to the pinned HTTPS gateway
// with its deployment-owned CA and the worker's workspace-scoped client certificate.
func NewStoppedWorkspaceChangeProvider(store execution.StateStore, binding StoppedWorkspaceBinding, roots *x509.CertPool, certificate tls.Certificate) (*StoppedWorkspaceChangeProvider, error) {
	endpoint, err := url.Parse(binding.GatewayEndpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil ||
		endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.RawFragment != "" ||
		store == nil || roots == nil || len(certificate.Certificate) == 0 || certificate.PrivateKey == nil ||
		binding.Execution.IsolationDomainID == "" || binding.Execution.ID == "" || binding.GatewayID == "" ||
		!stoppedWorkspaceName.MatchString(binding.Workspace) || !stoppedSandboxID.MatchString(binding.SandboxID) || !stoppedGeneration.MatchString(binding.Generation) {
		return nil, ErrWorkspaceChangeExport
	}
	port := endpoint.Port()
	if port == "" {
		port = "443"
	}
	tlsConfig := &tls.Config{
		ServerName:   endpoint.Hostname(),
		RootCAs:      roots.Clone(),
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	}
	conn, err := grpc.NewClient("passthrough:///"+net.JoinHostPort(endpoint.Hostname(), port),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithNoProxy(),
		grpc.WithDisableServiceConfig(),
		grpc.WithDisableRetry(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(stoppedWorkspaceChunkBytes+256)),
	)
	if err != nil {
		return nil, ErrWorkspaceChangeExport
	}
	return &StoppedWorkspaceChangeProvider{store: store, binding: binding, conn: conn}, nil
}

func (provider *StoppedWorkspaceChangeProvider) Close() error {
	return provider.conn.Close()
}

func (provider *StoppedWorkspaceChangeProvider) ExportWorkspaceChanges(ctx context.Context, ref execution.ExecutionRef, base *workspace.Snapshot) (*workspace.ChangeSet, error) {
	if provider == nil || provider.conn == nil || ctx == nil || ctx.Err() != nil || ref != provider.binding.Execution || base == nil || !workspaceArchiveDigestPattern.MatchString(base.Receipt().ArchiveDigest) {
		return nil, ErrWorkspaceChangeExport
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	entry, err := provider.store.GetExecution(ctx, ref)
	if err != nil || !entry.WorkspaceSourceSelected || entry.Execution.State != "ready" || entry.Execution.ID != ref.ID ||
		entry.Execution.IsolationDomainID != ref.IsolationDomainID || entry.Execution.GatewayID != provider.binding.GatewayID {
		return nil, ErrWorkspaceChangeExport
	}
	gateway, err := provider.store.GetGateway(ctx, ref.IsolationDomainID, provider.binding.GatewayID)
	if err != nil || gateway.Endpoint != provider.binding.GatewayEndpoint || gateway.Gateway.ID != provider.binding.GatewayID ||
		gateway.Gateway.IsolationDomainID != ref.IsolationDomainID || gateway.Gateway.Driver != "docker" ||
		(gateway.Gateway.State != execution.GatewayActive && gateway.Gateway.State != execution.GatewayDraining) || ctx.Err() != nil {
		return nil, ErrWorkspaceChangeExport
	}
	stream, err := retentionrpc.NewWorkspaceRetentionClient(provider.conn).ReadStoppedWorkspace(ctx, &retentionrpc.ReadStoppedWorkspaceRequest{
		WorkspaceScope: &retentionrpc.WorkspaceSelector{Selection: &retentionrpc.WorkspaceSelector_Workspace{Workspace: provider.binding.Workspace}},
		SandboxId:      provider.binding.SandboxID,
		Generation:     provider.binding.Generation,
	})
	if err != nil {
		return nil, ErrWorkspaceChangeExport
	}
	return compareStoppedWorkspaceStream(ctx, stream, base)
}

type stoppedWorkspaceStream interface {
	Recv() (*retentionrpc.ReadStoppedWorkspaceFrame, error)
}

func compareStoppedWorkspaceStream(ctx context.Context, stream stoppedWorkspaceStream, base *workspace.Snapshot) (*workspace.ChangeSet, error) {
	if ctx.Err() != nil {
		return nil, ErrWorkspaceChangeExport
	}
	frame, err := stream.Recv()
	header := frame.GetHeader()
	if err != nil || header == nil || len(frame.ProtoReflect().GetUnknown()) != 0 || len(header.ProtoReflect().GetUnknown()) != 0 ||
		header.SizeBytes < 1024 || header.SizeBytes > workspace.MaximumArchiveBytes || header.SizeBytes%512 != 0 || !workspaceArchiveDigestPattern.MatchString(header.Digest) {
		clear(frame.GetData())
		return nil, ErrWorkspaceChangeExport
	}
	archive := make([]byte, 0, int(header.SizeBytes))
	defer func() { clear(archive) }()
	for ctx.Err() == nil {
		frame, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		data := frame.GetData()
		if err != nil || len(data) == 0 || len(data) > stoppedWorkspaceChunkBytes || len(data) > cap(archive)-len(archive) || len(frame.ProtoReflect().GetUnknown()) != 0 {
			clear(data)
			return nil, ErrWorkspaceChangeExport
		}
		archive = append(archive, data...)
		clear(data)
	}
	if len(archive) != int(header.SizeBytes) || ctx.Err() != nil {
		return nil, ErrWorkspaceChangeExport
	}
	changes, err := workspace.CompareStoppedDockerArchive(base, archive, header.Digest)
	if err != nil || ctx.Err() != nil {
		return nil, ErrWorkspaceChangeExport
	}
	return changes, nil
}
