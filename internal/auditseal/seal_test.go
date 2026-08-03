package auditseal

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

type sealFixture struct {
	directory     string
	exportFile    string
	trustFile     string
	messageFile   string
	signatureFile string
	envelopeFile  string
	privateKey    ed25519.PrivateKey
	keyID         string
}

func TestSealAndVerifyBothExportKinds(t *testing.T) {
	for _, kind := range []string{AuthorizationExportKind, OperatorExportKind} {
		t.Run(kind, func(t *testing.T) {
			fixture := newSealFixture(t, kind)
			if err := PrepareSigningMessage(PrepareRequest{
				ExportFile:         fixture.exportFile,
				TrustProfileFile:   fixture.trustFile,
				SigningMessageFile: fixture.messageFile,
			}); err != nil {
				t.Fatalf("prepare signing message: %v", err)
			}
			fixture.sign(t)
			if err := Install(InstallRequest{
				ExportFile:       fixture.exportFile,
				SignatureFile:    fixture.signatureFile,
				TrustProfileFile: fixture.trustFile,
				OutputFile:       fixture.envelopeFile,
			}); err != nil {
				t.Fatalf("install envelope: %v", err)
			}
			envelope, err := VerifyFile(fixture.envelopeFile, fixture.trustFile)
			if err != nil {
				t.Fatalf("verify envelope: %v", err)
			}
			if envelope.Contract != EnvelopeContract || envelope.ExportKind != kind ||
				envelope.Signature.KeyID != fixture.keyID {
				t.Fatalf("unexpected envelope: %#v", envelope)
			}
			evidence, err := VerifyEvidenceFile(fixture.envelopeFile, fixture.trustFile)
			if err != nil {
				t.Fatalf("verify delivery evidence: %v", err)
			}
			encodedEnvelope, err := os.ReadFile(fixture.envelopeFile)
			if err != nil {
				t.Fatal(err)
			}
			wantEnvelopeDigest := sha256.Sum256(encodedEnvelope)
			wantExportID := "oax_00000000000000000001"
			if kind == AuthorizationExportKind {
				wantExportID = "aex_00000000000000000001"
			}
			if evidence.EnvelopeSHA256 != wantEnvelopeDigest || evidence.ExportKind != kind ||
				evidence.ExportID != wantExportID ||
				evidence.IsolationDomainID != "iso_00000000000000000001" ||
				evidence.ExportSHA256 != envelope.ExportSHA256 ||
				evidence.TrustProfileSHA256 != envelope.TrustProfileSHA256 ||
				evidence.SigningKeyID != fixture.keyID {
				t.Fatalf("unexpected delivery evidence: %#v", evidence)
			}
			if err := Install(InstallRequest{
				ExportFile:       fixture.exportFile,
				SignatureFile:    fixture.signatureFile,
				TrustProfileFile: fixture.trustFile,
				OutputFile:       fixture.envelopeFile,
			}); err != nil {
				t.Fatalf("replay envelope install: %v", err)
			}
		})
	}
}

func TestSealRejectsSubstitutionAndMalformedInputs(t *testing.T) {
	fixture := newSealFixture(t, AuthorizationExportKind)
	if err := PrepareSigningMessage(PrepareRequest{
		ExportFile:         fixture.exportFile,
		TrustProfileFile:   fixture.trustFile,
		SigningMessageFile: fixture.messageFile,
	}); err != nil {
		t.Fatalf("prepare signing message: %v", err)
	}
	fixture.sign(t)

	t.Run("changed export", func(t *testing.T) {
		encoded, err := os.ReadFile(fixture.exportFile)
		if err != nil {
			t.Fatal(err)
		}
		changed := strings.Replace(string(encoded), "policy.api.v1", "policy.api.v2", 1)
		writePrivate(t, fixture.exportFile, []byte(changed))
		if err := Install(InstallRequest{
			ExportFile:       fixture.exportFile,
			SignatureFile:    fixture.signatureFile,
			TrustProfileFile: fixture.trustFile,
			OutputFile:       fixture.envelopeFile,
		}); err == nil {
			t.Fatal("changed export was accepted")
		}
	})

	t.Run("duplicate member", func(t *testing.T) {
		duplicate := []byte("{\"content\":{},\"content\":{},\"contentSha256\":\"sha256:" + strings.Repeat("0", 64) + "\"}\n")
		writePrivate(t, fixture.exportFile, duplicate)
		if err := PrepareSigningMessage(PrepareRequest{
			ExportFile:         fixture.exportFile,
			TrustProfileFile:   fixture.trustFile,
			SigningMessageFile: filepath.Join(fixture.directory, "duplicate-message"),
		}); err == nil {
			t.Fatal("duplicate JSON member was accepted")
		}
	})

	t.Run("path collision", func(t *testing.T) {
		if err := PrepareSigningMessage(PrepareRequest{
			ExportFile:         fixture.exportFile,
			TrustProfileFile:   fixture.trustFile,
			SigningMessageFile: fixture.exportFile,
		}); err == nil {
			t.Fatal("path collision was accepted")
		}
	})
}

