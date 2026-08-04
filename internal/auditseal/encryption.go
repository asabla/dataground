package auditseal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
)

const (
	EncryptedPackageContract = "dataground.audit-export-encrypted-package/x25519-aes256gcm/v1"
	encryptionDomain         = "DataGround audit export encrypted package v1\n"
	maximumEncryptedBytes    = 100 << 20
)

type EncryptedPackage struct {
	Contract                    string `json:"contract"`
	EnvelopeSHA256              string `json:"envelopeSha256"`
	ExportKind                  string `json:"exportKind"`
	ExportID                    string `json:"exportId"`
	IsolationDomainID           string `json:"isolationDomainId"`
	RecipientID                 string `json:"recipientId"`
	RecipientTrustProfileSHA256 string `json:"recipientTrustProfileSha256"`
	EncryptionKeyID             string `json:"encryptionKeyId"`
	EphemeralPublicKey          string `json:"ephemeralPublicKey"`
	Nonce                       string `json:"nonce"`
	Ciphertext                  string `json:"ciphertext"`
}

type encryptedPackageHeader struct {
	Contract                    string `json:"contract"`
	EnvelopeSHA256              string `json:"envelopeSha256"`
	ExportKind                  string `json:"exportKind"`
	ExportID                    string `json:"exportId"`
	IsolationDomainID           string `json:"isolationDomainId"`
	RecipientID                 string `json:"recipientId"`
	RecipientTrustProfileSHA256 string `json:"recipientTrustProfileSha256"`
	EncryptionKeyID             string `json:"encryptionKeyId"`
	EphemeralPublicKey          string `json:"ephemeralPublicKey"`
	Nonce                       string `json:"nonce"`
}

type EncryptRequest struct {
	EnvelopeFile              string
	ExportTrustProfileFile    string
	RecipientTrustProfileFile string
	EncryptionKeyID           string
	OutputFile                string
}

type VerifiedEncryptedPackage struct {
	PackageSHA256               [sha256.Size]byte
	EnvelopeSHA256              string
	ExportKind                  string
	ExportID                    string
	IsolationDomainID           string
	RecipientID                 string
	RecipientTrustProfileSHA256 string
	EncryptionKeyID             string
}

