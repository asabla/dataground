package auditseal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
)

const (
	RecipientIdentityProofContract          = "dataground.audit-export-recipient-identity-proof/ed25519/v1"
	RecipientIdentityProofContentContract   = "dataground.audit-export-recipient-identity-proof/v1"
	RecipientIdentityProofSignatureContract = "dataground.audit-export-recipient-identity-proof-signature/ed25519/v1"
	RecipientProofingTrustContract          = "dataground.audit-export-recipient-proofing-trust/ed25519/v1"

	recipientIdentityProofDomain = "DataGround audit export recipient identity proof v1\n"
	maximumProofClockSkew        = 5 * time.Minute
)

var auditExportIsolationDomainPattern = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)

type RecipientProofingTrustProfile struct {
	Contract    string       `json:"contract"`
	AuthorityID string       `json:"authorityId"`
	Keys        []TrustedKey `json:"keys"`
}

type RecipientIdentityProofContent struct {
	Contract                    string    `json:"contract"`
	IsolationDomainID           string    `json:"isolationDomainId"`
	RecipientID                 string    `json:"recipientId"`
	RecipientTrustProfileSHA256 string    `json:"recipientTrustProfileSha256"`
	EvidenceSHA256              string    `json:"evidenceSha256"`
	AuthorityID                 string    `json:"authorityId"`
	VerifiedAt                  time.Time `json:"verifiedAt"`
	ExpiresAt                   time.Time `json:"expiresAt"`
}

type RecipientIdentityProofSignature struct {
	Contract  string `json:"contract"`
	KeyID     string `json:"keyId"`
	Signature string `json:"signature"`
}

type RecipientIdentityProof struct {
	Contract                   string                          `json:"contract"`
	Content                    RecipientIdentityProofContent   `json:"content"`
	ContentSHA256              string                          `json:"contentSha256"`
	ProofingTrustProfileSHA256 string                          `json:"proofingTrustProfileSha256"`
	Signature                  RecipientIdentityProofSignature `json:"signature"`
}

type VerifiedRecipientIdentityProof struct {
	Contract                    string
	SHA256                      string
	RecipientTrustProfileSHA256 string
	EvidenceSHA256              string
	AuthorityID                 string
	ProofingTrustProfileSHA256  string
	SigningKeyID                string
	VerifiedAt                  time.Time
	ExpiresAt                   time.Time
}

type recipientIdentityProofSigningFields struct {
	Contract                   string                        `json:"contract"`
	Content                    RecipientIdentityProofContent `json:"content"`
	ContentSHA256              string                        `json:"contentSha256"`
	ProofingTrustProfileSHA256 string                        `json:"proofingTrustProfileSha256"`
	KeyID                      string                        `json:"keyId"`
}

func VerifyRecipientIdentityProofFile(
	proofFile string,
	proofingTrustProfileFile string,
	recipientTrust RecipientTrustEvidence,
	isolationDomainID string,
	now time.Time,
) (VerifiedRecipientIdentityProof, error) {
	var verified VerifiedRecipientIdentityProof
	if !distinctPaths(proofFile, proofingTrustProfileFile) ||
		recipientTrust.Contract != RecipientTrustContract ||
		!auditExportIsolationDomainPattern.MatchString(isolationDomainID) ||
		now.IsZero() {
		return verified, errors.New("audit export recipient identity proof inputs are invalid")
	}
	encoded, err := readStablePrivateFile(proofFile, maximumControlBytes)
	if err != nil {
		return verified, fmt.Errorf("read audit export recipient identity proof: %w", err)
	}
	defer clear(encoded)
	var proof RecipientIdentityProof
	if err := decodeCanonicalJSON(encoded, &proof, maximumControlBytes); err != nil {
		return verified, errors.New("audit export recipient identity proof is invalid")
	}
	canonicalProof, err := canonicalJSON(proof)
	if err != nil || !bytes.Equal(canonicalProof, encoded) {
		clear(canonicalProof)
		return verified, errors.New("audit export recipient identity proof is not canonical")
	}
	clear(canonicalProof)
	trust, canonicalTrust, err := readRecipientProofingTrustProfile(proofingTrustProfileFile)
	if err != nil {
		return verified, err
	}
	defer clear(canonicalTrust)
	trustDigest := sha256.Sum256(canonicalTrust)
	content, err := canonicalJSON(proof.Content)
	if err != nil {
		return verified, errors.New("encode audit export recipient identity proof content")
	}
	defer clear(content)
	contentDigest := sha256.Sum256(bytes.TrimSuffix(content, []byte{'\n'}))
	now = now.UTC()
	if proof.Contract != RecipientIdentityProofContract ||
		proof.Content.Contract != RecipientIdentityProofContentContract ||
		proof.Content.IsolationDomainID != isolationDomainID ||
		proof.Content.RecipientID != recipientTrust.RecipientID ||
		proof.Content.RecipientTrustProfileSHA256 != recipientTrust.SHA256 ||
		proof.Content.AuthorityID != trust.AuthorityID ||
		!digestPattern.MatchString(proof.Content.EvidenceSHA256) ||
		proof.ContentSHA256 != digestString(contentDigest) ||
		proof.ProofingTrustProfileSHA256 != digestString(trustDigest) ||
		!canonicalRecipientIdentityProofTime(proof.Content.VerifiedAt) ||
		!canonicalRecipientIdentityProofTime(proof.Content.ExpiresAt) ||
		proof.Content.VerifiedAt.After(now.Add(maximumProofClockSkew)) ||
		!proof.Content.ExpiresAt.After(now) ||
		!proof.Content.ExpiresAt.After(proof.Content.VerifiedAt) {
		return verified, errors.New("audit export recipient identity proof fields do not match")
	}
	if err := verifyRecipientIdentityProofSignature(proof, trust); err != nil {
		return verified, err
	}
	proofDigest := sha256.Sum256(encoded)
	return VerifiedRecipientIdentityProof{
		Contract:                    proof.Contract,
		SHA256:                      digestString(proofDigest),
		RecipientTrustProfileSHA256: proof.Content.RecipientTrustProfileSHA256,
		EvidenceSHA256:              proof.Content.EvidenceSHA256,
		AuthorityID:                 proof.Content.AuthorityID,
		ProofingTrustProfileSHA256:  proof.ProofingTrustProfileSHA256,
		SigningKeyID:                proof.Signature.KeyID,
		VerifiedAt:                  proof.Content.VerifiedAt,
		ExpiresAt:                   proof.Content.ExpiresAt,
	}, nil
}

