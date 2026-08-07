package auditseal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"time"
)

const (
	WorkloadIdentityRevocationContract          = "dataground.audit-export-workload-identity-revocation/ed25519/v1"
	WorkloadIdentityRevocationContentContract   = "dataground.audit-export-workload-identity-revocation/v1"
	WorkloadIdentityRevocationSignatureContract = "dataground.audit-export-workload-identity-revocation-signature/ed25519/v1"
	WorkloadIdentityRevocationTrustContract     = "dataground.audit-export-workload-identity-revocation-trust/ed25519/v1"

	workloadIdentityRevocationDomain = "DataGround audit export workload identity revocation v1\n"
)

type WorkloadIdentityRevocationTrustProfile struct {
	Contract    string       `json:"contract"`
	AuthorityID string       `json:"authorityId"`
	Keys        []TrustedKey `json:"keys"`
}

type WorkloadIdentityRevocationContent struct {
	Contract                           string    `json:"contract"`
	IsolationDomainID                  string    `json:"isolationDomainId"`
	Scope                              string    `json:"scope"`
	WorkloadIdentityAuthorityID        string    `json:"workloadIdentityAuthorityId"`
	WorkloadIdentityTrustProfileSHA256 string    `json:"workloadIdentityTrustProfileSha256"`
	WorkloadIdentitySigningKeyID       string    `json:"workloadIdentitySigningKeyId,omitempty"`
	ReasonSHA256                       string    `json:"reasonSha256"`
	RevocationAuthorityID              string    `json:"revocationAuthorityId"`
	IssuedAt                           time.Time `json:"issuedAt"`
	EffectiveAt                        time.Time `json:"effectiveAt"`
}

type WorkloadIdentityRevocationSignature struct {
	Contract  string `json:"contract"`
	KeyID     string `json:"keyId"`
	Signature string `json:"signature"`
}

type WorkloadIdentityRevocation struct {
	Contract                     string                              `json:"contract"`
	Content                      WorkloadIdentityRevocationContent   `json:"content"`
	ContentSHA256                string                              `json:"contentSha256"`
	RevocationTrustProfileSHA256 string                              `json:"revocationTrustProfileSha256"`
	Signature                    WorkloadIdentityRevocationSignature `json:"signature"`
}

type VerifiedWorkloadIdentityRevocation struct {
	Contract                           string
	SHA256                             string
	IsolationDomainID                  string
	Scope                              string
	WorkloadIdentityAuthorityID        string
	WorkloadIdentityTrustProfileSHA256 string
	WorkloadIdentitySigningKeyID       string
	ReasonSHA256                       string
	RevocationAuthorityID              string
	RevocationTrustProfileSHA256       string
	RevocationSigningKeyID             string
	IssuedAt                           time.Time
	EffectiveAt                        time.Time
}

type workloadIdentityRevocationSigningFields struct {
	Contract                     string                            `json:"contract"`
	Content                      WorkloadIdentityRevocationContent `json:"content"`
	ContentSHA256                string                            `json:"contentSha256"`
	RevocationTrustProfileSHA256 string                            `json:"revocationTrustProfileSha256"`
	KeyID                        string                            `json:"keyId"`
}

func VerifyWorkloadIdentityRevocationFile(
	revocationFile string,
	revocationTrustProfileFile string,
	isolationDomainID string,
	now time.Time,
) (VerifiedWorkloadIdentityRevocation, error) {
	var verified VerifiedWorkloadIdentityRevocation
	if !distinctPaths(revocationFile, revocationTrustProfileFile) ||
		!auditExportIsolationDomainPattern.MatchString(isolationDomainID) || now.IsZero() {
		return verified, errors.New("audit export workload identity revocation inputs are invalid")
	}
	encoded, err := readStablePrivateFile(revocationFile, maximumControlBytes)
	if err != nil {
		return verified, fmt.Errorf("read audit export workload identity revocation: %w", err)
	}
	defer clear(encoded)
	trustEncoded, err := readStablePrivateFile(revocationTrustProfileFile, maximumControlBytes)
	if err != nil {
		return verified, fmt.Errorf("read audit export workload identity revocation trust profile: %w", err)
	}
	defer clear(trustEncoded)
	return VerifyWorkloadIdentityRevocation(encoded, trustEncoded, isolationDomainID, now)
}

