package workspace

import (
	"context"
	"errors"
	"io"
	"regexp"
	"time"
)

const (
	SourceGrantV1              = "dataground.workspace-source-grant/v1"
	SourceUseV1                = "dataground.workspace-source-use/v1"
	SourceReadAdmission        = "read-admission"
	SourceReadDelivery         = "read-delivery"
	MaximumSourceGrantDuration = time.Hour
)

var (
	ErrSourceUnauthorized  = errors.New("workspace source use is not authorized")
	sourceRevisionPattern  = regexp.MustCompile(`^rev_[0-9a-z]{20,32}$`)
	sourceOperationPattern = regexp.MustCompile(`^op_[0-9a-z]{20,32}$`)
)

// SourceGrantChange is administrative authority for a revision to read one
// immutable source. It does not replace the initiating principal's Cedar decision.
type SourceGrantChange struct {
	Contract      string
	Record        SourceRecord
	RevisionID    string
	Generation    int64
	Operation     string
	ActivatedAt   time.Time
	ExpiresAt     time.Time
	ActorID       string
	ReasonDigest  string
	CorrelationID string
}

func (v SourceGrantChange) Valid() bool {
	if v.Contract != SourceGrantV1 || !ValidSourceInstallation(SourceInstallation{Record: v.Record, ActorID: v.ActorID, CorrelationID: v.CorrelationID}) || !sourceRevisionPattern.MatchString(v.RevisionID) || v.Generation < 1 || !sourceDigestPattern.MatchString(v.ReasonDigest) {
		return false
	}
	switch v.Operation {
	case "revoke":
		return v.ActivatedAt.IsZero() && v.ExpiresAt.IsZero()
	case "activate":
		return validSourceTime(v.ActivatedAt) && validSourceTime(v.ExpiresAt) && v.ExpiresAt.After(v.ActivatedAt) && v.ExpiresAt.Sub(v.ActivatedAt) <= MaximumSourceGrantDuration
	default:
		return false
	}
}
func validSourceTime(t time.Time) bool {
	_, offset := t.Zone()
	return !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999 && offset == 0 && t.Nanosecond()%1000 == 0
}

// SourceUse is supplied by a trusted controller with independently validated
// actor, operation and revision authority. Grant approval alone is not a lease.
type SourceUse struct {
	Contract        string
	Record          SourceRecord
	RevisionID      string
	OperationID     string
	GrantGeneration int64
	ActorID         string
	CorrelationID   string
}

func (v SourceUse) Valid() bool {
	return v.Contract == SourceUseV1 && ValidSourceInstallation(SourceInstallation{Record: v.Record, ActorID: v.ActorID, CorrelationID: v.CorrelationID}) && sourceRevisionPattern.MatchString(v.RevisionID) && sourceOperationPattern.MatchString(v.OperationID) && v.GrantGeneration > 0
}

type SourcePermit struct {
	Generation int64
	ExpiresAt  time.Time
}
type SourceAuthorizer interface {
	AuthorizeWorkspaceSource(context.Context, SourceUse, string) (SourcePermit, error)
}

// ReadSource returns a validated private snapshot only after two durable grant
// decisions. The caller still owns Cedar authorization and any later provisioning
// effect; this result does not authorize filesystem mutation or sandbox creation.
func ReadSource(ctx context.Context, catalog SourceCatalog, objects SourceObjectReader, authorizer SourceAuthorizer, use SourceUse) (*Snapshot, error) {
	if ctx == nil || catalog == nil || objects == nil || authorizer == nil || !use.Valid() {
		return nil, ErrSourceInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	permit, err := authorizer.AuthorizeWorkspaceSource(ctx, use, SourceReadAdmission)
	if err != nil {
		return nil, err
	}
	if permit.Generation != use.GrantGeneration || !validSourceTime(permit.ExpiresAt) || !time.Now().Before(permit.ExpiresAt) {
		return nil, ErrSourceUnauthorized
	}
	readCtx, cancel := context.WithDeadline(ctx, permit.ExpiresAt)
	defer cancel()
	if objects.WorkspaceSourceStorageDigest() != use.Record.StorageProfileDigest {
		return nil, ErrSourceConflict
	}
	entry, err := catalog.GetWorkspaceSource(readCtx, use.Record.IsolationDomainID, use.Record.WorkspaceID, use.Record.ID)
	if err != nil {
		return nil, err
	}
	if !entry.Ready || entry.Installation.Record != use.Record {
		return nil, ErrSourceConflict
	}
	key, _ := SourceObjectKey(use.Record)
	reader, err := objects.OpenWorkspaceSource(readCtx, key)
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, ErrSourceUnavailable
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, use.Record.ArchiveBytes+1))
	closeErr := reader.Close()
	defer clear(data)
	if err := readCtx.Err(); err != nil {
		return nil, err
	}
	if readErr != nil || closeErr != nil {
		return nil, ErrSourceUnavailable
	}
	snapshot, err := Inspect(data, use.Record.ArchiveDigest)
	if err != nil || !sourceMatches(use.Record, snapshot, len(data)) {
		return nil, ErrSourceConflict
	}
	deliveredOK := false
	defer func() {
		if !deliveredOK {
			for _, entry := range snapshot.entries {
				clear(entry.content)
			}
		}
	}()
	current, err := catalog.GetWorkspaceSource(readCtx, use.Record.IsolationDomainID, use.Record.WorkspaceID, use.Record.ID)
	if err != nil {
		return nil, err
	}
	if current != entry {
		return nil, ErrSourceConflict
	}
	delivered, err := authorizer.AuthorizeWorkspaceSource(readCtx, use, SourceReadDelivery)
	if err != nil {
		return nil, err
	}
	if delivered.Generation != permit.Generation || !delivered.ExpiresAt.Equal(permit.ExpiresAt) || !time.Now().Before(delivered.ExpiresAt) {
		return nil, ErrSourceUnauthorized
	}
	if err := readCtx.Err(); err != nil {
		return nil, err
	}
	deliveredOK = true
	return snapshot, nil
}