func TestVerifyRejectsTrustAndEnvelopeSubstitution(t *testing.T) {
	fixture := newSealFixture(t, OperatorExportKind)
	if err := PrepareSigningMessage(PrepareRequest{
		ExportFile:         fixture.exportFile,
		TrustProfileFile:   fixture.trustFile,
		SigningMessageFile: fixture.messageFile,
	}); err != nil {
		t.Fatal(err)
	}
	fixture.sign(t)
	if err := Install(InstallRequest{
		ExportFile:       fixture.exportFile,
		SignatureFile:    fixture.signatureFile,
		TrustProfileFile: fixture.trustFile,
		OutputFile:       fixture.envelopeFile,
	}); err != nil {
		t.Fatal(err)
	}

	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	writeCanonicalPrivate(t, fixture.trustFile, TrustProfile{
		Contract: TrustContract,
		Keys: []TrustedKey{{
			KeyID:     fixture.keyID,
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		}},
	})
	if _, err := VerifyFile(fixture.envelopeFile, fixture.trustFile); err == nil {
		t.Fatal("substituted trust profile was accepted")
	}
}

func TestPrepareRejectsInvalidCompletionAndUnsafeDirectory(t *testing.T) {
	t.Run("completion cursor", func(t *testing.T) {
		fixture := newSealFixture(t, OperatorExportKind)
		document := operatorDocument(t)
		document.Content.Complete = false
		document.ContentSHA256 = contentDigest(t, document.Content)
		writeCanonicalPrivate(t, fixture.exportFile, document)
		if err := PrepareSigningMessage(PrepareRequest{
			ExportFile:         fixture.exportFile,
			TrustProfileFile:   fixture.trustFile,
			SigningMessageFile: fixture.messageFile,
		}); err == nil {
			t.Fatal("inconsistent completion cursor was accepted")
		}
	})

	t.Run("directory permissions", func(t *testing.T) {
		fixture := newSealFixture(t, OperatorExportKind)
		if err := os.Chmod(fixture.directory, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := PrepareSigningMessage(PrepareRequest{
			ExportFile:         fixture.exportFile,
			TrustProfileFile:   fixture.trustFile,
			SigningMessageFile: fixture.messageFile,
		}); err == nil {
			t.Fatal("unsafe directory permissions were accepted")
		}
	})
}

func newSealFixture(t *testing.T, kind string) sealFixture {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fixture := sealFixture{
		directory:     directory,
		exportFile:    filepath.Join(directory, "export.json"),
		trustFile:     filepath.Join(directory, "trust.json"),
		messageFile:   filepath.Join(directory, "message.bin"),
		signatureFile: filepath.Join(directory, "signature.json"),
		envelopeFile:  filepath.Join(directory, "envelope.json"),
		privateKey:    privateKey,
		keyID:         "audit_key_01",
	}
	writeCanonicalPrivate(t, fixture.trustFile, TrustProfile{
		Contract: TrustContract,
		Keys: []TrustedKey{{
			KeyID:     fixture.keyID,
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		}},
	})
	switch kind {
	case AuthorizationExportKind:
		writeCanonicalPrivate(t, fixture.exportFile, authorizationDocument(t))
	case OperatorExportKind:
		writeCanonicalPrivate(t, fixture.exportFile, operatorDocument(t))
	default:
		t.Fatalf("unsupported fixture kind %q", kind)
	}
	return fixture
}

func (fixture sealFixture) sign(t *testing.T) {
	t.Helper()
	message, err := os.ReadFile(fixture.messageFile)
	if err != nil {
		t.Fatal(err)
	}
	writeCanonicalPrivate(t, fixture.signatureFile, Signature{
		Contract:  SignatureContract,
		KeyID:     fixture.keyID,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, message)),
	})
}

