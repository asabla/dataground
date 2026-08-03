package auditseal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/asabla/dataground/internal/persistence"
)

const (
	EnvelopeContract  = "dataground.audit-export-envelope/ed25519/v1"
	SignatureContract = "dataground.audit-export-signature/ed25519/v1"
	TrustContract     = "dataground.audit-export-trust/ed25519/v1"

	AuthorizationExportKind = "authorization"
	OperatorExportKind      = "operator"

	maximumExportBytes  = 72 << 20
	maximumControlBytes = 1 << 20
	maximumJSONDepth    = 16
	signatureDomain     = "DataGround audit export envelope v1\n"
)

var (
	keyIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,63}$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Signature struct {
	Contract  string `json:"contract"`
	KeyID     string `json:"keyId"`
	Signature string `json:"signature"`
}

type TrustedKey struct {
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"`
}

type TrustProfile struct {
	Contract string       `json:"contract"`
	Keys     []TrustedKey `json:"keys"`
}

type Envelope struct {
	Contract           string          `json:"contract"`
	ExportKind         string          `json:"exportKind"`
	ExportSHA256       string          `json:"exportSha256"`
	TrustProfileSHA256 string          `json:"trustProfileSha256"`
	Export             json.RawMessage `json:"export"`
	Signature          Signature       `json:"signature"`
}

type PrepareRequest struct {
	ExportFile         string
	TrustProfileFile   string
	SigningMessageFile string
}

type InstallRequest struct {
	ExportFile       string
	SignatureFile    string
	TrustProfileFile string
	OutputFile       string
}

func PrepareSigningMessage(request PrepareRequest) error {
	if !distinctPaths(request.ExportFile, request.TrustProfileFile, request.SigningMessageFile) {
		return errors.New("audit export signing paths are invalid")
	}
	export, kind, err := readAndValidateExport(request.ExportFile)
	if err != nil {
		return err
	}
	defer clear(export)
	trust, canonicalTrust, err := readTrustProfile(request.TrustProfileFile)
	if err != nil {
		return err
	}
	defer clear(canonicalTrust)
	if err := validateTrustProfile(trust); err != nil {
		return err
	}
	trustDigest := sha256.Sum256(canonicalTrust)
	message := signingMessage(export, kind, digestString(trustDigest))
	defer clear(message)
	if err := installNewPrivateFile(request.SigningMessageFile, message, maximumExportBytes+maximumControlBytes); err != nil {
		return fmt.Errorf("install audit export signing message: %w", err)
	}
	return nil
}

func Install(request InstallRequest) error {
	if !distinctPaths(request.ExportFile, request.SignatureFile, request.TrustProfileFile, request.OutputFile) {
		return errors.New("audit export sealing paths are invalid")
	}
	export, kind, err := readAndValidateExport(request.ExportFile)
	if err != nil {
		return err
	}
	defer clear(export)
	signatureBytes, err := readStablePrivateFile(request.SignatureFile, maximumControlBytes)
	if err != nil {
		return fmt.Errorf("read audit export signature: %w", err)
	}
	defer clear(signatureBytes)
	signature, err := parseSignature(signatureBytes)
	if err != nil {
		return err
	}
	trust, canonicalTrust, err := readTrustProfile(request.TrustProfileFile)
	if err != nil {
		return err
	}
	defer clear(canonicalTrust)
	trustDigest := sha256.Sum256(canonicalTrust)
	if err := verifySignature(export, kind, digestString(trustDigest), signature, trust); err != nil {
		return err
	}
	exportDigest := sha256.Sum256(export)
	envelope := Envelope{
		Contract:           EnvelopeContract,
		ExportKind:         kind,
		ExportSHA256:       digestString(exportDigest),
		TrustProfileSHA256: digestString(trustDigest),
		Export:             append(json.RawMessage(nil), bytes.TrimSuffix(export, []byte{'\n'})...),
		Signature:          signature,
	}
	encoded, err := canonicalJSON(envelope)
	if err != nil {
		return errors.New("encode audit export envelope")
	}
	defer clear(encoded)
	if err := installNewPrivateFile(request.OutputFile, encoded, maximumExportBytes+maximumControlBytes); err != nil {
		return fmt.Errorf("install audit export envelope: %w", err)
	}
	installed, err := VerifyFile(request.OutputFile, request.TrustProfileFile)
	if err != nil {
		return fmt.Errorf("verify installed audit export envelope: %w", err)
	}
	if installed.ExportSHA256 != envelope.ExportSHA256 || installed.Signature.KeyID != signature.KeyID {
		return errors.New("installed audit export envelope does not match request")
	}
	return nil
}

