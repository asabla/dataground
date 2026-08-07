package releasecert

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"time"
)

const (
	ProviderDPoPIssuanceReportContract    = "dataground.provider-dpop-issuance-conformance/v1"
	ProviderDPoPIssuanceStatementContract = "dataground.provider-dpop-issuance-certification/v1"
	ProviderDPoPIssuanceSignatureContract = "dataground.provider-dpop-issuance-signature/ed25519/v1"
	ProviderDPoPIssuanceTrustContract     = "dataground.provider-dpop-issuance-trust/ed25519/v1"
	ProviderDPoPIssuanceEnvelopeContract  = "dataground.provider-dpop-issuance-envelope/v1"

	providerDPoPIssuanceMaximumValidity = 24 * time.Hour
	providerDPoPIssuanceMaximumAge      = time.Hour
	providerDPoPIssuanceClockSkew       = 5 * time.Minute
	providerDPoPIssuanceSignatureDomain = "DataGround provider DPoP issuance certification v1\n"
)

type ProviderDPoPIssuanceChecks struct {
	TokenEndpointProofAccepted         bool `json:"tokenEndpointProofAccepted"`
	TokenTypeDPoP                      bool `json:"tokenTypeDpop"`
	ConfirmationJKTMatched             bool `json:"confirmationJktMatched"`
	MissingTokenEndpointProofRejected  bool `json:"missingTokenEndpointProofRejected"`
	MismatchedTokenEndpointKeyRejected bool `json:"mismatchedTokenEndpointKeyRejected"`
	TokenEndpointProofReplayRejected   bool `json:"tokenEndpointProofReplayRejected"`
	WrongTokenEndpointMethodRejected   bool `json:"wrongTokenEndpointMethodRejected"`
	WrongTokenEndpointURIRejected      bool `json:"wrongTokenEndpointUriRejected"`
	StaleTokenEndpointProofRejected    bool `json:"staleTokenEndpointProofRejected"`
	ResourceProofAccepted              bool `json:"resourceProofAccepted"`
	MismatchedResourceKeyRejected      bool `json:"mismatchedResourceKeyRejected"`
	ResourceProofReplayRejected        bool `json:"resourceProofReplayRejected"`
	WrongResourceMethodRejected        bool `json:"wrongResourceMethodRejected"`
	WrongResourceURIRejected           bool `json:"wrongResourceUriRejected"`
	WrongAccessTokenHashRejected       bool `json:"wrongAccessTokenHashRejected"`
}

func (checks ProviderDPoPIssuanceChecks) Valid() bool {
	return checks.TokenEndpointProofAccepted && checks.TokenTypeDPoP &&
		checks.ConfirmationJKTMatched && checks.MissingTokenEndpointProofRejected &&
		checks.MismatchedTokenEndpointKeyRejected && checks.TokenEndpointProofReplayRejected &&
		checks.WrongTokenEndpointMethodRejected && checks.WrongTokenEndpointURIRejected &&
		checks.StaleTokenEndpointProofRejected &&
		checks.ResourceProofAccepted && checks.MismatchedResourceKeyRejected &&
		checks.ResourceProofReplayRejected && checks.WrongResourceMethodRejected &&
		checks.WrongResourceURIRejected && checks.WrongAccessTokenHashRejected
}

// ProviderDPoPIssuanceReport is the closed, non-secret result produced by a
// deployment-owned conformance run. DataGround validates and authenticates the
// result but never obtains provider credentials or acts as an authorization server.
type ProviderDPoPIssuanceReport struct {
	Contract                 string                     `json:"contract"`
	RunID                    string                     `json:"runId"`
	ProviderID               string                     `json:"providerId"`
	ProviderRegistrySHA256   string                     `json:"providerRegistrySha256"`
	Issuer                   string                     `json:"issuer"`
	TokenEndpoint            string                     `json:"tokenEndpoint"`
	Audience                 string                     `json:"audience"`
	GrantTypes               []string                   `json:"grantTypes"`
	DPoPAlgorithms           []string                   `json:"dpopAlgorithms"`
	AuthorizationServerNonce string                     `json:"authorizationServerNonce"`
	ObservedAt               string                     `json:"observedAt"`
	Checks                   ProviderDPoPIssuanceChecks `json:"checks"`
}

