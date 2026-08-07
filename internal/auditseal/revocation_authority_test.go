package auditseal

import "testing"

func TestInspectRevocationAuthorityTrustFilePreservesPurposeAndKeys(t *testing.T) {
	recipientFixture := newRecipientProofRevocationFixture(t, "profile")
	recipient, err := InspectRevocationAuthorityTrustFile(
		RevocationAuthorityPurposeRecipientProof, recipientFixture.trustFile,
	)
	if err != nil {
		t.Fatal(err)
	}
	if recipient.Purpose != RevocationAuthorityPurposeRecipientProof ||
		recipient.Contract != RecipientRevocationTrustContract ||
		recipient.AuthorityID != "archive-revocation.primary" ||
		len(recipient.KeyIDs) != 1 || recipient.KeyIDs[0] != "revocation_key_01" ||
		recipient.SHA256 != recipientFixture.revocation.RevocationTrustProfileSHA256 {
		t.Fatalf("recipient authority evidence = %#v", recipient)
	}

	workloadFixture := newWorkloadIdentityRevocationFixture(t, "profile")
	workload, err := InspectRevocationAuthorityTrustFile(
		RevocationAuthorityPurposeWorkloadIdentity, workloadFixture.trustFile,
	)
	if err != nil {
		t.Fatal(err)
	}
	if workload.Purpose != RevocationAuthorityPurposeWorkloadIdentity ||
		workload.Contract != WorkloadIdentityRevocationTrustContract ||
		workload.AuthorityID != "archive-revocation.primary" ||
		len(workload.KeyIDs) != 1 || workload.KeyIDs[0] != "revocation_key_01" ||
		workload.SHA256 != workloadFixture.revocation.RevocationTrustProfileSHA256 {
		t.Fatalf("workload authority evidence = %#v", workload)
	}
	if _, err := InspectRevocationAuthorityTrustFile("other", workloadFixture.trustFile); err == nil {
		t.Fatal("unknown revocation authority purpose was accepted")
	}
}