func VerifyFile(envelopeFile string, trustProfileFile string) (Envelope, error) {
	var envelope Envelope
	if !distinctPaths(envelopeFile, trustProfileFile) {
		return envelope, errors.New("audit export verification paths are invalid")
	}
	encoded, err := readStablePrivateFile(envelopeFile, maximumExportBytes+maximumControlBytes)
	if err != nil {
		return envelope, fmt.Errorf("read audit export envelope: %w", err)
	}
	defer clear(encoded)
	if err := decodeCanonicalJSON(encoded, &envelope, maximumExportBytes+maximumControlBytes); err != nil {
		return Envelope{}, errors.New("audit export envelope is invalid")
	}
	canonicalEnvelope, err := canonicalJSON(envelope)
	if err != nil || !bytes.Equal(canonicalEnvelope, encoded) {
		clear(canonicalEnvelope)
		return Envelope{}, errors.New("audit export envelope is not canonical")
	}
	clear(canonicalEnvelope)
	if envelope.Contract != EnvelopeContract || !validExportKind(envelope.ExportKind) ||
		!digestPattern.MatchString(envelope.ExportSHA256) ||
		!digestPattern.MatchString(envelope.TrustProfileSHA256) {
		return Envelope{}, errors.New("audit export envelope fields are invalid")
	}
	export := append([]byte(nil), envelope.Export...)
	export = append(export, '\n')
	defer clear(export)
	kind, err := validateExport(export)
	if err != nil || kind != envelope.ExportKind {
		return Envelope{}, errors.New("audit export envelope content is invalid")
	}
	exportDigest := sha256.Sum256(export)
	if envelope.ExportSHA256 != digestString(exportDigest) {
		return Envelope{}, errors.New("audit export envelope digest does not match")
	}
	trust, canonicalTrust, err := readTrustProfile(trustProfileFile)
	if err != nil {
		return Envelope{}, err
	}
	defer clear(canonicalTrust)
	trustDigest := sha256.Sum256(canonicalTrust)
	if envelope.TrustProfileSHA256 != digestString(trustDigest) {
		return Envelope{}, errors.New("audit export trust profile digest does not match")
	}
	if err := validateSignature(envelope.Signature); err != nil {
		return Envelope{}, err
	}
	if err := verifySignature(export, kind, envelope.TrustProfileSHA256, envelope.Signature, trust); err != nil {
		return Envelope{}, err
	}
	envelope.Export = append(json.RawMessage(nil), envelope.Export...)
	return envelope, nil
}

func readAndValidateExport(path string) ([]byte, string, error) {
	encoded, err := readStablePrivateFile(path, maximumExportBytes)
	if err != nil {
		return nil, "", fmt.Errorf("read audit export: %w", err)
	}
	kind, err := validateExport(encoded)
	if err != nil {
		clear(encoded)
		return nil, "", err
	}
	return encoded, kind, nil
}

func validateExport(encoded []byte) (string, error) {
	if err := requireUniqueJSON(encoded, maximumExportBytes); err != nil {
		return "", errors.New("audit export is invalid")
	}
	var header struct {
		Content struct {
			SchemaVersion string `json:"schemaVersion"`
		} `json:"content"`
	}
	if err := json.Unmarshal(encoded, &header); err != nil {
		return "", errors.New("audit export is invalid")
	}
	switch header.Content.SchemaVersion {
	case persistence.AuthorizationAuditExportSchema:
		var document persistence.AuthorizationAuditExportDocument
		if err := decodeCanonicalJSON(encoded, &document, maximumExportBytes); err != nil {
			return "", errors.New("authorization audit export is invalid")
		}
		canonical, err := canonicalJSON(document)
		if err != nil || !bytes.Equal(canonical, encoded) {
			clear(canonical)
			return "", errors.New("authorization audit export is not canonical")
		}
		clear(canonical)
		if err := persistence.ValidateAuthorizationAuditExportDocument(document); err != nil {
			return "", errors.New("authorization audit export contract is invalid")
		}
		return AuthorizationExportKind, nil
	case persistence.OperatorAuditExportSchema:
		var document persistence.OperatorAuditExportDocument
		if err := decodeCanonicalJSON(encoded, &document, maximumExportBytes); err != nil {
			return "", errors.New("operator audit export is invalid")
		}
		canonical, err := canonicalJSON(document)
		if err != nil || !bytes.Equal(canonical, encoded) {
			clear(canonical)
			return "", errors.New("operator audit export is not canonical")
		}
		clear(canonical)
		if err := persistence.ValidateOperatorAuditExportDocument(document); err != nil {
			return "", errors.New("operator audit export contract is invalid")
		}
		return OperatorExportKind, nil
	default:
		return "", errors.New("audit export contract is unsupported")
	}
}

