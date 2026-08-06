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
	WorkloadIdentityGrantContract          = "dataground.audit-export-workload-identity-grant/ed25519/v1"
	WorkloadIdentityGrantContentContract   = "dataground.audit-export-workload-identity-grant/v1"
	WorkloadIdentityGrantSignatureContract = "dataground.audit-export-workload-identity-grant-signature/ed25519/v1"
	WorkloadIdentityTrustContract          = "dataground.audit-export-workload-identity-trust/ed25519/v1"
	AuditExportTransportAudience           = "dataground.audit-export-transport"

	workloadIdentityGrantDomain = "DataGround audit export workload identity grant v1\n"
)

type WorkloadIdentityTrustProfile struct {
	Contract    string       `json:"contract"`
	AuthorityID string       `json:"authorityId"`
	Keys        []TrustedKey `json:"keys"`
}

type WorkloadIdentityGrantContent struct {
	Contract                string    `json:"contract"`
	IsolationDomainID       string    `json:"isolationDomainId"`
	WorkloadID              string    `json:"workloadId"`
	Audience                string    `json:"audience"`
	ClientCertificateSHA256 string    `json:"clientCertificateSha256"`
	AuthorityID             string    `json:"authorityId"`
	IssuedAt                time.Time `json:"issuedAt"`
	NotBefore               time.Time `json:"notBefore"`
	ExpiresAt               time.Time `json:"expiresAt"`
}

type WorkloadIdentityGrantSignature struct {
	Contract  string `json:"contract"`
	KeyID     string `json:"keyId"`
	Signature string `json:"signature"`
}

type WorkloadIdentityGrant struct {
	Contract                 string                         `json:"contract"`
	Content                  WorkloadIdentityGrantContent   `json:"content"`
	ContentSHA256            string                         `json:"contentSha256"`
	IssuerTrustProfileSHA256 string                         `json:"issuerTrustProfileSha256"`
	Signature                WorkloadIdentityGrantSignature `json:"signature"`
}

type VerifiedWorkloadIdentityGrant struct {
	Contract                 string
	SHA256                   string
	IsolationDomainID        string
	WorkloadID               string
	Audience                 string
	ClientCertificateSHA256  string
	AuthorityID              string
	IssuerTrustProfileSHA256 string
	IssuerSigningKeyID       string
	IssuedAt                 time.Time
	NotBefore                time.Time
	ExpiresAt                time.Time
}

type workloadIdentityGrantSigningFields struct {
	Contract                 string                       `json:"contract"`
	Content                  WorkloadIdentityGrantContent `json:"content"`
	ContentSHA256            string                       `json:"contentSha256"`
	IssuerTrustProfileSHA256 string                       `json:"issuerTrustProfileSha256"`
	KeyID                    string                       `json:"keyId"`
}

func VerifyWorkloadIdentityGrantFile(
	grantFile string,
	issuerTrustFile string,
	isolationDomainID string,
	workloadID string,
	clientCertificateSHA256 string,
	now time.Time,
) (VerifiedWorkloadIdentityGrant, error) {
	var verified VerifiedWorkloadIdentityGrant
	if !distinctPaths(grantFile, issuerTrustFile) ||
		!auditExportIsolationDomainPattern.MatchString(isolationDomainID) ||
		!auditExportDeliveryRecipientPattern.MatchString(workloadID) ||
		!digestPattern.MatchString(clientCertificateSHA256) || now.IsZero() {
		return verified, errors.New("audit export workload identity inputs are invalid")
	}
	encoded, err := readStablePrivateFile(grantFile, maximumControlBytes)
	if err != nil {
		return verified, fmt.Errorf("read audit export workload identity grant: %w", err)
	}
	defer clear(encoded)
	var grant WorkloadIdentityGrant
	if err := decodeCanonicalJSON(encoded, &grant, maximumControlBytes); err != nil {
		return verified, errors.New("audit export workload identity grant is invalid")
	}
	canonicalGrant, err := canonicalJSON(grant)
	if err != nil || !bytes.Equal(canonicalGrant, encoded) {
		clear(canonicalGrant)
		return verified, errors.New("audit export workload identity grant is not canonical")
	}
	clear(canonicalGrant)
	trust, canonicalTrust, err := readWorkloadIdentityTrustProfile(issuerTrustFile)
	if err != nil {
		return verified, err
	}
	defer clear(canonicalTrust)
	trustDigest := sha256.Sum256(canonicalTrust)
	content, err := canonicalJSON(grant.Content)
	if err != nil {
		return verified, errors.New("encode audit export workload identity grant content")
	}
	defer clear(content)
	contentDigest := sha256.Sum256(bytes.TrimSuffix(content, []byte{'\n'}))
	now = now.UTC()
	if grant.Contract != WorkloadIdentityGrantContract ||
		grant.Content.Contract != WorkloadIdentityGrantContentContract ||
		grant.Content.IsolationDomainID != isolationDomainID ||
		grant.Content.WorkloadID != workloadID ||
		grant.Content.Audience != AuditExportTransportAudience ||
		grant.Content.ClientCertificateSHA256 != clientCertificateSHA256 ||
		grant.Content.AuthorityID != trust.AuthorityID ||
		grant.ContentSHA256 != digestString(contentDigest) ||
		grant.IssuerTrustProfileSHA256 != digestString(trustDigest) ||
		!canonicalRecipientIdentityProofTime(grant.Content.IssuedAt) ||
		!canonicalRecipientIdentityProofTime(grant.Content.NotBefore) ||
		!canonicalRecipientIdentityProofTime(grant.Content.ExpiresAt) ||
		grant.Content.IssuedAt.After(now.Add(maximumProofClockSkew)) ||
		grant.Content.NotBefore.After(now) || !grant.Content.ExpiresAt.After(now) ||
		grant.Content.NotBefore.Before(grant.Content.IssuedAt) ||
		!grant.Content.ExpiresAt.After(grant.Content.NotBefore) {
		return verified, errors.New("audit export workload identity grant fields do not match")
	}
	if err := verifyWorkloadIdentityGrantSignature(grant, trust); err != nil {
		return verified, err
	}
	grantDigest := sha256.Sum256(encoded)
	return VerifiedWorkloadIdentityGrant{
		Contract: grant.Contract, SHA256: digestString(grantDigest),
		IsolationDomainID: grant.Content.IsolationDomainID, WorkloadID: grant.Content.WorkloadID,
		Audience: grant.Content.Audience, ClientCertificateSHA256: grant.Content.ClientCertificateSHA256,
		AuthorityID: grant.Content.AuthorityID, IssuerTrustProfileSHA256: grant.IssuerTrustProfileSHA256,
		IssuerSigningKeyID: grant.Signature.KeyID, IssuedAt: grant.Content.IssuedAt,
		NotBefore: grant.Content.NotBefore, ExpiresAt: grant.Content.ExpiresAt,
	}, nil
}

