package audittransport

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	maximumCertificateBytes = 256 << 10
	maximumPrivateKeyBytes  = 256 << 10
	maximumTrustBundleBytes = 1 << 20
)

type MTLSConfig struct {
	ClientCertificateFile   string
	ClientPrivateKeyFile    string
	ServerTrustBundleFile   string
	ClientCertificateSHA256 string
	ServerTrustSHA256       string
}

// NewMTLSTransport creates one proxy-free HTTPS transport from exact
// owner-only identity and trust files bound by the canonical destination.
func NewMTLSTransport(config MTLSConfig) (*http.Transport, error) {
	if config.ClientCertificateFile == "" || config.ClientPrivateKeyFile == "" ||
		config.ServerTrustBundleFile == "" ||
		config.ClientCertificateFile == config.ClientPrivateKeyFile ||
		config.ClientCertificateFile == config.ServerTrustBundleFile ||
		config.ClientPrivateKeyFile == config.ServerTrustBundleFile {
		return nil, errors.New("audit export mTLS files are invalid")
	}
	certificatePEM, err := readStableOwnerFile(config.ClientCertificateFile, maximumCertificateBytes)
	if err != nil {
		return nil, err
	}
	defer clear(certificatePEM)
	privateKeyPEM, err := readStableOwnerFile(config.ClientPrivateKeyFile, maximumPrivateKeyBytes)
	if err != nil {
		return nil, err
	}
	defer clear(privateKeyPEM)
	trustPEM, err := readStableOwnerFile(config.ServerTrustBundleFile, maximumTrustBundleBytes)
	if err != nil {
		return nil, err
	}
	defer clear(trustPEM)
	if digestLabel(certificatePEM) != config.ClientCertificateSHA256 ||
		digestLabel(trustPEM) != config.ServerTrustSHA256 {
		return nil, errors.New("audit export mTLS material does not match the destination")
	}
	certificates, err := parseCertificateChain(certificatePEM)
	if err != nil || !validPrivateKeyPEM(privateKeyPEM) {
		return nil, errors.New("audit export mTLS client identity is invalid")
	}
	pair, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil || len(pair.Certificate) != len(certificates) {
		return nil, errors.New("audit export mTLS client identity is invalid")
	}
	leaf := certificates[0]
	if leaf.IsCA || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 ||
		!supportsClientAuthentication(leaf) {
		return nil, errors.New("audit export mTLS client certificate is invalid")
	}
	now := time.Now().UTC()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("audit export mTLS client certificate is not currently valid")
	}
	pair.Leaf = leaf
	roots, err := parseTrustBundle(trustPEM)
	if err != nil {
		return nil, err
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("audit export mTLS HTTP configuration is invalid")
	}
	transport := base.Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	transport.ForceAttemptHTTP2 = true
	transport.TLSClientConfig = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      roots,
		Certificates: []tls.Certificate{pair},
	}
	return transport, nil
}

func parseCertificateChain(encoded []byte) ([]*x509.Certificate, error) {
	var certificates []*x509.Certificate
	remainder := bytes.TrimSpace(encoded)
	for len(remainder) > 0 {
		block, rest := pem.Decode(remainder)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("audit export mTLS client certificate chain is invalid")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, errors.New("audit export mTLS client certificate chain is invalid")
		}
		certificates = append(certificates, certificate)
		remainder = bytes.TrimSpace(rest)
	}
	if len(certificates) == 0 {
		return nil, errors.New("audit export mTLS client certificate chain is invalid")
	}
	return certificates, nil
}

func validPrivateKeyPEM(encoded []byte) bool {
	block, rest := pem.Decode(bytes.TrimSpace(encoded))
	if block == nil || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return false
	}
	switch block.Type {
	case "PRIVATE KEY", "EC PRIVATE KEY", "RSA PRIVATE KEY":
		return true
	default:
		return false
	}
}

func supportsClientAuthentication(certificate *x509.Certificate) bool {
	for _, usage := range certificate.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func parseTrustBundle(encoded []byte) (*x509.CertPool, error) {
	roots := x509.NewCertPool()
	remainder := bytes.TrimSpace(encoded)
	count := 0
	for len(remainder) > 0 {
		block, rest := pem.Decode(remainder)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("audit export mTLS server trust bundle is invalid")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.BasicConstraintsValid || !certificate.IsCA {
			return nil, errors.New("audit export mTLS server trust bundle is invalid")
		}
		roots.AddCert(certificate)
		count++
		remainder = bytes.TrimSpace(rest)
	}
	if count == 0 {
		return nil, errors.New("audit export mTLS server trust bundle is invalid")
	}
	return roots, nil
}

func readStableOwnerFile(path string, maximum int64) ([]byte, error) {
	if maximum <= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("audit export mTLS file path is invalid")
	}
	directory := filepath.Dir(path)
	directoryInfo, err := os.Lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode().Perm() != 0o700 ||
		!ownedByProcess(directoryInfo) {
		return nil, errors.New("audit export mTLS directory must be owner-only")
	}
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil || resolvedDirectory != directory {
		return nil, errors.New("audit export mTLS directory is invalid")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode().Perm() != 0o600 ||
		pathInfo.Size() <= 0 || pathInfo.Size() > maximum || !ownedByProcess(pathInfo) {
		return nil, errors.New("audit export mTLS file must be owner-only")
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil || resolvedPath != path {
		return nil, errors.New("audit export mTLS file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("audit export mTLS file is unavailable")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !sameFileState(pathInfo, openedInfo) {
		return nil, errors.New("audit export mTLS file changed during read")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(content) == 0 || int64(len(content)) > maximum {
		clear(content)
		return nil, errors.New("audit export mTLS file is invalid")
	}
	pathAfter, pathErr := os.Lstat(path)
	directoryAfter, directoryErr := os.Lstat(directory)
	resolvedAfter, resolvedErr := filepath.EvalSymlinks(path)
	if pathErr != nil || directoryErr != nil || resolvedErr != nil ||
		!sameFileState(pathInfo, pathAfter) || !sameDirectoryState(directoryInfo, directoryAfter) ||
		resolvedAfter != path || pathAfter.Mode().Perm() != 0o600 ||
		directoryAfter.Mode().Perm() != 0o700 {
		clear(content)
		return nil, errors.New("audit export mTLS file changed during read")
	}
	return content, nil
}

func ownedByProcess(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func sameFileState(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.Mode().IsRegular() && right.Mode().IsRegular() &&
		os.SameFile(left, right) && sameOwner(left, right) && left.Size() == right.Size() &&
		left.Mode() == right.Mode() && left.ModTime().Equal(right.ModTime())
}

func sameDirectoryState(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.IsDir() && right.IsDir() &&
		os.SameFile(left, right) && sameOwner(left, right) && left.Mode() == right.Mode()
}

func sameOwner(left, right os.FileInfo) bool {
	leftStat, leftOK := left.Sys().(*syscall.Stat_t)
	rightStat, rightOK := right.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && leftStat.Uid == rightStat.Uid
}

func digestLabel(content []byte) string {
	digest := sha256.Sum256(content)
	return fmt.Sprintf("sha256:%s", hex.EncodeToString(digest[:]))
}