func readTrustProfile(path string) (TrustProfile, []byte, error) {
	var trust TrustProfile
	encoded, err := readStablePrivateFile(path, maximumControlBytes)
	if err != nil {
		return trust, nil, fmt.Errorf("read audit export trust profile: %w", err)
	}
	defer clear(encoded)
	if err := decodeCanonicalJSON(encoded, &trust, maximumControlBytes); err != nil {
		return TrustProfile{}, nil, errors.New("audit export trust profile is invalid")
	}
	canonical, err := canonicalJSON(trust)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return TrustProfile{}, nil, errors.New("audit export trust profile is not canonical")
	}
	if err := validateTrustProfile(trust); err != nil {
		clear(canonical)
		return TrustProfile{}, nil, err
	}
	return trust, canonical, nil
}

func parseSignature(encoded []byte) (Signature, error) {
	var signature Signature
	if err := decodeCanonicalJSON(encoded, &signature, maximumControlBytes); err != nil {
		return signature, errors.New("audit export signature is invalid")
	}
	canonical, err := canonicalJSON(signature)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return Signature{}, errors.New("audit export signature is not canonical")
	}
	clear(canonical)
	if err := validateSignature(signature); err != nil {
		return Signature{}, err
	}
	return signature, nil
}

func validateSignature(signature Signature) error {
	decoded, err := base64.RawURLEncoding.DecodeString(signature.Signature)
	if signature.Contract != SignatureContract || !keyIDPattern.MatchString(signature.KeyID) ||
		err != nil || len(decoded) != ed25519.SignatureSize ||
		base64.RawURLEncoding.EncodeToString(decoded) != signature.Signature {
		clear(decoded)
		return errors.New("audit export signature fields are invalid")
	}
	clear(decoded)
	return nil
}

func validateTrustProfile(trust TrustProfile) error {
	if trust.Contract != TrustContract || len(trust.Keys) == 0 || len(trust.Keys) > 8 ||
		!sort.SliceIsSorted(trust.Keys, func(left, right int) bool {
			return trust.Keys[left].KeyID < trust.Keys[right].KeyID
		}) {
		return errors.New("audit export trust profile fields are invalid")
	}
	previous := ""
	for _, key := range trust.Keys {
		decoded, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
		valid := keyIDPattern.MatchString(key.KeyID) && key.KeyID != previous && err == nil &&
			len(decoded) == ed25519.PublicKeySize &&
			base64.RawURLEncoding.EncodeToString(decoded) == key.PublicKey
		clear(decoded)
		if !valid {
			return errors.New("audit export trust profile key is invalid")
		}
		previous = key.KeyID
	}
	return nil
}

