package auditseal

import (
	"crypto/sha256"
	"errors"
)

const (
	RevocationAuthorityPurposeRecipientProof   = "recipient-proof"
	RevocationAuthorityPurposeWorkloadIdentity = "workload-identity"
)

type RevocationAuthorityTrustEvidence struct {
	Purpose     string
	Contract    string
	SHA256      string
	AuthorityID string
	KeyIDs      []string
}

func InspectRevocationAuthorityTrustFile(
	purpose string,
	path string,
) (RevocationAuthorityTrustEvidence, error) {
	switch purpose {
	case RevocationAuthorityPurposeRecipientProof:
		trust, canonical, err := readRecipientRevocationTrustProfile(path)
		if err != nil {
			return RevocationAuthorityTrustEvidence{}, err
		}
		defer clear(canonical)
		return newRevocationAuthorityTrustEvidence(
			purpose, trust.Contract, trust.AuthorityID, trust.Keys, canonical,
		), nil
	case RevocationAuthorityPurposeWorkloadIdentity:
		trust, canonical, err := readWorkloadIdentityRevocationTrustProfile(path)
		if err != nil {
			return RevocationAuthorityTrustEvidence{}, err
		}
		defer clear(canonical)
		return newRevocationAuthorityTrustEvidence(
			purpose, trust.Contract, trust.AuthorityID, trust.Keys, canonical,
		), nil
	default:
		return RevocationAuthorityTrustEvidence{}, errors.New("audit export revocation authority purpose is invalid")
	}
}

func newRevocationAuthorityTrustEvidence(
	purpose string,
	contract string,
	authorityID string,
	keys []TrustedKey,
	canonical []byte,
) RevocationAuthorityTrustEvidence {
	digest := sha256.Sum256(canonical)
	keyIDs := make([]string, len(keys))
	for index, key := range keys {
		keyIDs[index] = key.KeyID
	}
	return RevocationAuthorityTrustEvidence{
		Purpose: purpose, Contract: contract, SHA256: digestString(digest),
		AuthorityID: authorityID, KeyIDs: keyIDs,
	}
}
