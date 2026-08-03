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

	"github.com/asabla/dataground/internal/persistence"
)

var auditExportDeliveryRecipientPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)

const (
	DeliveryReceiptContract          = "dataground.audit-export-delivery-receipt/ed25519/v2"
	DeliveryReceiptSignatureContract = "dataground.audit-export-delivery-receipt-signature/ed25519/v2"
	RecipientTrustContract           = "dataground.audit-export-recipient-trust/ed25519/v1"

	legacyDeliveryReceiptContract          = "dataground.audit-export-delivery-receipt/ed25519/v1"
	legacyDeliveryReceiptSignatureContract = "dataground.audit-export-delivery-receipt-signature/ed25519/v1"
	deliveryReceiptDomain                  = "DataGround audit export delivery receipt v2\n"
	legacyDeliveryReceiptDomain            = "DataGround audit export delivery receipt v1\n"
)

type RecipientTrustProfile struct {
	Contract    string       `json:"contract"`
	RecipientID string       `json:"recipientId"`
	Keys        []TrustedKey `json:"keys"`
}

type DeliveryReceiptSignature struct {
	Contract  string `json:"contract"`
	KeyID     string `json:"keyId"`
	Signature string `json:"signature"`
}

type DeliveryReceiptContent struct {
	DeliveryContract         string    `json:"deliveryContract"`
	DeliveryID               string    `json:"deliveryId"`
	IsolationDomainID        string    `json:"isolationDomainId"`
	ExportKind               string    `json:"exportKind"`
	ExportID                 string    `json:"exportId"`
	EnvelopeSHA256           string    `json:"envelopeSha256"`
	ExportSHA256             string    `json:"exportSha256"`
	ExportTrustProfileSHA256 string    `json:"exportTrustProfileSha256"`
	ExportSigningKeyID       string    `json:"exportSigningKeyId"`
	RecipientID              string    `json:"recipientId"`
	DestinationSHA256        string    `json:"destinationSha256"`
	AcceptedAt               time.Time `json:"acceptedAt"`
}

type DeliveryReceipt struct {
	Contract                    string                   `json:"contract"`
	Content                     DeliveryReceiptContent   `json:"content"`
	ContentSHA256               string                   `json:"contentSha256"`
	RecipientTrustProfileSHA256 string                   `json:"recipientTrustProfileSha256"`
	Signature                   DeliveryReceiptSignature `json:"signature"`
}

type VerifiedDeliveryReceipt struct {
	ReceiptSHA256               [sha256.Size]byte
	DeliveryContract            string
	Contract                    string
	RecipientTrustProfileSHA256 string
	SigningKeyID                string
	AcceptedAt                  time.Time
}

type RecipientTrustEvidence struct {
	Contract    string
	RecipientID string
	SHA256      string
	KeyIDs      []string
}

func InspectRecipientTrustProfileFile(path string) (RecipientTrustEvidence, error) {
	trust, canonical, err := readRecipientTrustProfile(path)
	if err != nil {
		return RecipientTrustEvidence{}, err
	}
	defer clear(canonical)
	digest := sha256.Sum256(canonical)
	keyIDs := make([]string, len(trust.Keys))
	for index, key := range trust.Keys {
		keyIDs[index] = key.KeyID
	}
	return RecipientTrustEvidence{
		Contract:    trust.Contract,
		RecipientID: trust.RecipientID,
		SHA256:      digestString(digest),
		KeyIDs:      keyIDs,
	}, nil
}

type deliveryReceiptSigningFields struct {
	Contract                    string                 `json:"contract"`
	Content                     DeliveryReceiptContent `json:"content"`
	ContentSHA256               string                 `json:"contentSha256"`
	RecipientTrustProfileSHA256 string                 `json:"recipientTrustProfileSha256"`
	KeyID                       string                 `json:"keyId"`
}

