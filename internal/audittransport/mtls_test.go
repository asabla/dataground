package audittransport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMTLSTransportAuthenticatesPinnedPeers(t *testing.T) {
	fixture := newMTLSFixture(t, true)
	transport, err := NewMTLSTransport(fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 ||
			request.TLS.PeerCertificates[0].Subject.CommonName != "audit-export-client" {
			t.Fatal("request did not carry the pinned client identity")
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{fixture.serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    fixture.roots,
	}
	server.StartTLS()
	defer server.Close()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("authenticated request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestMTLSTransportRejectsUnboundOrInvalidIdentity(t *testing.T) {
	fixture := newMTLSFixture(t, true)
	fixture.config.ClientCertificateSHA256 = "sha256:" + strings.Repeat("0", 64)
	if _, err := NewMTLSTransport(fixture.config); err == nil {
		t.Fatal("unbound client certificate was accepted")
	}
	fixture = newMTLSFixture(t, false)
	if _, err := NewMTLSTransport(fixture.config); err == nil {
		t.Fatal("certificate without client authentication usage was accepted")
	}
	fixture = newMTLSFixtureWithKeyUsage(t, true, x509.KeyUsageKeyEncipherment)
	if _, err := NewMTLSTransport(fixture.config); err == nil {
		t.Fatal("client certificate without digital signature usage was accepted")
	}
	fixture = newMTLSFixture(t, true)
	if err := os.Chmod(fixture.config.ClientPrivateKeyFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMTLSTransport(fixture.config); err == nil {
		t.Fatal("non-owner-only private key was accepted")
	}
	fixture = newMTLSFixture(t, true)
	certificatePEM, err := os.ReadFile(fixture.config.ClientCertificateFile)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM = append(certificatePEM, []byte("unexpected trailing data\n")...)
	if err := os.WriteFile(fixture.config.ClientCertificateFile, certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.config.ClientCertificateSHA256 = testDigest(certificatePEM)
	if _, err := NewMTLSTransport(fixture.config); err == nil {
		t.Fatal("client certificate file with hidden trailing data was accepted")
	}
}

type mtlsFixture struct {
	config            MTLSConfig
	serverCertificate tls.Certificate
	roots             *x509.CertPool
}

func newMTLSFixture(t *testing.T, clientAuth bool) mtlsFixture {
	return newMTLSFixtureWithKeyUsage(t, clientAuth, x509.KeyUsageDigitalSignature)
}

func newMTLSFixtureWithKeyUsage(
	t *testing.T,
	clientAuth bool,
	clientKeyUsage x509.KeyUsage,
) mtlsFixture {
	t.Helper()
	now := time.Now().UTC()
	rootKey := newECDSAKey(t)
	rootTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "audit-export-test-root"},
		NotBefore:    now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	rootDER := signCertificate(t, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})
	serverKey := newECDSAKey(t)
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "audit-export-server"},
		NotBefore:    now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER := signCertificate(t, serverTemplate, root, &serverKey.PublicKey, rootKey)
	serverCertificate := tls.Certificate{
		Certificate: [][]byte{serverDER, rootDER}, PrivateKey: serverKey,
	}
	clientKey := newECDSAKey(t)
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "audit-export-client"},
		NotBefore:    now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: clientKeyUsage,
	}
	if clientAuth {
		clientTemplate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	} else {
		clientTemplate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	clientDER := signCertificate(t, clientTemplate, root, &clientKey.PublicKey, rootKey)
	clientPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
	keyDER, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	certificateFile := writeOwnerFile(t, directory, "client.pem", clientPEM)
	privateKeyFile := writeOwnerFile(t, directory, "client-key.pem", keyPEM)
	trustFile := writeOwnerFile(t, directory, "server-roots.pem", rootPEM)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	return mtlsFixture{
		config: MTLSConfig{
			ClientCertificateFile:   certificateFile,
			ClientPrivateKeyFile:    privateKeyFile,
			ServerTrustBundleFile:   trustFile,
			ClientCertificateSHA256: testDigest(clientPEM),
			ServerTrustSHA256:       testDigest(rootPEM),
		},
		serverCertificate: serverCertificate,
		roots:             roots,
	}
}

func newECDSAKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func signCertificate(
	t *testing.T,
	template *x509.Certificate,
	parent *x509.Certificate,
	publicKey any,
	privateKey any,
) []byte {
	t.Helper()
	encoded, err := x509.CreateCertificate(rand.Reader, template, parent, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func writeOwnerFile(t *testing.T, directory, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}
