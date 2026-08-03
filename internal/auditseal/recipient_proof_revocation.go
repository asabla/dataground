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
	RecipientProofRevocationContract          = "dataground.audit-export-recipient-proof-revocation/ed25519/v1"
	RecipientProofRevocationContentContract   = "dataground.audit-export-recipient-proof-revocation/v1"
	RecipientProofRevocationSignatureContract = "dataground.audit-export-recipient-proof-revocation-signature/ed25519/v1"
	RecipientRevocationTrustContract          = "dataground.audit-export-recipient-revocation-trust/ed25519/v1"

	recipientProofRevocationDomain = "DataGround audit export recipient proof revocation v1\n"
)

type RecipientRevocationTrustProfile struct {
	Contract    string       `json:"contract"`
	AuthorityID string       `json:"authorityId"`
	Keys        []TrustedKey `json:"keys"`
}

type RecipientProofRevocationContent struct {
	Contract                   string    `json:"contract"`
	IsolationDomainID          string    `json:"isolationDomainId"`
	Scope                      string    `json:"scope"`
	ProofingAuthorityID        string    `json:"proofingAuthorityId"`
	ProofingTrustProfileSHA256 string    `json:"proofingTrustProfileSha256"`
	ProofingSigningKeyID       string    `json:"proofingSigningKeyId,omitempty"`
	ReasonSHA256               string    `json:"reasonSha256"`
	RevocationAuthorityID      string    `json:"revocationAuthorityId"`
	IssuedAt                   time.Time `json:"issuedAt"`
	EffectiveAt                time.Time `json:"effectiveAt"`
}

type RecipientProofRevocationSignature struct {
	Contract  string `json:"contract"`
	KeyID     string `json:"keyId"`
	Signature string `json:"signature"`
}

type RecipientProofRevocation struct {
	Contract                     string                            `json:"contract"`
	Content                      RecipientProofRevocationContent   `json:"content"`
	ContentSHA256                string                            `json:"contentSha256"`
	RevocationTrustProfileSHA256 string                            `json:"revocationTrustProfileSha256"`
	Signature                    RecipientProofRevocationSignature `json:"signature"`
}

type VerifiedRecipientProofRevocation struct {
	Contract                     string
	SHA256                       string
	IsolationDomainID            string
	Scope                        string
	ProofingAuthorityID          string
	ProofingTrustProfileSHA256   string
	ProofingSigningKeyID         string
	ReasonSHA256                 string
	RevocationAuthorityID        string
	RevocationTrustProfileSHA256 string
	RevocationSigningKeyID       string
	IssuedAt                     time.Time
	EffectiveAt                  time.Time
}

type recipientProofRevocationSigningFields struct {
	Contract                     string                          `json:"contract"`
	Content                      RecipientProofRevocationContent `json:"content"`
	ContentSHA256                string                          `json:"contentSha256"`
	RevocationTrustProfileSHA256 string                          `json:"revocationTrustProfileSha256"`
	KeyID                        string                          `json:"keyId"`
}

func VerifyRecipientProofRevocationFile(
	revocationFile string,
	revocationTrustProfileFile string,
	isolationDomainID string,
	now time.Time,
) (VerifiedRecipientProofRevocation, error) {
	var verified VerifiedRecipientProofRevocation
	if !distinctPaths(revocationFile, revocationTrustProfileFile) ||
		!auditExportIsolationDomainPattern.MatchString(isolationDomainID) || now.IsZero() {
		return verified, errors.New("audit export recipient proof revocation inputs are invalid")
	}
	encoded, err := readStablePrivateFile(revocationFile, maximumControlBytes)
	if err != nil {
		return verified, fmt.Errorf("read audit export recipient proof revocation: %w", err)
	}
	defer clear(encoded)
	var revocation RecipientProofRevocation
	if err := decodeCanonicalJSON(encoded, &revocation, maximumControlBytes); err != nil {
		return verified, errors.New("audit export recipient proof revocation is invalid")
	}
	canonicalRevocation, err := canonicalJSON(revocation)
	if err != nil || !bytes.Equal(canonicalRevocation, encoded) {
		clear(canonicalRevocation)
		return verified, errors.New("audit export recipient proof revocation is not canonical")
	}
	clear(canonicalRevocation)
	trust, canonicalTrust, err := readRecipientRevocationTrustProfile(revocationTrustProfileFile)
	if err != nil {
		return verified, err
	}
	defer clear(canonicalTrust)
	trustDigest := sha256.Sum256(canonicalTrust)
	content, err := canonicalJSON(revocation.Content)
	if err != nil {
		return verified, errors.New("encode audit export recipient proof revocation content")
	}
	defer clear(content)
	contentDigest := sha256.Sum256(bytes.TrimSuffix(content, []byte{'\n'}))
	now = now.UTC()
	if revocation.Contract != RecipientProofRevocationContract ||
		revocation.Content.Contract != RecipientProofRevocationContentContract ||
		revocation.Content.IsolationDomainID != isolationDomainID ||
		!validRecipientProofRevocationScope(revocation.Content.Scope, revocation.Content.ProofingSigningKeyID) ||
		!auditExportDeliveryRecipientPattern.MatchString(revocation.Content.ProofingAuthorityID) ||
		!digestPattern.MatchString(revocation.Content.ProofingTrustProfileSHA256) ||
		!digestPattern.MatchString(revocation.Content.ReasonSHA256) ||
		revocation.Content.RevocationAuthorityID != trust.AuthorityID ||
		revocation.Content.RevocationAuthorityID == revocation.Content.ProofingAuthorityID ||
		revocation.ContentSHA256 != digestString(contentDigest) ||
		revocation.RevocationTrustProfileSHA256 != digestString(trustDigest) ||
		!canonicalRecipientIdentityProofTime(revocation.Content.IssuedAt) ||
		!canonicalRecipientIdentityProofTime(revocation.Content.EffectiveAt) ||
		revocation.Content.IssuedAt.After(now.Add(maximumProofClockSkew)) {
		return verified, errors.New("audit export recipient proof revocation fields do not match")
	}
	if err := verifyRecipientProofRevocationSignature(revocation, trust); err != nil {
		return verified, err
	}
	revocationDigest := sha256.Sum256(encoded)
	return VerifiedRecipientProofRevocation{
		Contract: revocation.Contract, SHA256: digestString(revocationDigest),
		IsolationDomainID: revocation.Content.IsolationDomainID, Scope: revocation.Content.Scope,
		ProofingAuthorityID:          revocation.Content.ProofingAuthorityID,
		ProofingTrustProfileSHA256:   revocation.Content.ProofingTrustProfileSHA256,
		ProofingSigningKeyID:         revocation.Content.ProofingSigningKeyID,
		ReasonSHA256:                 revocation.Content.ReasonSHA256,
		RevocationAuthorityID:        revocation.Content.RevocationAuthorityID,
		RevocationTrustProfileSHA256: revocation.RevocationTrustProfileSHA256,
		RevocationSigningKeyID:       revocation.Signature.KeyID,
		IssuedAt:                     revocation.Content.IssuedAt, EffectiveAt: revocation.Content.EffectiveAt,
	}, nil
}

