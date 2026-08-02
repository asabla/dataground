package releasecert

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	StatementContract = "dataground.release-certification/oidc-loopback/v1"
	SignatureContract = "dataground.release-certification-signature/ed25519/v1"
	TrustContract     = "dataground.release-certification-trust/ed25519/v1"
	EnvelopeContract  = "dataground.release-certification-envelope/v1"

	maximumInputBytes    = 1 << 20
	maximumEvidenceBytes = 4 << 20
	maximumJSONDepth     = 16
	maximumValidity      = 31 * 24 * time.Hour
	maximumClockSkew     = 5 * time.Minute
	signatureDomain      = "DataGround release certification oidc-loopback v1\n"
)

var (
	idPattern        = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,63}$`)
	revisionPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	goVersionPattern = regexp.MustCompile(`^go[0-9]+\.[0-9]+(?:\.[0-9]+)?$`)
)

type Artifact struct {
	Kind   string `json:"kind"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

type Statement struct {
	Contract           string     `json:"contract"`
	ReleaseID          string     `json:"releaseId"`
	SourceRevision     string     `json:"sourceRevision"`
	GoVersion          string     `json:"goVersion"`
	DeploymentProfile  string     `json:"deploymentProfile"`
	TrustProfileSHA256 string     `json:"trustProfileSha256"`
	IssuedAt           string     `json:"issuedAt"`
	ExpiresAt          string     `json:"expiresAt"`
	ReviewerID         string     `json:"reviewerId"`
	Reason             string     `json:"reason"`
	Artifacts          []Artifact `json:"artifacts"`
}

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
	Contract        string    `json:"contract"`
	StatementSHA256 string    `json:"statementSha256"`
	Statement       Statement `json:"statement"`
	Signature       Signature `json:"signature"`
}

type InstallRequest struct {
	StatementFile    string
	SignatureFile    string
	TrustProfileFile string
	OutputFile       string
	SourceRevision   string
	GoVersion        string
	Now              time.Time
}

type PrepareRequest struct {
	StatementFile      string
	TrustProfileFile   string
	SigningMessageFile string
	SourceRevision     string
	GoVersion          string
	Now                time.Time
}

func CurrentBuild() (string, string, error) {
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return "", "", errors.New("release certification requires source revision metadata")
	}
	var revision, modified string
	var revisionSet, modifiedSet bool
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			if revisionSet {
				return "", "", errors.New("release certification requires unambiguous source revision metadata")
			}
			revision, revisionSet = setting.Value, true
		case "vcs.modified":
			if modifiedSet {
				return "", "", errors.New("release certification requires unambiguous source revision metadata")
			}
			modified, modifiedSet = setting.Value, true
		}
	}
	if !revisionPattern.MatchString(revision) || modified != "false" {
		return "", "", errors.New("release certification requires a clean source revision")
	}
	goVersion := runtime.Version()
	if !goVersionPattern.MatchString(goVersion) {
		return "", "", errors.New("release certification requires a released Go runtime")
	}
	return revision, goVersion, nil
}

func PrepareSigningMessage(request PrepareRequest) error {
	if request.Now.IsZero() {
		request.Now = time.Now().UTC()
	}
	if !revisionPattern.MatchString(request.SourceRevision) || !goVersionPattern.MatchString(request.GoVersion) {
		return errors.New("release certification source revision is invalid")
	}
	statementBytes, err := readStablePrivateFile(request.StatementFile, maximumInputBytes)
	if err != nil {
		return fmt.Errorf("read certification statement: %w", err)
	}
	defer clear(statementBytes)
	trustBytes, err := readStablePrivateFile(request.TrustProfileFile, maximumInputBytes)
	if err != nil {
		return fmt.Errorf("read certification trust profile: %w", err)
	}
	defer clear(trustBytes)
	statement, canonicalStatement, err := parseStatement(
		statementBytes,
		request.SourceRevision,
		request.GoVersion,
		request.Now,
	)
	if err != nil {
		return err
	}
	defer clear(canonicalStatement)
	_, canonicalTrust, err := parseTrustProfile(trustBytes)
	if err != nil {
		return err
	}
	defer clear(canonicalTrust)
	trustDigest := sha256.Sum256(canonicalTrust)
	if !equalDigest(statement.TrustProfileSHA256, trustDigest[:]) {
		return errors.New("release certification trust profile digest does not match")
	}
	if err := verifyArtifacts(statement); err != nil {
		return err
	}
	message := signatureMessage(canonicalStatement)
	defer clear(message)
	if err := installNewFile(request.SigningMessageFile, message); err != nil {
		return fmt.Errorf("install release certification signing message: %w", err)
	}
	return nil
}

