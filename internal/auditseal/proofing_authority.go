package auditseal

import "crypto/sha256"

type ProofingAuthorityTrustEvidence struct {
	Contract    string
	SHA256      string
	AuthorityID string
	KeyIDs      []string
}

func InspectProofingAuthorityTrustFile(path string) (ProofingAuthorityTrustEvidence, error) {
	trust, canonical, err := readRecipientProofingTrustProfile(path)
	if err != nil {
		return ProofingAuthorityTrustEvidence{}, err
	}
	defer clear(canonical)
	digest := sha256.Sum256(canonical)
	keyIDs := make([]string, len(trust.Keys))
	for index, key := range trust.Keys {
		keyIDs[index] = key.KeyID
	}
	return ProofingAuthorityTrustEvidence{
		Contract: trust.Contract, SHA256: digestString(digest),
		AuthorityID: trust.AuthorityID, KeyIDs: keyIDs,
	}, nil
}