type ProviderDPoPIssuanceStatement struct {
	Contract                 string   `json:"contract"`
	CertificationID          string   `json:"certificationId"`
	ProviderID               string   `json:"providerId"`
	ProviderRegistrySHA256   string   `json:"providerRegistrySha256"`
	Issuer                   string   `json:"issuer"`
	TokenEndpoint            string   `json:"tokenEndpoint"`
	Audience                 string   `json:"audience"`
	GrantTypes               []string `json:"grantTypes"`
	DPoPAlgorithms           []string `json:"dpopAlgorithms"`
	AuthorizationServerNonce string   `json:"authorizationServerNonce"`
	ConformanceReportFile    string   `json:"conformanceReportFile"`
	ConformanceReportSHA256  string   `json:"conformanceReportSha256"`
	TrustProfileSHA256       string   `json:"trustProfileSha256"`
	IssuedAt                 string   `json:"issuedAt"`
	ExpiresAt                string   `json:"expiresAt"`
	ReviewerID               string   `json:"reviewerId"`
	Reason                   string   `json:"reason"`
}

type ProviderDPoPIssuanceSignature struct {
	Contract  string `json:"contract"`
	KeyID     string `json:"keyId"`
	Signature string `json:"signature"`
}

type ProviderDPoPIssuanceTrustProfile struct {
	Contract string       `json:"contract"`
	Keys     []TrustedKey `json:"keys"`
}

type ProviderDPoPIssuanceEnvelope struct {
	Contract        string                        `json:"contract"`
	StatementSHA256 string                        `json:"statementSha256"`
	Statement       ProviderDPoPIssuanceStatement `json:"statement"`
	Signature       ProviderDPoPIssuanceSignature `json:"signature"`
}

type ProviderDPoPIssuancePrepareRequest struct {
	StatementFile      string
	TrustProfileFile   string
	SigningMessageFile string
	Now                time.Time
}

type ProviderDPoPIssuanceInstallRequest struct {
	StatementFile    string
	SignatureFile    string
	TrustProfileFile string
	OutputFile       string
	Now              time.Time
}

func PrepareProviderDPoPIssuanceSigningMessage(request ProviderDPoPIssuancePrepareRequest) error {
	if request.Now.IsZero() {
		request.Now = time.Now().UTC()
	}
	statement, canonicalStatement, _, canonicalTrust, err := loadProviderDPoPIssuanceInputs(
		request.StatementFile, request.TrustProfileFile, request.Now,
	)
	if err != nil {
		return err
	}
	defer clear(canonicalStatement)
	defer clear(canonicalTrust)
	if err := verifyProviderDPoPIssuanceReport(statement, request.Now); err != nil {
		return err
	}
	message := providerDPoPIssuanceSignatureMessage(canonicalStatement)
	defer clear(message)
	if err := installNewFile(request.SigningMessageFile, message); err != nil {
		return fmt.Errorf("install provider DPoP issuance signing message: %w", err)
	}
	return nil
}

func InstallProviderDPoPIssuance(request ProviderDPoPIssuanceInstallRequest) error {
	if request.Now.IsZero() {
		request.Now = time.Now().UTC()
	}
	statement, canonicalStatement, trust, canonicalTrust, err := loadProviderDPoPIssuanceInputs(
		request.StatementFile, request.TrustProfileFile, request.Now,
	)
	if err != nil {
		return err
	}
	defer clear(canonicalStatement)
	defer clear(canonicalTrust)
	if err := verifyProviderDPoPIssuanceReport(statement, request.Now); err != nil {
		return err
	}
	signatureBytes, err := readStablePrivateFile(request.SignatureFile, maximumInputBytes)
	if err != nil {
		return fmt.Errorf("read provider DPoP issuance signature: %w", err)
	}
	defer clear(signatureBytes)
	signature, err := parseProviderDPoPIssuanceSignature(signatureBytes)
	if err != nil {
		return err
	}
	if err := verifyProviderDPoPIssuanceSignature(canonicalStatement, signature, trust); err != nil {
		return err
	}
	digest := sha256.Sum256(canonicalStatement)
	envelope := ProviderDPoPIssuanceEnvelope{
		Contract:        ProviderDPoPIssuanceEnvelopeContract,
		StatementSHA256: hex.EncodeToString(digest[:]),
		Statement:       statement,
		Signature:       signature,
	}
	encoded, err := canonicalJSON(envelope)
	if err != nil {
		return errors.New("encode provider DPoP issuance envelope")
	}
	defer clear(encoded)
	if err := installNewFile(request.OutputFile, encoded); err != nil {
		return fmt.Errorf("install provider DPoP issuance evidence: %w", err)
	}
	installed, err := VerifyProviderDPoPIssuanceFile(
		request.OutputFile, request.TrustProfileFile, request.Now,
	)
	if err != nil {
		return fmt.Errorf("verify installed provider DPoP issuance evidence: %w", err)
	}
	if installed.StatementSHA256 != envelope.StatementSHA256 || installed.Signature.KeyID != signature.KeyID {
		return errors.New("installed provider DPoP issuance evidence does not match request")
	}
	return nil
}