func Install(request InstallRequest) error {
	if request.Now.IsZero() {
		request.Now = time.Now().UTC()
	}
	if !revisionPattern.MatchString(request.SourceRevision) || !goVersionPattern.MatchString(request.GoVersion) {
		return errors.New("release certification source revision is invalid")
	}
	statementBytes, err := readStablePrivateFile(request.StatementFile, maximumInputBytes)
	if err != nil {
		return fmt.Errorf("read certification statement: %w", err)
	}
	defer clear(statementBytes)
	signatureBytes, err := readStablePrivateFile(request.SignatureFile, maximumInputBytes)
	if err != nil {
		return fmt.Errorf("read certification signature: %w", err)
	}
	defer clear(signatureBytes)
	trustBytes, err := readStablePrivateFile(request.TrustProfileFile, maximumInputBytes)
	if err != nil {
		return fmt.Errorf("read certification trust profile: %w", err)
	}
	defer clear(trustBytes)

	statement, canonicalStatement, err := parseStatement(
		statementBytes,
		request.SourceRevision,
		request.GoVersion,
		request.Now,
	)
	if err != nil {
		return err
	}
	defer clear(canonicalStatement)
	signature, err := parseSignature(signatureBytes)
	if err != nil {
		return err
	}
	trust, canonicalTrust, err := parseTrustProfile(trustBytes)
	if err != nil {
		return err
	}
	defer clear(canonicalTrust)
	trustDigest := sha256.Sum256(canonicalTrust)
	if !equalDigest(statement.TrustProfileSHA256, trustDigest[:]) {
		return errors.New("release certification trust profile digest does not match")
	}
	if err := verifySignature(canonicalStatement, signature, trust); err != nil {
		return err
	}
	if err := verifyArtifacts(statement); err != nil {
		return err
	}
	statementDigest := sha256.Sum256(canonicalStatement)
	envelope := Envelope{
		Contract:        EnvelopeContract,
		StatementSHA256: hex.EncodeToString(statementDigest[:]),
		Statement:       statement,
		Signature:       signature,
	}
	encoded, err := canonicalJSON(envelope)
	if err != nil {
		return errors.New("encode release certification envelope")
	}
	defer clear(encoded)
	if err := installNewFile(request.OutputFile, encoded); err != nil {
		return fmt.Errorf("install release certification: %w", err)
	}
	installed, err := VerifyFile(
		request.OutputFile,
		request.TrustProfileFile,
		request.SourceRevision,
		request.GoVersion,
		request.Now,
	)
	if err != nil {
		return fmt.Errorf("verify installed release certification: %w", err)
	}
	if installed.StatementSHA256 != envelope.StatementSHA256 || installed.Signature.KeyID != signature.KeyID {
		return errors.New("installed release certification does not match request")
	}
	return nil
}