// VerifyWorkloadIdentityRevocation verifies one owned in-memory notice and
// trust profile. Callers must clear both byte slices after use.
func VerifyWorkloadIdentityRevocation(
	encoded []byte,
	trustEncoded []byte,
	isolationDomainID string,
	now time.Time,
) (VerifiedWorkloadIdentityRevocation, error) {
	var verified VerifiedWorkloadIdentityRevocation
	if len(encoded) == 0 || len(encoded) > maximumControlBytes ||
		len(trustEncoded) == 0 || len(trustEncoded) > maximumControlBytes ||
		!auditExportIsolationDomainPattern.MatchString(isolationDomainID) || now.IsZero() {
		return verified, errors.New("audit export workload identity revocation inputs are invalid")
	}
	var revocation WorkloadIdentityRevocation
	if err := decodeCanonicalJSON(encoded, &revocation, maximumControlBytes); err != nil {
		return verified, errors.New("audit export workload identity revocation is invalid")
	}
	canonicalRevocation, err := canonicalJSON(revocation)
	if err != nil || !bytes.Equal(canonicalRevocation, encoded) {
		clear(canonicalRevocation)
		return verified, errors.New("audit export workload identity revocation is not canonical")
	}
	clear(canonicalRevocation)
	trust, canonicalTrust, err := decodeWorkloadIdentityRevocationTrustProfile(trustEncoded)
	if err != nil {
		return verified, err
	}
	defer clear(canonicalTrust)
	trustDigest := sha256.Sum256(canonicalTrust)
	content, err := canonicalJSON(revocation.Content)
	if err != nil {
		return verified, errors.New("encode audit export workload identity revocation content")
	}
	defer clear(content)
	contentDigest := sha256.Sum256(bytes.TrimSuffix(content, []byte{'\n'}))
	now = now.UTC()
	if revocation.Contract != WorkloadIdentityRevocationContract ||
		revocation.Content.Contract != WorkloadIdentityRevocationContentContract ||
		revocation.Content.IsolationDomainID != isolationDomainID ||
		!validWorkloadIdentityRevocationScope(revocation.Content.Scope, revocation.Content.WorkloadIdentitySigningKeyID) ||
		!auditExportDeliveryRecipientPattern.MatchString(revocation.Content.WorkloadIdentityAuthorityID) ||
		!digestPattern.MatchString(revocation.Content.WorkloadIdentityTrustProfileSHA256) ||
		!digestPattern.MatchString(revocation.Content.ReasonSHA256) ||
		revocation.Content.RevocationAuthorityID != trust.AuthorityID ||
		revocation.Content.RevocationAuthorityID == revocation.Content.WorkloadIdentityAuthorityID ||
		revocation.ContentSHA256 != digestString(contentDigest) ||
		revocation.RevocationTrustProfileSHA256 != digestString(trustDigest) ||
		!canonicalRecipientIdentityProofTime(revocation.Content.IssuedAt) ||
		!canonicalRecipientIdentityProofTime(revocation.Content.EffectiveAt) ||
		revocation.Content.IssuedAt.After(now.Add(maximumProofClockSkew)) {
		return verified, errors.New("audit export workload identity revocation fields do not match")
	}
	if err := verifyWorkloadIdentityRevocationSignature(revocation, trust); err != nil {
		return verified, err
	}
	revocationDigest := sha256.Sum256(encoded)
	return VerifiedWorkloadIdentityRevocation{
		Contract: revocation.Contract, SHA256: digestString(revocationDigest),
		IsolationDomainID: revocation.Content.IsolationDomainID, Scope: revocation.Content.Scope,
		WorkloadIdentityAuthorityID:        revocation.Content.WorkloadIdentityAuthorityID,
		WorkloadIdentityTrustProfileSHA256: revocation.Content.WorkloadIdentityTrustProfileSHA256,
		WorkloadIdentitySigningKeyID:       revocation.Content.WorkloadIdentitySigningKeyID,
		ReasonSHA256:                       revocation.Content.ReasonSHA256,
		RevocationAuthorityID:              revocation.Content.RevocationAuthorityID,
		RevocationTrustProfileSHA256:       revocation.RevocationTrustProfileSHA256,
		RevocationSigningKeyID:             revocation.Signature.KeyID,
		IssuedAt:                           revocation.Content.IssuedAt, EffectiveAt: revocation.Content.EffectiveAt,
	}, nil
}