func readRecipientProofingTrustProfile(path string) (RecipientProofingTrustProfile, []byte, error) {
	var trust RecipientProofingTrustProfile
	encoded, err := readStablePrivateFile(path, maximumControlBytes)
	if err != nil {
		return trust, nil, fmt.Errorf("read audit export recipient proofing trust profile: %w", err)
	}
	defer clear(encoded)
	if err := decodeCanonicalJSON(encoded, &trust, maximumControlBytes); err != nil {
		return trust, nil, errors.New("audit export recipient proofing trust profile is invalid")
	}
	canonical, err := canonicalJSON(trust)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return trust, nil, errors.New("audit export recipient proofing trust profile is not canonical")
	}
	if trust.Contract != RecipientProofingTrustContract ||
		!auditExportDeliveryRecipientPattern.MatchString(trust.AuthorityID) ||
		!validSortedTrustedKeys(trust.Keys) {
		clear(canonical)
		return trust, nil, errors.New("audit export recipient proofing trust profile fields are invalid")
	}
	return trust, canonical, nil
}

func verifyRecipientIdentityProofSignature(
	proof RecipientIdentityProof,
	trust RecipientProofingTrustProfile,
) error {
	if proof.Signature.Contract != RecipientIdentityProofSignatureContract ||
		!keyIDPattern.MatchString(proof.Signature.KeyID) {
		return errors.New("audit export recipient identity proof signature fields are invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(proof.Signature.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		base64.RawURLEncoding.EncodeToString(signature) != proof.Signature.Signature {
		clear(signature)
		return errors.New("audit export recipient identity proof signature fields are invalid")
	}
	defer clear(signature)
	index := sort.Search(len(trust.Keys), func(index int) bool {
		return trust.Keys[index].KeyID >= proof.Signature.KeyID
	})
	if index >= len(trust.Keys) || trust.Keys[index].KeyID != proof.Signature.KeyID {
		return errors.New("audit export recipient identity proof signing key is not trusted")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(trust.Keys[index].PublicKey)
	if err != nil {
		return errors.New("audit export recipient identity proof signing key is invalid")
	}
	defer clear(publicKey)
	message, err := recipientIdentityProofSigningMessage(proof)
	if err != nil {
		return err
	}
	defer clear(message)
	if !ed25519.Verify(publicKey, message, signature) {
		return errors.New("audit export recipient identity proof signature does not verify")
	}
	return nil
}

func recipientIdentityProofSigningMessage(proof RecipientIdentityProof) ([]byte, error) {
	fields := recipientIdentityProofSigningFields{
		Contract: proof.Contract, Content: proof.Content, ContentSHA256: proof.ContentSHA256,
		ProofingTrustProfileSHA256: proof.ProofingTrustProfileSHA256,
		KeyID:                      proof.Signature.KeyID,
	}
	canonical, err := canonicalJSON(fields)
	if err != nil {
		return nil, errors.New("encode audit export recipient identity proof signing message")
	}
	message := make([]byte, 0, len(recipientIdentityProofDomain)+len(canonical))
	message = append(message, recipientIdentityProofDomain...)
	message = append(message, canonical...)
	clear(canonical)
	return message, nil
}

func canonicalRecipientIdentityProofTime(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && offset == 0 && value.Nanosecond()%1000 == 0 && value.Equal(value.UTC())
}