func readWorkloadIdentityTrustProfile(path string) (WorkloadIdentityTrustProfile, []byte, error) {
	var trust WorkloadIdentityTrustProfile
	encoded, err := readStablePrivateFile(path, maximumControlBytes)
	if err != nil {
		return trust, nil, fmt.Errorf("read audit export workload identity trust profile: %w", err)
	}
	defer clear(encoded)
	if err := decodeCanonicalJSON(encoded, &trust, maximumControlBytes); err != nil {
		return trust, nil, errors.New("audit export workload identity trust profile is invalid")
	}
	canonical, err := canonicalJSON(trust)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return trust, nil, errors.New("audit export workload identity trust profile is not canonical")
	}
	if trust.Contract != WorkloadIdentityTrustContract ||
		!auditExportDeliveryRecipientPattern.MatchString(trust.AuthorityID) ||
		!validSortedTrustedKeys(trust.Keys) {
		clear(canonical)
		return trust, nil, errors.New("audit export workload identity trust profile fields are invalid")
	}
	return trust, canonical, nil
}

func verifyWorkloadIdentityGrantSignature(
	grant WorkloadIdentityGrant,
	trust WorkloadIdentityTrustProfile,
) error {
	if grant.Signature.Contract != WorkloadIdentityGrantSignatureContract ||
		!keyIDPattern.MatchString(grant.Signature.KeyID) {
		return errors.New("audit export workload identity grant signature fields are invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(grant.Signature.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		base64.RawURLEncoding.EncodeToString(signature) != grant.Signature.Signature {
		clear(signature)
		return errors.New("audit export workload identity grant signature fields are invalid")
	}
	defer clear(signature)
	index := sort.Search(len(trust.Keys), func(index int) bool {
		return trust.Keys[index].KeyID >= grant.Signature.KeyID
	})
	if index >= len(trust.Keys) || trust.Keys[index].KeyID != grant.Signature.KeyID {
		return errors.New("audit export workload identity grant signing key is not trusted")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(trust.Keys[index].PublicKey)
	if err != nil {
		return errors.New("audit export workload identity grant signing key is invalid")
	}
	defer clear(publicKey)
	message, err := workloadIdentityGrantSigningMessage(grant)
	if err != nil {
		return err
	}
	defer clear(message)
	if !ed25519.Verify(publicKey, message, signature) {
		return errors.New("audit export workload identity grant signature does not verify")
	}
	return nil
}

func workloadIdentityGrantSigningMessage(grant WorkloadIdentityGrant) ([]byte, error) {
	fields := workloadIdentityGrantSigningFields{
		Contract: grant.Contract, Content: grant.Content, ContentSHA256: grant.ContentSHA256,
		IssuerTrustProfileSHA256: grant.IssuerTrustProfileSHA256, KeyID: grant.Signature.KeyID,
	}
	canonical, err := canonicalJSON(fields)
	if err != nil {
		return nil, errors.New("encode audit export workload identity grant signing message")
	}
	message := make([]byte, 0, len(workloadIdentityGrantDomain)+len(canonical))
	message = append(message, workloadIdentityGrantDomain...)
	message = append(message, canonical...)
	clear(canonical)
	return message, nil
}