func VerifyDeliveryReceiptFile(
	receiptFile string,
	trustProfileFile string,
	delivery persistence.AuditExportDelivery,
) (VerifiedDeliveryReceipt, error) {
	var verified VerifiedDeliveryReceipt
	if !distinctPaths(receiptFile, trustProfileFile) || !delivery.Valid() {
		return verified, errors.New("audit export delivery receipt inputs are invalid")
	}
	encoded, err := readStablePrivateFile(receiptFile, maximumControlBytes)
	if err != nil {
		return verified, fmt.Errorf("read audit export delivery receipt: %w", err)
	}
	defer clear(encoded)
	var receipt DeliveryReceipt
	if err := decodeCanonicalJSON(encoded, &receipt, maximumControlBytes); err != nil {
		return verified, errors.New("audit export delivery receipt is invalid")
	}
	canonicalReceipt, err := canonicalJSON(receipt)
	if err != nil || !bytes.Equal(canonicalReceipt, encoded) {
		clear(canonicalReceipt)
		return verified, errors.New("audit export delivery receipt is not canonical")
	}
	clear(canonicalReceipt)
	trust, canonicalTrust, err := readRecipientTrustProfile(trustProfileFile)
	if err != nil {
		return verified, err
	}
	defer clear(canonicalTrust)
	trustDigest := sha256.Sum256(canonicalTrust)
	content, err := canonicalJSON(receipt.Content)
	if err != nil {
		return verified, errors.New("encode audit export delivery receipt content")
	}
	defer clear(content)
	contentDigest := sha256.Sum256(bytes.TrimSuffix(content, []byte{'\n'}))
	if !validDeliveryReceiptVersion(receipt) ||
		receipt.ContentSHA256 != digestString(contentDigest) ||
		receipt.RecipientTrustProfileSHA256 != digestString(trustDigest) ||
		trust.RecipientID != delivery.RecipientID ||
		!sameDeliveryReceiptContent(receipt.Content, delivery) ||
		!canonicalReceiptTime(receipt.Content.AcceptedAt) {
		return verified, errors.New("audit export delivery receipt fields do not match")
	}
	if err := verifyDeliveryReceiptSignature(receipt, trust); err != nil {
		return verified, err
	}
	return VerifiedDeliveryReceipt{
		ReceiptSHA256:               sha256.Sum256(encoded),
		DeliveryContract:            receipt.Content.DeliveryContract,
		Contract:                    receipt.Contract,
		RecipientTrustProfileSHA256: receipt.RecipientTrustProfileSHA256,
		SigningKeyID:                receipt.Signature.KeyID,
		AcceptedAt:                  receipt.Content.AcceptedAt,
	}, nil
}

func readRecipientTrustProfile(path string) (RecipientTrustProfile, []byte, error) {
	var trust RecipientTrustProfile
	encoded, err := readStablePrivateFile(path, maximumControlBytes)
	if err != nil {
		return trust, nil, fmt.Errorf("read audit export recipient trust profile: %w", err)
	}
	defer clear(encoded)
	if err := decodeCanonicalJSON(encoded, &trust, maximumControlBytes); err != nil {
		return RecipientTrustProfile{}, nil, errors.New("audit export recipient trust profile is invalid")
	}
	canonical, err := canonicalJSON(trust)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return RecipientTrustProfile{}, nil, errors.New("audit export recipient trust profile is not canonical")
	}
	if err := validateRecipientTrustProfile(trust); err != nil {
		clear(canonical)
		return RecipientTrustProfile{}, nil, err
	}
	return trust, canonical, nil
}

func validateRecipientTrustProfile(trust RecipientTrustProfile) error {
	if trust.Contract != RecipientTrustContract ||
		!auditExportDeliveryRecipientPattern.MatchString(trust.RecipientID) ||
		!validSortedTrustedKeys(trust.Keys) {
		return errors.New("audit export recipient trust profile fields are invalid")
	}
	return nil
}

func validSortedTrustedKeys(keys []TrustedKey) bool {
	if len(keys) == 0 || len(keys) > 8 ||
		!sort.SliceIsSorted(keys, func(left, right int) bool {
			return keys[left].KeyID < keys[right].KeyID
		}) {
		return false
	}
	previous := ""
	for _, key := range keys {
		decoded, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
		valid := keyIDPattern.MatchString(key.KeyID) && key.KeyID != previous && err == nil &&
			len(decoded) == ed25519.PublicKeySize &&
			base64.RawURLEncoding.EncodeToString(decoded) == key.PublicKey
		clear(decoded)
		if !valid {
			return false
		}
		previous = key.KeyID
	}
	return true
}