func VerifyProviderDPoPIssuanceFile(
	envelopeFile string,
	trustProfileFile string,
	now time.Time,
) (ProviderDPoPIssuanceEnvelope, error) {
	var envelope ProviderDPoPIssuanceEnvelope
	if now.IsZero() {
		now = time.Now().UTC()
	}
	encoded, err := readStablePrivateFile(envelopeFile, maximumInputBytes)
	if err != nil {
		return envelope, err
	}
	defer clear(encoded)
	if err := decodeCanonicalJSON(encoded, &envelope); err != nil ||
		envelope.Contract != ProviderDPoPIssuanceEnvelopeContract {
		return envelope, errors.New("provider DPoP issuance envelope is invalid")
	}
	statementBytes, err := canonicalJSON(envelope.Statement)
	if err != nil {
		return envelope, errors.New("provider DPoP issuance statement is invalid")
	}
	defer clear(statementBytes)
	statement, _, err := parseProviderDPoPIssuanceStatement(statementBytes, now)
	if err != nil {
		return envelope, err
	}
	envelope.Statement = statement
	digest := sha256.Sum256(statementBytes)
	if !equalDigest(envelope.StatementSHA256, digest[:]) {
		return envelope, errors.New("provider DPoP issuance statement digest does not match")
	}
	trustBytes, err := readStablePrivateFile(trustProfileFile, maximumInputBytes)
	if err != nil {
		return envelope, err
	}
	defer clear(trustBytes)
	trust, canonicalTrust, err := parseProviderDPoPIssuanceTrustProfile(trustBytes)
	if err != nil {
		return envelope, err
	}
	defer clear(canonicalTrust)
	trustDigest := sha256.Sum256(canonicalTrust)
	if !equalDigest(statement.TrustProfileSHA256, trustDigest[:]) {
		return envelope, errors.New("provider DPoP issuance trust profile digest does not match")
	}
	if err := validateProviderDPoPIssuanceSignature(envelope.Signature); err != nil {
		return envelope, err
	}
	if err := verifyProviderDPoPIssuanceSignature(statementBytes, envelope.Signature, trust); err != nil {
		return envelope, err
	}
	if err := verifyProviderDPoPIssuanceReport(statement, now); err != nil {
		return envelope, err
	}
	return envelope, nil
}

func loadProviderDPoPIssuanceInputs(
	statementFile string,
	trustProfileFile string,
	now time.Time,
) (
	ProviderDPoPIssuanceStatement,
	[]byte,
	ProviderDPoPIssuanceTrustProfile,
	[]byte,
	error,
) {
	var zeroStatement ProviderDPoPIssuanceStatement
	var zeroTrust ProviderDPoPIssuanceTrustProfile
	statementBytes, err := readStablePrivateFile(statementFile, maximumInputBytes)
	if err != nil {
		return zeroStatement, nil, zeroTrust, nil, fmt.Errorf("read provider DPoP issuance statement: %w", err)
	}
	defer clear(statementBytes)
	statement, canonicalStatement, err := parseProviderDPoPIssuanceStatement(statementBytes, now)
	if err != nil {
		return zeroStatement, nil, zeroTrust, nil, err
	}
	trustBytes, err := readStablePrivateFile(trustProfileFile, maximumInputBytes)
	if err != nil {
		clear(canonicalStatement)
		return zeroStatement, nil, zeroTrust, nil, fmt.Errorf("read provider DPoP issuance trust profile: %w", err)
	}
	defer clear(trustBytes)
	trust, canonicalTrust, err := parseProviderDPoPIssuanceTrustProfile(trustBytes)
	if err != nil {
		clear(canonicalStatement)
		return zeroStatement, nil, zeroTrust, nil, err
	}
	trustDigest := sha256.Sum256(canonicalTrust)
	if !equalDigest(statement.TrustProfileSHA256, trustDigest[:]) {
		clear(canonicalStatement)
		clear(canonicalTrust)
		return zeroStatement, nil, zeroTrust, nil,
			errors.New("provider DPoP issuance trust profile digest does not match")
	}
	return statement, canonicalStatement, trust, canonicalTrust, nil
}