func VerifyFile(
	envelopeFile string,
	trustProfileFile string,
	sourceRevision string,
	goVersion string,
	now time.Time,
) (Envelope, error) {
	var envelope Envelope
	if now.IsZero() {
		now = time.Now().UTC()
	}
	encoded, err := readStablePrivateFile(envelopeFile, maximumInputBytes)
	if err != nil {
		return envelope, err
	}
	defer clear(encoded)
	if err := decodeCanonicalJSON(encoded, &envelope); err != nil {
		return envelope, errors.New("release certification envelope is invalid")
	}
	if envelope.Contract != EnvelopeContract {
		return envelope, errors.New("release certification envelope contract is invalid")
	}
	statementBytes, err := canonicalJSON(envelope.Statement)
	if err != nil {
		return envelope, errors.New("release certification statement is invalid")
	}
	defer clear(statementBytes)
	statement, _, err := parseStatement(statementBytes, sourceRevision, goVersion, now)
	if err != nil {
		return envelope, err
	}
	envelope.Statement = statement
	digest := sha256.Sum256(statementBytes)
	if !equalDigest(envelope.StatementSHA256, digest[:]) {
		return envelope, errors.New("release certification statement digest does not match")
	}
	trustBytes, err := readStablePrivateFile(trustProfileFile, maximumInputBytes)
	if err != nil {
		return envelope, err
	}
	defer clear(trustBytes)
	trust, canonicalTrust, err := parseTrustProfile(trustBytes)
	if err != nil {
		return envelope, err
	}
	defer clear(canonicalTrust)
	trustDigest := sha256.Sum256(canonicalTrust)
	if !equalDigest(statement.TrustProfileSHA256, trustDigest[:]) {
		return envelope, errors.New("release certification trust profile digest does not match")
	}
	if err := validateSignature(envelope.Signature); err != nil {
		return envelope, err
	}
	if err := verifySignature(statementBytes, envelope.Signature, trust); err != nil {
		return envelope, err
	}
	if err := verifyArtifacts(statement); err != nil {
		return envelope, err
	}
	return envelope, nil
}

func parseStatement(
	encoded []byte,
	sourceRevision string,
	goVersion string,
	now time.Time,
) (Statement, []byte, error) {
	var statement Statement
	if err := decodeCanonicalJSON(encoded, &statement); err != nil {
		return statement, nil, errors.New("release certification statement is invalid")
	}
	canonical, err := canonicalJSON(statement)
	if err != nil || !bytes.Equal(encoded, canonical) {
		clear(canonical)
		return statement, nil, errors.New("release certification statement is not canonical")
	}
	if statement.Contract != StatementContract || !idPattern.MatchString(statement.ReleaseID) ||
		!revisionPattern.MatchString(statement.SourceRevision) || statement.SourceRevision != sourceRevision ||
		!goVersionPattern.MatchString(statement.GoVersion) || statement.GoVersion != goVersion ||
		!idPattern.MatchString(statement.DeploymentProfile) || !digestPattern.MatchString(statement.TrustProfileSHA256) ||
		!idPattern.MatchString(statement.ReviewerID) || !validReason(statement.Reason) {
		clear(canonical)
		return statement, nil, errors.New("release certification statement fields are invalid")
	}
	issuedAt, err := parseCanonicalTime(statement.IssuedAt)
	if err != nil {
		clear(canonical)
		return statement, nil, errors.New("release certification issue time is invalid")
	}
	expiresAt, err := parseCanonicalTime(statement.ExpiresAt)
	if err != nil || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > maximumValidity ||
		issuedAt.After(now.Add(maximumClockSkew)) || !expiresAt.After(now) {
		clear(canonical)
		return statement, nil, errors.New("release certification validity is invalid")
	}
	if err := validateArtifacts(statement.Artifacts); err != nil {
		clear(canonical)
		return statement, nil, err
	}
	return statement, canonical, nil
}

func parseSignature(encoded []byte) (Signature, error) {
	var signature Signature
	if err := decodeCanonicalJSON(encoded, &signature); err != nil {
		return signature, errors.New("release certification signature is invalid")
	}
	canonical, err := canonicalJSON(signature)
	defer clear(canonical)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return signature, errors.New("release certification signature is not canonical")
	}
	if err := validateSignature(signature); err != nil {
		return signature, err
	}
	return signature, nil
}

func validateSignature(signature Signature) error {
	decoded, err := base64.RawURLEncoding.DecodeString(signature.Signature)
	if signature.Contract != SignatureContract || !idPattern.MatchString(signature.KeyID) ||
		err != nil || len(decoded) != ed25519.SignatureSize ||
		base64.RawURLEncoding.EncodeToString(decoded) != signature.Signature {
		return errors.New("release certification signature fields are invalid")
	}
	return nil
}