func EncryptFile(request EncryptRequest) error {
	if !distinctPaths(
		request.EnvelopeFile,
		request.ExportTrustProfileFile,
		request.RecipientTrustProfileFile,
		request.OutputFile,
	) || !keyIDPattern.MatchString(request.EncryptionKeyID) {
		return errors.New("audit export encryption inputs are invalid")
	}
	evidence, err := VerifyEvidenceFile(request.EnvelopeFile, request.ExportTrustProfileFile)
	if err != nil {
		return err
	}
	envelope, err := readStablePrivateFile(request.EnvelopeFile, maximumExportBytes+maximumControlBytes)
	if err != nil {
		return fmt.Errorf("read audit export envelope for encryption: %w", err)
	}
	defer clear(envelope)
	if sha256.Sum256(envelope) != evidence.EnvelopeSHA256 {
		return errors.New("audit export envelope changed before encryption")
	}
	trust, canonicalTrust, err := readRecipientTrustProfile(request.RecipientTrustProfileFile)
	if err != nil {
		return err
	}
	defer clear(canonicalTrust)
	if trust.Contract != RecipientEncryptionTrustContract {
		return errors.New("audit export recipient trust profile does not authorize encryption")
	}
	key, err := recipientEncryptionKey(trust, request.EncryptionKeyID)
	if err != nil {
		return err
	}
	publicKeyBytes, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
	if err != nil {
		return errors.New("audit export recipient encryption key is invalid")
	}
	defer clear(publicKeyBytes)
	curve := ecdh.X25519()
	recipientPublicKey, err := curve.NewPublicKey(publicKeyBytes)
	if err != nil {
		return errors.New("audit export recipient encryption key is invalid")
	}
	ephemeralPrivateKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("generate audit export ephemeral encryption key")
	}
	sharedSecret, err := ephemeralPrivateKey.ECDH(recipientPublicKey)
	if err != nil {
		return errors.New("derive audit export encryption secret")
	}
	defer clear(sharedSecret)
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		clear(nonce)
		return errors.New("generate audit export encryption nonce")
	}
	defer clear(nonce)
	trustDigest := sha256.Sum256(canonicalTrust)
	header := encryptedPackageHeader{
		Contract:                    EncryptedPackageContract,
		EnvelopeSHA256:              digestString(evidence.EnvelopeSHA256),
		ExportKind:                  evidence.ExportKind,
		ExportID:                    evidence.ExportID,
		IsolationDomainID:           evidence.IsolationDomainID,
		RecipientID:                 trust.RecipientID,
		RecipientTrustProfileSHA256: digestString(trustDigest),
		EncryptionKeyID:             key.KeyID,
		EphemeralPublicKey:          base64.RawURLEncoding.EncodeToString(ephemeralPrivateKey.PublicKey().Bytes()),
		Nonce:                       base64.RawURLEncoding.EncodeToString(nonce),
	}
	aad, err := canonicalJSON(header)
	if err != nil {
		return errors.New("encode audit export encryption binding")
	}
	defer clear(aad)
	keyBytes, err := deriveEncryptionKey(sharedSecret, sha256.Sum256(aad))
	if err != nil {
		return errors.New("derive audit export encryption key")
	}
	defer clear(keyBytes)
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return errors.New("initialize audit export encryption")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return errors.New("initialize audit export authenticated encryption")
	}
	ciphertext := aead.Seal(nil, nonce, envelope, aad)
	defer clear(ciphertext)
	document := EncryptedPackage{
		Contract:                    header.Contract,
		EnvelopeSHA256:              header.EnvelopeSHA256,
		ExportKind:                  header.ExportKind,
		ExportID:                    header.ExportID,
		IsolationDomainID:           header.IsolationDomainID,
		RecipientID:                 header.RecipientID,
		RecipientTrustProfileSHA256: header.RecipientTrustProfileSHA256,
		EncryptionKeyID:             header.EncryptionKeyID,
		EphemeralPublicKey:          header.EphemeralPublicKey,
		Nonce:                       header.Nonce,
		Ciphertext:                  base64.RawURLEncoding.EncodeToString(ciphertext),
	}
	encoded, err := canonicalJSON(document)
	if err != nil {
		return errors.New("encode audit export encrypted package")
	}
	defer clear(encoded)
	if err := installNewPrivateFile(request.OutputFile, encoded, maximumEncryptedBytes); err != nil {
		return fmt.Errorf("install audit export encrypted package: %w", err)
	}
	verified, err := VerifyEncryptedPackageFile(
		request.OutputFile,
		request.EnvelopeFile,
		request.ExportTrustProfileFile,
		request.RecipientTrustProfileFile,
	)
	if err != nil {
		return fmt.Errorf("verify installed audit export encrypted package: %w", err)
	}
	if verified.EncryptionKeyID != request.EncryptionKeyID ||
		verified.EnvelopeSHA256 != header.EnvelopeSHA256 {
		return errors.New("installed audit export encrypted package does not match request")
	}
	return nil
}

