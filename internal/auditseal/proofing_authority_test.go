package auditseal

import "testing"

func TestInspectProofingAuthorityTrustFilePreservesAuthorityAndKeys(t *testing.T) {
	fixture := newRecipientIdentityProofFixture(t)
	evidence, err := InspectProofingAuthorityTrustFile(fixture.proofingTrustFile)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Contract != RecipientProofingTrustContract ||
		evidence.AuthorityID != fixture.proof.Content.AuthorityID ||
		evidence.SHA256 != fixture.proof.ProofingTrustProfileSHA256 ||
		len(evidence.KeyIDs) != 1 || evidence.KeyIDs[0] != fixture.proof.Signature.KeyID {
		t.Fatalf("proofing authority evidence = %#v", evidence)
	}
}
