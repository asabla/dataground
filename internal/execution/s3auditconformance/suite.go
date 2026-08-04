package s3auditconformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"github.com/asabla/dataground/internal/audittransport"
	"github.com/asabla/dataground/internal/execution/s3store"
)

var runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Config struct {
	RunID string
}

// Run verifies the audit-export adapter against the same disposable S3
// candidate used by the enforcement and invocation-artifact suites.
func Run(ctx context.Context, store *s3store.Store, config Config) error {
	if ctx == nil || store == nil || !runIDPattern.MatchString(config.RunID) {
		return errors.New("invalid audit-export S3 conformance configuration")
	}
	objects, err := s3store.NewAuditExportStore(store)
	if err != nil {
		return errors.New("invalid audit-export S3 conformance store")
	}
	domainID := "iso_" + config.RunID[:20]
	deliveryID := "adl_" + config.RunID[:20]
	missingDigest := sha256.Sum256([]byte("missing audit export package"))
	missingKey := objectKey(domainID, deliveryID, missingDigest)
	if _, err := objects.OpenAuditExportObject(ctx, missingKey); !errors.Is(
		err, audittransport.ErrObjectMissing,
	) {
		return errors.New("audit-export missing-read conformance failed")
	}

	content := []byte("dataground audit export transport conformance\n")
	digest := sha256.Sum256(content)
	key := objectKey(domainID, deliveryID, digest)
	if err := audittransport.Execute(ctx, objects, key, content, digest); err != nil {
		return errors.New("audit-export immutable-create conformance failed")
	}
	if err := audittransport.Execute(ctx, objects, key, content, digest); err != nil {
		return errors.New("audit-export exact-replay conformance failed")
	}
	return nil
}

func objectKey(domainID string, deliveryID string, digest [sha256.Size]byte) string {
	return fmt.Sprintf(
		"audit-export-deliveries/v1/%s/%s/%s.json",
		domainID,
		deliveryID,
		hex.EncodeToString(digest[:]),
	)
}
