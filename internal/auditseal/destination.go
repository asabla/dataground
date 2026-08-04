package auditseal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"github.com/asabla/dataground/internal/persistence"
)

const DeliveryDestinationContract = "dataground.audit-export-delivery-destination/s3/v1"

var deliveryDestinationObjectKeyPattern = regexp.MustCompile(
	`^audit-export-deliveries/v1/iso_[0-9a-z]{20,32}/adl_[0-9a-z]{20,32}/[0-9a-f]{64}\.json$`,
)

type DeliveryDestination struct {
	Contract          string `json:"contract"`
	DeliveryID        string `json:"deliveryId"`
	IsolationDomainID string `json:"isolationDomainId"`
	RecipientID       string `json:"recipientId"`
	TransportContract string `json:"transportContract"`
	Endpoint          string `json:"endpoint"`
	Bucket            string `json:"bucket"`
	AddressingStyle   string `json:"addressingStyle"`
	ObjectKey         string `json:"objectKey"`
}

type VerifiedDeliveryDestination struct {
	SHA256            [sha256.Size]byte
	TransportContract string
	Endpoint          string
	Bucket            string
	AddressingStyle   string
	ObjectKey         string
}

func VerifyDeliveryDestinationFile(
	path string,
	delivery persistence.AuditExportDelivery,
) (VerifiedDeliveryDestination, error) {
	var verified VerifiedDeliveryDestination
	if delivery.Contract != persistence.AuditExportTransportedDeliveryContract || !delivery.Valid() {
		return verified, errors.New("audit export delivery destination inputs are invalid")
	}
	encoded, err := readStablePrivateFile(path, maximumControlBytes)
	if err != nil {
		return verified, err
	}
	defer clear(encoded)
	var destination DeliveryDestination
	if err := decodeCanonicalJSON(encoded, &destination, maximumControlBytes); err != nil {
		return verified, errors.New("audit export delivery destination is invalid")
	}
	canonical, err := canonicalJSON(destination)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return verified, errors.New("audit export delivery destination is not canonical")
	}
	clear(canonical)
	digest := sha256.Sum256(encoded)
	expectedObjectKey := fmt.Sprintf(
		"audit-export-deliveries/v1/%s/%s/%s.json",
		delivery.IsolationDomainID,
		delivery.DeliveryID,
		hex.EncodeToString(delivery.EncryptedPackageDigest),
	)
	if destination.Contract != DeliveryDestinationContract ||
		destination.DeliveryID != delivery.DeliveryID ||
		destination.IsolationDomainID != delivery.IsolationDomainID ||
		destination.RecipientID != delivery.RecipientID ||
		destination.TransportContract != persistence.AuditExportDeliveryTransportContract ||
		(destination.AddressingStyle != "path" && destination.AddressingStyle != "virtual-hosted") ||
		len(destination.Endpoint) == 0 || len(destination.Endpoint) > 2048 ||
		len(destination.Bucket) == 0 || len(destination.Bucket) > 63 ||
		!deliveryDestinationObjectKeyPattern.MatchString(destination.ObjectKey) ||
		destination.ObjectKey != expectedObjectKey ||
		!bytes.Equal(digest[:], delivery.DestinationDigest) {
		return verified, errors.New("audit export delivery destination fields do not match")
	}
	return VerifiedDeliveryDestination{
		SHA256: digest, TransportContract: destination.TransportContract,
		Endpoint: destination.Endpoint, Bucket: destination.Bucket,
		AddressingStyle: destination.AddressingStyle, ObjectKey: destination.ObjectKey,
	}, nil
}