func verifySignature(
	export []byte,
	kind string,
	trustDigest string,
	signature Signature,
	trust TrustProfile,
) error {
	if err := validateSignature(signature); err != nil {
		return err
	}
	index := sort.Search(len(trust.Keys), func(index int) bool {
		return trust.Keys[index].KeyID >= signature.KeyID
	})
	if index >= len(trust.Keys) || trust.Keys[index].KeyID != signature.KeyID {
		return errors.New("audit export signing key is not trusted")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(trust.Keys[index].PublicKey)
	if err != nil {
		return errors.New("audit export signing key is invalid")
	}
	defer clear(publicKey)
	encodedSignature, err := base64.RawURLEncoding.DecodeString(signature.Signature)
	if err != nil {
		return errors.New("audit export signature is invalid")
	}
	defer clear(encodedSignature)
	message := signingMessage(export, kind, trustDigest)
	defer clear(message)
	if !ed25519.Verify(ed25519.PublicKey(publicKey), message, encodedSignature) {
		return errors.New("audit export signature does not verify")
	}
	return nil
}

func signingMessage(export []byte, kind string, trustDigest string) []byte {
	message := make([]byte, 0, len(signatureDomain)+len(kind)+len(trustDigest)+len(export)+2)
	message = append(message, signatureDomain...)
	message = append(message, kind...)
	message = append(message, '\n')
	message = append(message, trustDigest...)
	message = append(message, '\n')
	message = append(message, export...)
	return message
}

func digestString(digest [sha256.Size]byte) string {
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validExportKind(kind string) bool {
	return kind == AuthorizationExportKind || kind == OperatorExportKind
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func decodeCanonicalJSON(encoded []byte, target any, maximumBytes int64) error {
	if err := requireUniqueJSON(encoded, maximumBytes); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON has trailing data")
	}
	return nil
}

func requireUniqueJSON(encoded []byte, maximumBytes int64) error {
	if len(encoded) == 0 || int64(len(encoded)) > maximumBytes || !bytes.HasSuffix(encoded, []byte{'\n'}) {
		return errors.New("JSON size or terminator is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON has trailing data")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maximumJSONDepth {
		return errors.New("JSON is too deeply nested")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is invalid")
			}
			if _, exists := seen[key]; exists {
				return errors.New("JSON contains a duplicate member")
			}
			seen[key] = struct{}{}
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("JSON delimiter is invalid")
	}
	closing, err := decoder.Token()
	if err != nil || (delimiter == '{' && closing != json.Delim('}')) ||
		(delimiter == '[' && closing != json.Delim(']')) {
		return errors.New("JSON delimiter is unbalanced")
	}
	return nil
}

func readStablePrivateFile(path string, maximumBytes int64) ([]byte, error) {
	if !canonicalAbsolutePath(path) || maximumBytes <= 0 {
		return nil, errors.New("file path is invalid")
	}
	resolvedBefore, err := filepath.EvalSymlinks(path)
	if err != nil || resolvedBefore != path {
		return nil, errors.New("file path is not stable")
	}
	directoryPath := filepath.Dir(path)
	directoryPathInfo, err := os.Lstat(directoryPath)
	if err != nil || !safePrivateDirectory(directoryPathInfo) {
		return nil, errors.New("file directory is invalid")
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		return nil, errors.New("file directory is unavailable")
	}
	defer directory.Close()
	directoryBefore, err := directory.Stat()
	if err != nil || !os.SameFile(directoryPathInfo, directoryBefore) ||
		!safePrivateDirectory(directoryBefore) {
		return nil, errors.New("file directory changed before reading")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !safePrivateFile(pathInfo, maximumBytes) {
		return nil, errors.New("file is invalid")
	}
	descriptor, err := syscall.Openat(
		int(directory.Fd()),
		filepath.Base(path),
		syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, errors.New("file is unavailable")
	}
	file := os.NewFile(uintptr(descriptor), filepath.Base(path))
	if file == nil {
		syscall.Close(descriptor)
		return nil, errors.New("file descriptor is invalid")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !sameOwner(pathInfo, before) || !os.SameFile(pathInfo, before) ||
		!safePrivateFile(before, maximumBytes) {
		return nil, errors.New("file changed before reading")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil || len(content) == 0 || int64(len(content)) > maximumBytes || int64(len(content)) != before.Size() {
		clear(content)
		return nil, errors.New("file content is invalid")
	}
	after, err := file.Stat()
	if err != nil || !sameFileState(before, after) {
		clear(content)
		return nil, errors.New("file changed while reading")
	}
	pathAfter, err := os.Lstat(path)
	if err != nil || !sameFileState(after, pathAfter) {
		clear(content)
		return nil, errors.New("file path changed while reading")
	}
	resolvedAfter, err := filepath.EvalSymlinks(path)
	if err != nil || resolvedAfter != resolvedBefore {
		clear(content)
		return nil, errors.New("file path changed while reading")
	}
	directoryAfter, err := directory.Stat()
	directoryPathAfter, pathErr := os.Lstat(directoryPath)
	if err != nil || pathErr != nil || !sameDirectoryIdentity(directoryBefore, directoryAfter) ||
		!sameDirectoryIdentity(directoryAfter, directoryPathAfter) {
		clear(content)
		return nil, errors.New("file directory changed while reading")
	}
	return content, nil
}

func installNewPrivateFile(path string, content []byte, maximumBytes int64) error {
	if !canonicalAbsolutePath(path) || len(content) == 0 || int64(len(content)) > maximumBytes {
		return errors.New("output path is invalid")
	}
	directory := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return errors.New("output directory path is invalid")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil || !safePrivateDirectory(directoryInfo) {
		return errors.New("output directory is invalid")
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return errors.New("output directory is unavailable")
	}
	defer directoryHandle.Close()
	directoryBefore, err := directoryHandle.Stat()
	if err != nil || !os.SameFile(directoryInfo, directoryBefore) || !safePrivateDirectory(directoryBefore) {
		return errors.New("output directory changed before install")
	}
	temporary, err := os.CreateTemp(directory, ".dataground-audit-export-*")
	if err != nil {
		return errors.New("create temporary audit export file")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return errors.New("secure temporary audit export file")
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return errors.New("write temporary audit export file")
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return errors.New("sync temporary audit export file")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close temporary audit export file")
	}
	resolvedAfter, err := filepath.EvalSymlinks(directory)
	if err != nil || resolvedAfter != resolved {
		return errors.New("output directory changed before install")
	}
	if err := os.Link(temporaryPath, path); err != nil {
		existing, readErr := readStablePrivateFile(path, maximumBytes)
		if readErr != nil {
			return errors.New("audit export file already exists or cannot be installed")
		}
		defer clear(existing)
		if !bytes.Equal(existing, content) {
			return errors.New("audit export file conflicts with existing file")
		}
		return syncStableDirectory(directoryHandle, directoryBefore, directory)
	}
	directoryAfter, err := directoryHandle.Stat()
	directoryPathAfter, pathErr := os.Lstat(directory)
	if err != nil || pathErr != nil || !sameDirectoryIdentity(directoryBefore, directoryAfter) ||
		!sameDirectoryIdentity(directoryAfter, directoryPathAfter) {
		return errors.New("output directory changed during install")
	}
	installed, err := readStablePrivateFile(path, maximumBytes)
	if err != nil || !bytes.Equal(installed, content) {
		clear(installed)
		return errors.New("installed audit export file does not match")
	}
	clear(installed)
	return syncStableDirectory(directoryHandle, directoryBefore, directory)
}

func distinctPaths(paths ...string) bool {
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if !canonicalAbsolutePath(path) {
			return false
		}
		if _, exists := seen[path]; exists {
			return false
		}
		seen[path] = struct{}{}
	}
	return true
}

func canonicalAbsolutePath(path string) bool {
	return path != "" && strings.IndexByte(path, 0) < 0 && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func safePrivateFile(info os.FileInfo, maximumBytes int64) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0 &&
		info.Size() > 0 && info.Size() <= maximumBytes && ownedByProcess(info)
}

func safePrivateDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm()&0o077 == 0 && ownedByProcess(info)
}

func ownedByProcess(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func sameOwner(left, right os.FileInfo) bool {
	leftStat, leftOK := left.Sys().(*syscall.Stat_t)
	rightStat, rightOK := right.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && leftStat.Uid == rightStat.Uid
}

func sameFileState(left, right os.FileInfo) bool {
	return os.SameFile(left, right) && sameOwner(left, right) && left.Size() == right.Size() &&
		left.Mode() == right.Mode() && left.ModTime().Equal(right.ModTime())
}

func sameDirectoryIdentity(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.IsDir() && right.IsDir() &&
		os.SameFile(left, right) && sameOwner(left, right) && left.Mode() == right.Mode()
}

func syncStableDirectory(directory *os.File, before os.FileInfo, path string) error {
	if directory == nil || before == nil {
		return errors.New("audit export directory is invalid")
	}
	current, err := directory.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !sameDirectoryIdentity(before, current) ||
		!sameDirectoryIdentity(current, pathInfo) {
		return errors.New("audit export directory changed before sync")
	}
	if err := directory.Sync(); err != nil {
		return errors.New("sync audit export directory")
	}
	after, err := directory.Stat()
	pathAfter, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !sameDirectoryIdentity(current, after) ||
		!sameDirectoryIdentity(after, pathAfter) {
		return errors.New("audit export directory changed during sync")
	}
	return nil
}