func parseTrustProfile(encoded []byte) (TrustProfile, []byte, error) {
	var trust TrustProfile
	if err := decodeCanonicalJSON(encoded, &trust); err != nil {
		return trust, nil, errors.New("release certification trust profile is invalid")
	}
	canonical, err := canonicalJSON(trust)
	if err != nil || !bytes.Equal(encoded, canonical) {
		clear(canonical)
		return trust, nil, errors.New("release certification trust profile is not canonical")
	}
	if trust.Contract != TrustContract || len(trust.Keys) == 0 || len(trust.Keys) > 8 {
		clear(canonical)
		return trust, nil, errors.New("release certification trust profile fields are invalid")
	}
	previous := ""
	for _, key := range trust.Keys {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(key.PublicKey)
		if !idPattern.MatchString(key.KeyID) || key.KeyID <= previous || decodeErr != nil ||
			len(decoded) != ed25519.PublicKeySize ||
			base64.RawURLEncoding.EncodeToString(decoded) != key.PublicKey {
			clear(canonical)
			return trust, nil, errors.New("release certification trust key is invalid")
		}
		previous = key.KeyID
	}
	return trust, canonical, nil
}

func verifySignature(statement []byte, signature Signature, trust TrustProfile) error {
	encoded, err := base64.RawURLEncoding.DecodeString(signature.Signature)
	if err != nil {
		return errors.New("release certification signature is invalid")
	}
	index := sort.Search(len(trust.Keys), func(index int) bool { return trust.Keys[index].KeyID >= signature.KeyID })
	if index >= len(trust.Keys) || trust.Keys[index].KeyID != signature.KeyID {
		return errors.New("release certification signing key is not trusted")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(trust.Keys[index].PublicKey)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), signatureMessage(statement), encoded) {
		return errors.New("release certification signature does not verify")
	}
	return nil
}

func signatureMessage(statement []byte) []byte {
	message := make([]byte, 0, len(signatureDomain)+len(statement))
	message = append(message, signatureDomain...)
	return append(message, statement...)
}

func validateArtifacts(artifacts []Artifact) error {
	required := []string{"admission-capacity-evidence", "api-authorization-policy", "oidc-security-configuration"}
	if len(artifacts) != len(required) {
		return errors.New("release certification artifacts are incomplete")
	}
	paths := make(map[string]struct{}, len(artifacts))
	for index, artifact := range artifacts {
		if artifact.Kind != required[index] || !canonicalAbsolutePath(artifact.File) ||
			!digestPattern.MatchString(artifact.SHA256) {
			return errors.New("release certification artifact is invalid")
		}
		if _, exists := paths[artifact.File]; exists {
			return errors.New("release certification artifact paths are not distinct")
		}
		paths[artifact.File] = struct{}{}
	}
	return nil
}

func verifyArtifacts(statement Statement) error {
	contents := make(map[string][]byte, len(statement.Artifacts))
	artifacts := make(map[string]Artifact, len(statement.Artifacts))
	defer func() {
		for _, content := range contents {
			clear(content)
		}
	}()
	for _, artifact := range statement.Artifacts {
		encoded, err := readStablePrivateFile(artifact.File, maximumEvidenceBytes)
		if err != nil {
			return errors.New("release certification artifact is unavailable")
		}
		digest := sha256.Sum256(encoded)
		if !equalDigest(artifact.SHA256, digest[:]) {
			clear(encoded)
			return errors.New("release certification artifact digest does not match")
		}
		contents[artifact.Kind] = encoded
		artifacts[artifact.Kind] = artifact
	}
	var evidence struct {
		Contract          string `json:"contract"`
		SourceRevision    string `json:"sourceRevision"`
		GoVersion         string `json:"goVersion"`
		DeploymentProfile string `json:"deploymentProfile"`
		Accepted          bool   `json:"accepted"`
	}
	if err := decodeArtifactJSON(contents["admission-capacity-evidence"], &evidence); err != nil ||
		evidence.Contract != "dataground.authentication-rate-limit-capacity-evidence/v2" ||
		evidence.SourceRevision != statement.SourceRevision ||
		evidence.GoVersion != statement.GoVersion ||
		evidence.DeploymentProfile != statement.DeploymentProfile || !evidence.Accepted {
		return errors.New("release certification admission evidence is invalid")
	}
	var configuration struct {
		Contract  string `json:"contract"`
		Admission struct {
			DeploymentProfile    string `json:"deploymentProfile"`
			CapacityEvidenceFile string `json:"capacityEvidenceFile"`
			CapacityEvidenceHash string `json:"capacityEvidenceSha256"`
		} `json:"admission"`
		Authorization struct {
			PolicyFile string `json:"policyFile"`
		} `json:"authorization"`
	}
	capacity := artifacts["admission-capacity-evidence"]
	policy := artifacts["api-authorization-policy"]
	if err := decodeArtifactJSON(contents["oidc-security-configuration"], &configuration); err != nil ||
		configuration.Contract != "dataground.api-security/oidc-dpop/v3" ||
		configuration.Admission.DeploymentProfile != statement.DeploymentProfile ||
		configuration.Admission.CapacityEvidenceFile != capacity.File ||
		configuration.Admission.CapacityEvidenceHash != capacity.SHA256 ||
		configuration.Authorization.PolicyFile != policy.File {
		return errors.New("release certification OIDC configuration binding is invalid")
	}
	return nil
}

