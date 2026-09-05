package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
)

const (
	maximumInvocationAuthorizationPolicyBytes = 1 << 20
	maximumInvocationAuthorizationEntityBytes = authz.MaximumInvocationCedarEntitySnapshotBytes
)

var (
	ErrInvocationAuthorizationPolicyRecordInvalid  = errors.New("invocation authorization policy record is invalid")
	ErrInvocationAuthorizationPolicyRecordConflict = errors.New("invocation authorization policy scope is already bound")
	ErrInvocationAuthorizationPolicyRecordMissing  = errors.New("invocation authorization policy record is missing")
)

type InvocationAuthorizationPolicyRecord struct {
	Contract                  string
	IsolationDomainID         string
	ServiceID                 string
	RevisionID                string
	PolicySetID               string
	PolicyDigest              []byte
	Schema                    []byte
	Policies                  []byte
	Entities                  []byte
	InstalledBy               string
	InstallationCorrelationID string
	ReasonDigest              []byte
}

func (record InvocationAuthorizationPolicyRecord) Valid() bool {
	if record.IsolationDomainID == "" ||
		record.ServiceID == "" ||
		record.RevisionID == "" ||
		record.PolicySetID == "" ||
		len(record.PolicyDigest) != sha256.Size ||
		len(record.Schema) == 0 ||
		len(record.Schema) > maximumInvocationAuthorizationPolicyBytes ||
		len(record.Policies) == 0 ||
		len(record.Policies) > maximumInvocationAuthorizationPolicyBytes ||
		record.InstalledBy == "" ||
		record.InstallationCorrelationID == "" ||
		len(record.ReasonDigest) != sha256.Size {
		return false
	}
	switch record.Contract {
	case "dataground.invocation-authorization-policy/v1":
		digest := invocationAuthorizationPolicyRecordDigest(record.Schema, record.Policies)
		return len(record.Entities) == 0 && bytes.Equal(record.PolicyDigest, digest[:])
	case "dataground.invocation-authorization-policy/v2",
		"dataground.invocation-authorization-policy/v3", "dataground.invocation-authorization-policy/v4":
		if !validInvocationAuthorizationEntityBytes(record.Entities) {
			return false
		}
		var digest [sha256.Size]byte
		if record.Contract == "dataground.invocation-authorization-policy/v4" {
			digest = authz.InvocationAuthorizationPolicyV4Digest(record.Schema, record.Policies, record.Entities)
		} else if record.Contract == "dataground.invocation-authorization-policy/v3" {
			digest = authz.InvocationAuthorizationPolicyV3Digest(
				record.Schema, record.Policies, record.Entities,
			)
		} else {
			digest = authz.InvocationAuthorizationPolicyV2Digest(
				record.Schema, record.Policies, record.Entities,
			)
		}
		return bytes.Equal(record.PolicyDigest, digest[:])
	default:
		return false
	}
}
func (repository *Repository) InstallInvocationAuthorizationPolicy(
	ctx context.Context,
	record InvocationAuthorizationPolicyRecord,
) error {
	if repository == nil || repository.pool == nil || !record.Valid() {
		return ErrInvocationAuthorizationPolicyRecordInvalid
	}
	record = cloneInvocationAuthorizationPolicyRecord(record)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin invocation authorization policy installation: %w", err)
	}
	defer tx.Rollback(ctx)

	result, err := tx.Exec(ctx, `
		INSERT INTO invocation_authorization_policies (
			isolation_domain_id,
			service_id,
			revision_id,
			contract,
			policy_set_id,
			policy_digest,
			cedar_schema,
			cedar_policies,
			cedar_entities,
			installed_by,
			installation_correlation_id,
			reason_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (isolation_domain_id, service_id, revision_id) DO NOTHING
	`,
		record.IsolationDomainID,
		record.ServiceID,
		record.RevisionID,
		record.Contract,
		record.PolicySetID,
		record.PolicyDigest,
		record.Schema,
		record.Policies,
		nullableInvocationAuthorizationEntities(record.Entities),
		record.InstalledBy,
		record.InstallationCorrelationID,
		record.ReasonDigest,
	)
	if err != nil {
		return fmt.Errorf("install invocation authorization policy: %w", err)
	}
	if result.RowsAffected() == 0 {
		existing, err := getInvocationAuthorizationPolicyRecord(
			ctx,
			tx,
			record.IsolationDomainID,
			record.ServiceID,
			record.RevisionID,
		)
		if err != nil {
			return err
		}
		if !sameInvocationAuthorizationPolicyRecord(existing, record) {
			return ErrInvocationAuthorizationPolicyRecordConflict
		}
		return tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id,
			isolation_domain_id,
			actor_id,
			action,
			resource_type,
			resource_id,
			outcome,
			correlation_id,
			safe_metadata,
			occurred_at
		) VALUES (
			$1,
			$2,
			$3,
			'invocation-authorization-policy.install',
			'service-revision',
			$4,
			'accepted',
			$5,
			jsonb_build_object(
				'policySetId', $6::text,
				'policyDigest', $7::text,
				'reasonDigest', $8::text
			),
			clock_timestamp()
		)
	`,
		identity.New("aud"),
		record.IsolationDomainID,
		record.InstalledBy,
		record.RevisionID,
		record.InstallationCorrelationID,
		record.PolicySetID,
		"sha256:"+hex.EncodeToString(record.PolicyDigest),
		"sha256:"+hex.EncodeToString(record.ReasonDigest),
	); err != nil {
		return fmt.Errorf("audit invocation authorization policy installation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit invocation authorization policy installation: %w", err)
	}
	return nil
}

func (repository *Repository) GetInvocationAuthorizationPolicy(
	ctx context.Context,
	isolationDomainID string,
	serviceID string,
	revisionID string,
) (InvocationAuthorizationPolicyRecord, error) {
	if repository == nil || repository.pool == nil ||
		isolationDomainID == "" || serviceID == "" || revisionID == "" {
		return InvocationAuthorizationPolicyRecord{}, ErrInvocationAuthorizationPolicyRecordInvalid
	}
	record, err := getInvocationAuthorizationPolicyRecord(
		ctx,
		repository.pool,
		isolationDomainID,
		serviceID,
		revisionID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationAuthorizationPolicyRecord{}, ErrInvocationAuthorizationPolicyRecordMissing
	}
	return record, err
}

type invocationAuthorizationPolicyQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getInvocationAuthorizationPolicyRecord(
	ctx context.Context,
	querier invocationAuthorizationPolicyQuerier,
	isolationDomainID string,
	serviceID string,
	revisionID string,
) (InvocationAuthorizationPolicyRecord, error) {
	var record InvocationAuthorizationPolicyRecord
	err := querier.QueryRow(ctx, `
		SELECT
			contract,
			isolation_domain_id,
			service_id,
			revision_id,
			policy_set_id,
			policy_digest,
			cedar_schema,
			cedar_policies,
			cedar_entities,
			installed_by,
			installation_correlation_id,
			reason_digest
		FROM invocation_authorization_policies
		WHERE isolation_domain_id = $1
		  AND service_id = $2
		  AND revision_id = $3
	`, isolationDomainID, serviceID, revisionID).Scan(
		&record.Contract,
		&record.IsolationDomainID,
		&record.ServiceID,
		&record.RevisionID,
		&record.PolicySetID,
		&record.PolicyDigest,
		&record.Schema,
		&record.Policies,
		&record.Entities,
		&record.InstalledBy,
		&record.InstallationCorrelationID,
		&record.ReasonDigest,
	)
	if err != nil {
		return InvocationAuthorizationPolicyRecord{}, err
	}
	if !record.Valid() {
		return InvocationAuthorizationPolicyRecord{}, ErrInvocationAuthorizationPolicyRecordInvalid
	}
	return cloneInvocationAuthorizationPolicyRecord(record), nil
}

func validInvocationAuthorizationEntityBytes(encoded []byte) bool {
	_, err := authz.ParseInvocationCedarEntitySnapshot(encoded)
	return err == nil
}
func invocationAuthorizationPolicyRecordDigest(schema []byte, policies []byte) [sha256.Size]byte {
	digest := sha256.New()
	writeInvocationAuthorizationDigestContent(digest, schema)
	writeInvocationAuthorizationDigestContent(digest, policies)
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func writeInvocationAuthorizationDigestContent(digest hash.Hash, content []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(content)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(content)
}

func nullableInvocationAuthorizationEntities(entities []byte) any {
	if len(entities) == 0 {
		return nil
	}
	return entities
}
func cloneInvocationAuthorizationPolicyRecord(
	record InvocationAuthorizationPolicyRecord,
) InvocationAuthorizationPolicyRecord {
	record.PolicyDigest = append([]byte(nil), record.PolicyDigest...)
	record.Schema = append([]byte(nil), record.Schema...)
	record.Policies = append([]byte(nil), record.Policies...)
	record.Entities = append([]byte(nil), record.Entities...)
	record.ReasonDigest = append([]byte(nil), record.ReasonDigest...)
	return record
}

func sameInvocationAuthorizationPolicyRecord(
	left InvocationAuthorizationPolicyRecord,
	right InvocationAuthorizationPolicyRecord,
) bool {
	return left.Contract == right.Contract &&
		left.IsolationDomainID == right.IsolationDomainID &&
		left.ServiceID == right.ServiceID &&
		left.RevisionID == right.RevisionID &&
		left.PolicySetID == right.PolicySetID &&
		bytes.Equal(left.PolicyDigest, right.PolicyDigest) &&
		bytes.Equal(left.Schema, right.Schema) &&
		bytes.Equal(left.Policies, right.Policies) &&
		bytes.Equal(left.Entities, right.Entities) &&
		left.InstalledBy == right.InstalledBy &&
		left.InstallationCorrelationID == right.InstallationCorrelationID &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest)
}