func authorizationDocument(t *testing.T) persistence.AuthorizationAuditExportDocument {
	t.Helper()
	content := persistence.AuthorizationAuditExportContent{
		SchemaVersion:     persistence.AuthorizationAuditExportSchema,
		ExportID:          "aex_00000000000000000001",
		IsolationDomainID: "iso_00000000000000000001",
		RequestedBy:       "operator@example.invalid",
		CorrelationID:     "cor_00000000000000000001",
		Cursor:            "",
		NextCursor:        authorizationCursor(1, 1, 2, 2),
		Complete:          true,
		Records: []persistence.AuthorizationAuditExportRecord{
			{
				Source:        "api",
				Sequence:      "1",
				RecordedAt:    time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC),
				PrincipalID:   "usr_00000000000000000001",
				PrincipalKind: "human",
				Action:        "readInvocation",
				ResourceType:  "DataGround::Invocation",
				ResourceID:    "inv_00000000000000000001",
				Outcome:       "allowed",
				PolicySetID:   "policy.api.v1",
				PolicyDigest:  "sha256:" + strings.Repeat("1", 64),
				CorrelationID: "cor_00000000000000000002",
			},
			{
				Source:        "invocation",
				Sequence:      "2",
				RecordedAt:    time.Date(2026, 8, 3, 10, 0, 1, 0, time.UTC),
				ActorID:       "operator@example.invalid",
				Action:        "run",
				OperationID:   "op_00000000000000000001",
				InvocationID:  "inv_00000000000000000001",
				ServiceID:     "svc_00000000000000000001",
				RevisionID:    "rev_00000000000000000001",
				Outcome:       "allowed",
				PolicySetID:   "policy.invocation.v1",
				PolicyDigest:  "sha256:" + strings.Repeat("2", 64),
				CorrelationID: "cor_00000000000000000003",
			},
		},
	}
	return persistence.AuthorizationAuditExportDocument{
		Content:       content,
		ContentSHA256: contentDigest(t, content),
	}
}

func operatorDocument(t *testing.T) persistence.OperatorAuditExportDocument {
	t.Helper()
	content := persistence.OperatorAuditExportContent{
		SchemaVersion:     persistence.OperatorAuditExportSchema,
		ExportID:          "oax_00000000000000000001",
		IsolationDomainID: "iso_00000000000000000001",
		RequestedBy:       "operator@example.invalid",
		CorrelationID:     "cor_00000000000000000001",
		Cursor:            "",
		NextCursor:        operatorCursor(1, 1),
		Complete:          true,
		Records: []persistence.OperatorAuditExportRecord{{
			Sequence:      "1",
			AuditID:       "aud_00000000000000000001",
			RecordedAt:    time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC),
			ActorID:       "operator@example.invalid",
			Action:        "oidc-provider-credential.activate",
			ResourceType:  "oidc-provider-credential",
			ResourceID:    "opc_00000000000000000001",
			Outcome:       "succeeded",
			CorrelationID: "cor_00000000000000000002",
			SafeMetadata:  json.RawMessage(`{"endpoint":"jwks","generation":1,"providerId":"primary"}`),
		}},
	}
	return persistence.OperatorAuditExportDocument{
		Content:       content,
		ContentSHA256: contentDigest(t, content),
	}
}

func contentDigest(t *testing.T, content any) string {
	t.Helper()
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func authorizationCursor(apiAfter, apiThrough, invocationAfter, invocationThrough uint64) string {
	encoded := make([]byte, 33)
	encoded[0] = 1
	for index, value := range []uint64{apiAfter, apiThrough, invocationAfter, invocationThrough} {
		binary.BigEndian.PutUint64(encoded[1+index*8:9+index*8], value)
	}
	return "v1." + base64.RawURLEncoding.EncodeToString(encoded)
}

func operatorCursor(after, through uint64) string {
	encoded := make([]byte, 17)
	encoded[0] = 1
	binary.BigEndian.PutUint64(encoded[1:9], after)
	binary.BigEndian.PutUint64(encoded[9:17], through)
	return "v1." + base64.RawURLEncoding.EncodeToString(encoded)
}

func writeCanonicalPrivate(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := canonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	writePrivate(t, path, encoded)
}

func writePrivate(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