func decodeArtifactJSON(encoded []byte, target any) error {
	if len(encoded) == 0 || len(encoded) > maximumEvidenceBytes {
		return errors.New("artifact JSON size is invalid")
	}
	if err := requireUniqueJSON(encoded); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("artifact JSON has trailing data")
	}
	return nil
}

func validReason(reason string) bool {
	if len(reason) < 8 || len(reason) > 512 || strings.TrimSpace(reason) != reason {
		return false
	}
	for _, character := range reason {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func equalDigest(encoded string, expected []byte) bool {
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == sha256.Size && subtle.ConstantTimeCompare(decoded, expected) == 1
}

func parseCanonicalTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("time is not canonical UTC")
	}
	return parsed, nil
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func decodeCanonicalJSON(encoded []byte, target any) error {
	if len(encoded) == 0 || len(encoded) > maximumInputBytes || !bytes.HasSuffix(encoded, []byte{'\n'}) {
		return errors.New("JSON size or terminator is invalid")
	}
	if err := requireUniqueJSON(encoded); err != nil {
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

func requireUniqueJSON(encoded []byte) error {
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
	pathInfo, err := os.Lstat(path)
	if err != nil || !safePrivateFile(pathInfo, maximumBytes) {
		return nil, errors.New("file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("file is unavailable")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, before) || !safePrivateFile(before, maximumBytes) {
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
	return content, nil
}

func sameFileState(left, right os.FileInfo) bool {
	return os.SameFile(left, right) && left.Size() == right.Size() && left.Mode() == right.Mode() &&
		left.ModTime().Equal(right.ModTime())
}

func safePrivateFile(info os.FileInfo, maximumBytes int64) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0 &&
		info.Size() > 0 && info.Size() <= maximumBytes
}

func canonicalAbsolutePath(path string) bool {
	return path != "" && strings.IndexByte(path, 0) < 0 && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func installNewFile(path string, content []byte) error {
	if !canonicalAbsolutePath(path) || len(content) == 0 || len(content) > maximumInputBytes {
		return errors.New("output path is invalid")
	}
	directory := filepath.Dir(path)
	directoryInfo, err := os.Lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode().Perm()&0o022 != 0 {
		return errors.New("output directory is invalid")
	}
	temporary, err := os.CreateTemp(directory, ".dataground-release-certification-*")
	if err != nil {
		return errors.New("create temporary certification")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return errors.New("secure temporary certification")
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return errors.New("write temporary certification")
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return errors.New("sync temporary certification")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close temporary certification")
	}
	if err := os.Link(temporaryPath, path); err != nil {
		existing, readErr := readStablePrivateFile(path, maximumInputBytes)
		if readErr != nil {
			return errors.New("release certification already exists or cannot be installed")
		}
		defer clear(existing)
		if !bytes.Equal(existing, content) {
			return errors.New("release certification conflicts with existing file")
		}
		return syncDirectory(directory)
	}
	installed, err := os.Lstat(path)
	if err != nil || !safePrivateFile(installed, maximumInputBytes) {
		return errors.New("installed release certification is invalid")
	}
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("open certification directory")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("sync certification directory")
	}
	return nil
}