func parseProviderDPoPIssuanceStatement(
	encoded []byte,
	now time.Time,
) (ProviderDPoPIssuanceStatement, []byte, error) {
	var statement ProviderDPoPIssuanceStatement
	if err := decodeCanonicalJSON(encoded, &statement); err != nil {
		return statement, nil, errors.New("provider DPoP issuance statement is invalid")
	}
	canonical, err := canonicalJSON(statement)
	if err != nil || !bytes.Equal(encoded, canonical) {
		clear(canonical)
		return statement, nil, errors.New("provider DPoP issuance statement is not canonical")
	}
	if statement.Contract != ProviderDPoPIssuanceStatementContract ||
		!idPattern.MatchString(statement.CertificationID) ||
		!providerIDPattern.MatchString(statement.ProviderID) ||
		!digestPattern.MatchString(statement.ProviderRegistrySHA256) ||
		!validProviderDPoPIssuanceURL(statement.Issuer, true) ||
		!validProviderDPoPIssuanceURL(statement.TokenEndpoint, false) ||
		statement.Audience != "dataground-api" ||
		!validProviderDPoPIssuanceGrantTypes(statement.GrantTypes) ||
		!validProviderDPoPIssuanceAlgorithms(statement.DPoPAlgorithms) ||
		!validProviderDPoPIssuanceNonceMode(statement.AuthorizationServerNonce) ||
		!canonicalAbsolutePath(statement.ConformanceReportFile) ||
		!digestPattern.MatchString(statement.ConformanceReportSHA256) ||
		!digestPattern.MatchString(statement.TrustProfileSHA256) ||
		!idPattern.MatchString(statement.ReviewerID) || !validReason(statement.Reason) {
		clear(canonical)
		return statement, nil, errors.New("provider DPoP issuance statement fields are invalid")
	}
	issuedAt, err := parseCanonicalTime(statement.IssuedAt)
	if err != nil {
		clear(canonical)
		return statement, nil, errors.New("provider DPoP issuance issue time is invalid")
	}
	expiresAt, err := parseCanonicalTime(statement.ExpiresAt)
	if err != nil || !expiresAt.After(issuedAt) ||
		expiresAt.Sub(issuedAt) > providerDPoPIssuanceMaximumValidity ||
		issuedAt.After(now.Add(providerDPoPIssuanceClockSkew)) || !expiresAt.After(now) {
		clear(canonical)
		return statement, nil, errors.New("provider DPoP issuance validity is invalid")
	}
	return statement, canonical, nil
}

func verifyProviderDPoPIssuanceReport(statement ProviderDPoPIssuanceStatement, now time.Time) error {
	encoded, err := readStablePrivateFile(statement.ConformanceReportFile, maximumEvidenceBytes)
	if err != nil {
		return errors.New("provider DPoP issuance conformance report is unavailable")
	}
	defer clear(encoded)
	digest := sha256.Sum256(encoded)
	if !equalDigest(statement.ConformanceReportSHA256, digest[:]) {
		return errors.New("provider DPoP issuance conformance report digest does not match")
	}
	var report ProviderDPoPIssuanceReport
	if err := decodeCanonicalJSON(encoded, &report); err != nil {
		return errors.New("provider DPoP issuance conformance report is invalid")
	}
	canonical, err := canonicalJSON(report)
	if err != nil || !bytes.Equal(encoded, canonical) ||
		report.Contract != ProviderDPoPIssuanceReportContract || !idPattern.MatchString(report.RunID) ||
		report.ProviderID != statement.ProviderID ||
		report.ProviderRegistrySHA256 != statement.ProviderRegistrySHA256 ||
		report.Issuer != statement.Issuer || report.TokenEndpoint != statement.TokenEndpoint ||
		report.Audience != statement.Audience ||
		!equalStrings(report.GrantTypes, statement.GrantTypes) ||
		!equalStrings(report.DPoPAlgorithms, statement.DPoPAlgorithms) ||
		report.AuthorizationServerNonce != statement.AuthorizationServerNonce || !report.Checks.Valid() {
		clear(canonical)
		return errors.New("provider DPoP issuance conformance report fields are invalid")
	}
	clear(canonical)
	observedAt, err := parseCanonicalTime(report.ObservedAt)
	issuedAt, issueErr := parseCanonicalTime(statement.IssuedAt)
	if err != nil || issueErr != nil || observedAt.After(issuedAt) ||
		issuedAt.Sub(observedAt) > providerDPoPIssuanceMaximumAge ||
		observedAt.After(now.Add(providerDPoPIssuanceClockSkew)) {
		return errors.New("provider DPoP issuance observation time is invalid")
	}
	return nil
}