func decodeWorkloadIdentityRevocationTrustProfile(
	encoded []byte,
) (WorkloadIdentityRevocationTrustProfile, []byte, error) {
	var trust WorkloadIdentityRevocationTrustProfile
	if len(encoded) == 0 || len(encoded) > maximumControlBytes {
		return trust, nil, errors.New("audit export workload identity revocation trust profile is invalid")
	}
	if err := decodeCanonicalJSON(encoded, &trust, maximumControlBytes); err != nil {
		return trust, nil, errors.New("audit export workload identity revocation trust profile is invalid")
	}
	canonical, err := canonicalJSON(trust)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return trust, nil, errors.New("audit export workload identity revocation trust profile is not canonical")
	}
	if trust.Contract != WorkloadIdentityRevocationTrustContract ||
		!auditExportDeliveryRecipientPattern.MatchString(trust.AuthorityID) ||
		!validSortedTrustedKeys(trust.Keys) {
		clear(canonical)
		return trust, nil, errors.New("audit export workload identity revocation trust profile fields are invalid")
	}
	return trust, canonical, nil
}

func readWorkloadIdentityRevocationTrustProfile(path string) (WorkloadIdentityRevocationTrustProfile, []byte, error) {
	var trust WorkloadIdentityRevocationTrustProfile
	encoded, err := readStablePrivateFile(path, maximumControlBytes)
	if err != nil {
		return trust, nil, fmt.Errorf("read audit export workload identity revocation trust profile: %w", err)
	}
	defer clear(encoded)
	return decodeWorkloadIdentityRevocationTrustProfile(encoded)
}

func validWorkloadIdentityRevocationScope(scope, keyID string) bool {
	return (scope == "profile" && keyID == "") ||
		(scope == "key" && keyIDPattern.MatchString(keyID))
}

func verifyWorkloadIdentityRevocationSignature(
	revocation WorkloadIdentityRevocation,
	trust WorkloadIdentityRevocationTrustProfile,
) error {
	if revocation.Signature.Contract != WorkloadIdentityRevocationSignatureContract ||
		!keyIDPattern.MatchString(revocation.Signature.KeyID) {
		return errors.New("audit export workload identity revocation signature fields are invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(revocation.Signature.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		base64.RawURLEncoding.EncodeToString(signature) != revocation.Signature.Signature {
		clear(signature)
		return errors.New("audit export workload identity revocation signature fields are invalid")
	}
	defer clear(signature)
	index := sort.Search(len(trust.Keys), func(index int) bool {
		return trust.Keys[index].KeyID >= revocation.Signature.KeyID
	})
	if index >= len(trust.Keys) || trust.Keys[index].KeyID != revocation.Signature.KeyID {
		return errors.New("audit export workload identity revocation signing key is not trusted")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(trust.Keys[index].PublicKey)
	if err != nil {
		return errors.New("audit export workload identity revocation signing key is invalid")
	}
	defer clear(publicKey)
	message, err := workloadIdentityRevocationSigningMessage(revocation)
	if err != nil {
		return err
	}
	defer clear(message)
	if !ed25519.Verify(publicKey, message, signature) {
		return errors.New("audit export workload identity revocation signature does not verify")
	}
	return nil
}

func workloadIdentityRevocationSigningMessage(revocation WorkloadIdentityRevocation) ([]byte, error) {
	fields := workloadIdentityRevocationSigningFields{
		Contract: revocation.Contract, Content: revocation.Content,
		ContentSHA256:                revocation.ContentSHA256,
		RevocationTrustProfileSHA256: revocation.RevocationTrustProfileSHA256,
		KeyID:                        revocation.Signature.KeyID,
	}
	canonical, err := canonicalJSON(fields)
	if err != nil {
		return nil, errors.New("encode audit export workload identity revocation signing message")
	}
	message := make([]byte, 0, len(workloadIdentityRevocationDomain)+len(canonical))
	message = append(message, workloadIdentityRevocationDomain...)
	message = append(message, canonical...)
	clear(canonical)
	return message, nil
}