func readRecipientRevocationTrustProfile(path string) (RecipientRevocationTrustProfile, []byte, error) {
	var trust RecipientRevocationTrustProfile
	encoded, err := readStablePrivateFile(path, maximumControlBytes)
	if err != nil {
		return trust, nil, fmt.Errorf("read audit export recipient revocation trust profile: %w", err)
	}
	defer clear(encoded)
	if err := decodeCanonicalJSON(encoded, &trust, maximumControlBytes); err != nil {
		return trust, nil, errors.New("audit export recipient revocation trust profile is invalid")
	}
	canonical, err := canonicalJSON(trust)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return trust, nil, errors.New("audit export recipient revocation trust profile is not canonical")
	}
	if trust.Contract != RecipientRevocationTrustContract ||
		!auditExportDeliveryRecipientPattern.MatchString(trust.AuthorityID) ||
		!validSortedTrustedKeys(trust.Keys) {
		clear(canonical)
		return trust, nil, errors.New("audit export recipient revocation trust profile fields are invalid")
	}
	return trust, canonical, nil
}

func validRecipientProofRevocationScope(scope, keyID string) bool {
	return (scope == "profile" && keyID == "") ||
		(scope == "key" && keyIDPattern.MatchString(keyID))
}

func verifyRecipientProofRevocationSignature(
	revocation RecipientProofRevocation,
	trust RecipientRevocationTrustProfile,
) error {
	if revocation.Signature.Contract != RecipientProofRevocationSignatureContract ||
		!keyIDPattern.MatchString(revocation.Signature.KeyID) {
		return errors.New("audit export recipient proof revocation signature fields are invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(revocation.Signature.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		base64.RawURLEncoding.EncodeToString(signature) != revocation.Signature.Signature {
		clear(signature)
		return errors.New("audit export recipient proof revocation signature fields are invalid")
	}
	defer clear(signature)
	index := sort.Search(len(trust.Keys), func(index int) bool {
		return trust.Keys[index].KeyID >= revocation.Signature.KeyID
	})
	if index >= len(trust.Keys) || trust.Keys[index].KeyID != revocation.Signature.KeyID {
		return errors.New("audit export recipient proof revocation signing key is not trusted")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(trust.Keys[index].PublicKey)
	if err != nil {
		return errors.New("audit export recipient proof revocation signing key is invalid")
	}
	defer clear(publicKey)
	message, err := recipientProofRevocationSigningMessage(revocation)
	if err != nil {
		return err
	}
	defer clear(message)
	if !ed25519.Verify(publicKey, message, signature) {
		return errors.New("audit export recipient proof revocation signature does not verify")
	}
	return nil
}

func recipientProofRevocationSigningMessage(revocation RecipientProofRevocation) ([]byte, error) {
	fields := recipientProofRevocationSigningFields{
		Contract: revocation.Contract, Content: revocation.Content,
		ContentSHA256:                revocation.ContentSHA256,
		RevocationTrustProfileSHA256: revocation.RevocationTrustProfileSHA256,
		KeyID:                        revocation.Signature.KeyID,
	}
	canonical, err := canonicalJSON(fields)
	if err != nil {
		return nil, errors.New("encode audit export recipient proof revocation signing message")
	}
	message := make([]byte, 0, len(recipientProofRevocationDomain)+len(canonical))
	message = append(message, recipientProofRevocationDomain...)
	message = append(message, canonical...)
	clear(canonical)
	return message, nil
}