func VerifyEncryptedPackageFile(
	packageFile string,
	envelopeFile string,
	exportTrustProfileFile string,
	recipientTrustProfileFile string,
) (VerifiedEncryptedPackage, error) {
	var verified VerifiedEncryptedPackage
	if !distinctPaths(packageFile, envelopeFile, exportTrustProfileFile, recipientTrustProfileFile) {
		return verified, errors.New("audit export encrypted package paths are invalid")
	}
	evidence, err := VerifyEvidenceFile(envelopeFile, exportTrustProfileFile)
	if err != nil {
		return verified, err
	}
	trust, canonicalTrust, err := readRecipientTrustProfile(recipientTrustProfileFile)
	if err != nil {
		return verified, err
	}
	defer clear(canonicalTrust)
	encoded, err := readStablePrivateFile(packageFile, maximumEncryptedBytes)
	if err != nil {
		return verified, fmt.Errorf("read audit export encrypted package: %w", err)
	}
	defer clear(encoded)
	var document EncryptedPackage
	if err := decodeCanonicalJSON(encoded, &document, maximumEncryptedBytes); err != nil {
		return verified, errors.New("audit export encrypted package is invalid")
	}
	canonical, err := canonicalJSON(document)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return verified, errors.New("audit export encrypted package is not canonical")
	}
	clear(canonical)
	trustDigest := sha256.Sum256(canonicalTrust)
	if trust.Contract != RecipientEncryptionTrustContract ||
		document.Contract != EncryptedPackageContract ||
		document.EnvelopeSHA256 != digestString(evidence.EnvelopeSHA256) ||
		document.ExportKind != evidence.ExportKind || document.ExportID != evidence.ExportID ||
		document.IsolationDomainID != evidence.IsolationDomainID ||
		document.RecipientID != trust.RecipientID ||
		document.RecipientTrustProfileSHA256 != digestString(trustDigest) ||
		!keyIDPattern.MatchString(document.EncryptionKeyID) {
		return verified, errors.New("audit export encrypted package fields do not match")
	}
	if _, err := recipientEncryptionKey(trust, document.EncryptionKeyID); err != nil {
		return verified, err
	}
	ephemeralPublicKey, ephemeralErr := base64.RawURLEncoding.DecodeString(document.EphemeralPublicKey)
	nonce, nonceErr := base64.RawURLEncoding.DecodeString(document.Nonce)
	ciphertext, ciphertextErr := base64.RawURLEncoding.DecodeString(document.Ciphertext)
	validEncoding := ephemeralErr == nil && nonceErr == nil && ciphertextErr == nil &&
		len(ephemeralPublicKey) == 32 && len(nonce) == 12 && len(ciphertext) >= 16 &&
		base64.RawURLEncoding.EncodeToString(ephemeralPublicKey) == document.EphemeralPublicKey &&
		base64.RawURLEncoding.EncodeToString(nonce) == document.Nonce &&
		base64.RawURLEncoding.EncodeToString(ciphertext) == document.Ciphertext
	clear(ephemeralPublicKey)
	clear(nonce)
	clear(ciphertext)
	if !validEncoding {
		return verified, errors.New("audit export encrypted package encoding is invalid")
	}
	return VerifiedEncryptedPackage{
		PackageSHA256:               sha256.Sum256(encoded),
		EnvelopeSHA256:              document.EnvelopeSHA256,
		ExportKind:                  document.ExportKind,
		ExportID:                    document.ExportID,
		IsolationDomainID:           document.IsolationDomainID,
		RecipientID:                 document.RecipientID,
		RecipientTrustProfileSHA256: document.RecipientTrustProfileSHA256,
		EncryptionKeyID:             document.EncryptionKeyID,
	}, nil
}

// ReadEncryptedPackageFile returns an owned copy of the exact stable encrypted
// package bytes after checking the expected digest. Callers use it immediately
// before transport so a post-verification file replacement cannot change the
// externally visible effect.
func ReadEncryptedPackageFile(path string, expected [sha256.Size]byte) ([]byte, error) {
	encoded, err := readStablePrivateFile(path, maximumEncryptedBytes)
	if err != nil {
		return nil, fmt.Errorf("read audit export encrypted package for transport: %w", err)
	}
	if sha256.Sum256(encoded) != expected {
		clear(encoded)
		return nil, errors.New("audit export encrypted package changed before transport")
	}
	return encoded, nil
}

func recipientEncryptionKey(trust RecipientTrustProfile, keyID string) (TrustedKey, error) {
	index := sort.Search(len(trust.EncryptionKeys), func(index int) bool {
		return trust.EncryptionKeys[index].KeyID >= keyID
	})
	if index >= len(trust.EncryptionKeys) || trust.EncryptionKeys[index].KeyID != keyID {
		return TrustedKey{}, errors.New("audit export recipient encryption key is not trusted")
	}
	return trust.EncryptionKeys[index], nil
}

func deriveEncryptionKey(secret []byte, salt [sha256.Size]byte) ([]byte, error) {
	return hkdf.Key(sha256.New, secret, salt[:], encryptionDomain, 32)
}