func parseProviderDPoPIssuanceSignature(encoded []byte) (ProviderDPoPIssuanceSignature, error) {
	var signature ProviderDPoPIssuanceSignature
	if err := decodeCanonicalJSON(encoded, &signature); err != nil {
		return signature, errors.New("provider DPoP issuance signature is invalid")
	}
	canonical, err := canonicalJSON(signature)
	defer clear(canonical)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return signature, errors.New("provider DPoP issuance signature is not canonical")
	}
	if err := validateProviderDPoPIssuanceSignature(signature); err != nil {
		return signature, err
	}
	return signature, nil
}

func validateProviderDPoPIssuanceSignature(signature ProviderDPoPIssuanceSignature) error {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(signature.Signature)
	if signature.Contract != ProviderDPoPIssuanceSignatureContract ||
		!idPattern.MatchString(signature.KeyID) || err != nil || len(decoded) != ed25519.SignatureSize ||
		base64.RawURLEncoding.EncodeToString(decoded) != signature.Signature {
		return errors.New("provider DPoP issuance signature fields are invalid")
	}
	return nil
}

func parseProviderDPoPIssuanceTrustProfile(
	encoded []byte,
) (ProviderDPoPIssuanceTrustProfile, []byte, error) {
	var trust ProviderDPoPIssuanceTrustProfile
	if err := decodeCanonicalJSON(encoded, &trust); err != nil {
		return trust, nil, errors.New("provider DPoP issuance trust profile is invalid")
	}
	canonical, err := canonicalJSON(trust)
	if err != nil || !bytes.Equal(encoded, canonical) ||
		trust.Contract != ProviderDPoPIssuanceTrustContract || len(trust.Keys) == 0 || len(trust.Keys) > 8 {
		clear(canonical)
		return trust, nil, errors.New("provider DPoP issuance trust profile fields are invalid")
	}
	previous := ""
	for _, key := range trust.Keys {
		decoded, decodeErr := base64.RawURLEncoding.Strict().DecodeString(key.PublicKey)
		if !idPattern.MatchString(key.KeyID) || key.KeyID <= previous || decodeErr != nil ||
			len(decoded) != ed25519.PublicKeySize ||
			base64.RawURLEncoding.EncodeToString(decoded) != key.PublicKey {
			clear(canonical)
			return trust, nil, errors.New("provider DPoP issuance trust key is invalid")
		}
		previous = key.KeyID
	}
	return trust, canonical, nil
}

func verifyProviderDPoPIssuanceSignature(
	statement []byte,
	signature ProviderDPoPIssuanceSignature,
	trust ProviderDPoPIssuanceTrustProfile,
) error {
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(signature.Signature)
	if err != nil {
		return errors.New("provider DPoP issuance signature is invalid")
	}
	index := sort.Search(len(trust.Keys), func(index int) bool {
		return trust.Keys[index].KeyID >= signature.KeyID
	})
	if index >= len(trust.Keys) || trust.Keys[index].KeyID != signature.KeyID {
		return errors.New("provider DPoP issuance signing key is not trusted")
	}
	publicKey, err := base64.RawURLEncoding.Strict().DecodeString(trust.Keys[index].PublicKey)
	if err != nil || !ed25519.Verify(
		ed25519.PublicKey(publicKey), providerDPoPIssuanceSignatureMessage(statement), encoded,
	) {
		return errors.New("provider DPoP issuance signature does not verify")
	}
	return nil
}

func providerDPoPIssuanceSignatureMessage(statement []byte) []byte {
	message := make([]byte, 0, len(providerDPoPIssuanceSignatureDomain)+len(statement))
	message = append(message, providerDPoPIssuanceSignatureDomain...)
	return append(message, statement...)
}

func validProviderDPoPIssuanceURL(value string, issuer bool) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.String() != value || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" ||
		parsed.Opaque != "" || parsed.OmitHost {
		return false
	}
	if issuer {
		return true
	}
	return parsed.Path != ""
}

func validProviderDPoPIssuanceGrantTypes(values []string) bool {
	return validSortedProviderDPoPValues(values, map[string]struct{}{
		"authorization_code": {}, "client_credentials": {}, "refresh_token": {},
	})
}

func validProviderDPoPIssuanceAlgorithms(values []string) bool {
	return validSortedProviderDPoPValues(values, map[string]struct{}{"ES256": {}, "EdDSA": {}})
}

func validSortedProviderDPoPValues(values []string, allowed map[string]struct{}) bool {
	if len(values) == 0 || len(values) > len(allowed) {
		return false
	}
	previous := ""
	for _, value := range values {
		if _, ok := allowed[value]; !ok || value <= previous {
			return false
		}
		previous = value
	}
	return true
}

func validProviderDPoPIssuanceNonceMode(value string) bool {
	return value == "not-required" || value == "challenge-retry"
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