func verifyDeliveryReceiptSignature(receipt DeliveryReceipt, trust RecipientTrustProfile) error {
	expectedSignatureContract := DeliveryReceiptSignatureContract
	if receipt.Contract == legacyDeliveryReceiptContract {
		expectedSignatureContract = legacyDeliveryReceiptSignatureContract
	}
	if receipt.Signature.Contract != expectedSignatureContract ||
		!keyIDPattern.MatchString(receipt.Signature.KeyID) {
		return errors.New("audit export delivery receipt signature fields are invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(receipt.Signature.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		base64.RawURLEncoding.EncodeToString(signature) != receipt.Signature.Signature {
		clear(signature)
		return errors.New("audit export delivery receipt signature fields are invalid")
	}
	defer clear(signature)
	index := sort.Search(len(trust.Keys), func(index int) bool {
		return trust.Keys[index].KeyID >= receipt.Signature.KeyID
	})
	if index >= len(trust.Keys) || trust.Keys[index].KeyID != receipt.Signature.KeyID {
		return errors.New("audit export delivery receipt signing key is not trusted")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(trust.Keys[index].PublicKey)
	if err != nil {
		return errors.New("audit export delivery receipt signing key is invalid")
	}
	defer clear(publicKey)
	message, err := deliveryReceiptSigningMessage(receipt)
	if err != nil {
		return err
	}
	defer clear(message)
	if !ed25519.Verify(publicKey, message, signature) {
		return errors.New("audit export delivery receipt signature does not verify")
	}
	return nil
}

func deliveryReceiptSigningMessage(receipt DeliveryReceipt) ([]byte, error) {
	fields := deliveryReceiptSigningFields{
		Contract: receipt.Contract, Content: receipt.Content, ContentSHA256: receipt.ContentSHA256,
		RecipientTrustProfileSHA256: receipt.RecipientTrustProfileSHA256,
		KeyID:                       receipt.Signature.KeyID,
	}
	canonical, err := canonicalJSON(fields)
	if err != nil {
		return nil, errors.New("encode audit export delivery receipt signing message")
	}
	domain := deliveryReceiptDomain
	if receipt.Contract == legacyDeliveryReceiptContract {
		domain = legacyDeliveryReceiptDomain
	}
	message := make([]byte, 0, len(domain)+len(canonical))
	message = append(message, domain...)
	message = append(message, canonical...)
	clear(canonical)
	return message, nil
}

func sameDeliveryReceiptContent(content DeliveryReceiptContent, delivery persistence.AuditExportDelivery) bool {
	validDeliveryContract := content.DeliveryContract == delivery.Contract ||
		(delivery.Contract == persistence.AuditExportDeliveryContract &&
			content.DeliveryContract == persistence.AuditExportDeliveryReceiptVerifiedContract)
	return validDeliveryContract &&
		content.DeliveryID == delivery.DeliveryID &&
		content.IsolationDomainID == delivery.IsolationDomainID &&
		content.ExportKind == delivery.ExportKind &&
		content.ExportID == delivery.ExportID &&
		content.EnvelopeSHA256 == digestStringFromBytes(delivery.EnvelopeDigest) &&
		content.ExportSHA256 == delivery.ExportSHA256 &&
		content.ExportTrustProfileSHA256 == delivery.TrustProfileSHA256 &&
		content.ExportSigningKeyID == delivery.SigningKeyID &&
		content.RecipientID == delivery.RecipientID &&
		content.DestinationSHA256 == digestStringFromBytes(delivery.DestinationDigest)
}

func validDeliveryReceiptVersion(receipt DeliveryReceipt) bool {
	switch receipt.Contract {
	case DeliveryReceiptContract:
		return receipt.Content.DeliveryContract == persistence.AuditExportDeliveryContract
	case legacyDeliveryReceiptContract:
		return receipt.Content.DeliveryContract == persistence.AuditExportDeliveryReceiptVerifiedContract
	default:
		return false
	}
}

func canonicalReceiptTime(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && offset == 0 && value.Nanosecond()%1000 == 0 && value.Equal(value.UTC())
}

func digestStringFromBytes(value []byte) string {
	if len(value) != sha256.Size {
		return ""
	}
	var digest [sha256.Size]byte
	copy(digest[:], value)
	return digestString(digest)
}
