package persistence_test

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	"github.com/asabla/dataground/internal/execution/openshell/retentionrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type stoppedWorkspaceGatewayFixture struct {
	retentionrpc.UnimplementedWorkspaceRetentionServer
	archive     []byte
	endpoint    string
	roots       *x509.CertPool
	certificate tls.Certificate
	calls       atomic.Int32
}

const stoppedFixtureWorkspace = "retention-fixture"
const stoppedFixtureSandbox = "retention-sandbox"
const stoppedFixtureGeneration = "generation-1"
const stoppedFixtureSubject = "workspace-admin-fixture"

func newStoppedWorkspaceGatewayFixture(t *testing.T) *stoppedWorkspaceGatewayFixture {
	t.Helper()
	var buffer bytes.Buffer
	content := []byte("updated\x00\xfe")
	archive := tar.NewWriter(&buffer)
	if err := archive.WriteHeader(&tar.Header{Name: "workspace/", Mode: 0700, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	if err := archive.WriteHeader(&tar.Header{Name: "workspace/private.bin", Mode: 0600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	identity := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: stoppedFixtureSubject}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, identity, identity, public, private)
	if err != nil {
		t.Fatal(err)
	}
	trustedClient, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	clients := x509.NewCertPool()
	clients.AddCert(trustedClient)
	fixture := &stoppedWorkspaceGatewayFixture{archive: buffer.Bytes(), certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: private}, roots: x509.NewCertPool()}
	grpcServer := grpc.NewServer()
	retentionrpc.RegisterWorkspaceRetentionServer(grpcServer, fixture)
	server := httptest.NewUnstartedServer(grpcServer)
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clients, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(func() { grpcServer.Stop(); server.Close() })
	fixture.roots.AddCert(server.Certificate())
	fixture.endpoint = server.URL
	return fixture
}

func (fixture *stoppedWorkspaceGatewayFixture) bind(t *testing.T, store execution.StateStore, ref execution.ExecutionRef, gatewayID string) *openshell.StoppedWorkspaceChangeProvider {
	t.Helper()
	provider, err := openshell.NewStoppedWorkspaceChangeProvider(store, openshell.StoppedWorkspaceBinding{
		Execution: ref, GatewayID: gatewayID, GatewayEndpoint: fixture.endpoint,
		Workspace: stoppedFixtureWorkspace, SandboxID: stoppedFixtureSandbox, Generation: stoppedFixtureGeneration,
	}, fixture.roots, fixture.certificate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

func (fixture *stoppedWorkspaceGatewayFixture) ReadStoppedWorkspace(request *retentionrpc.ReadStoppedWorkspaceRequest, stream grpc.ServerStreamingServer[retentionrpc.ReadStoppedWorkspaceFrame]) error {
	client, present := peer.FromContext(stream.Context())
	if !present {
		return status.Error(codes.Unauthenticated, "missing test peer")
	}
	info, verified := client.AuthInfo.(credentials.TLSInfo)
	if !verified || len(info.State.VerifiedChains) == 0 || info.State.VerifiedChains[0][0].Subject.CommonName != stoppedFixtureSubject {
		return status.Error(codes.Unauthenticated, "missing test client identity")
	}
	if request.GetWorkspaceScope().GetWorkspace() != stoppedFixtureWorkspace || request.GetSandboxId() != stoppedFixtureSandbox || request.GetGeneration() != stoppedFixtureGeneration {
		return status.Error(codes.PermissionDenied, "substituted stopped workspace binding")
	}
	attempt := fixture.calls.Add(1)
	if err := stream.Send(&retentionrpc.ReadStoppedWorkspaceFrame{Payload: &retentionrpc.ReadStoppedWorkspaceFrame_Header{Header: &retentionrpc.StoppedWorkspaceArchiveHeader{
		SizeBytes: uint64(len(fixture.archive)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(fixture.archive)),
	}}}); err != nil {
		return err
	}
	for _, chunk := range [][]byte{fixture.archive[:512], fixture.archive[512:]} {
		if err := stream.Send(&retentionrpc.ReadStoppedWorkspaceFrame{Payload: &retentionrpc.ReadStoppedWorkspaceFrame_Data{Data: chunk}}); err != nil {
			return err
		}
	}
	if attempt == 1 {
		return status.Error(codes.Unavailable, "lost receipt after all data frames")
	}
	return nil
}
