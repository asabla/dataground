package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const InvocationAuthorizationPolicyWithdrawalContract = "dataground.invocation-authorization-policy-withdrawal/v1"

var (
	ErrInvocationAuthorizationPolicyWithdrawalInvalid = errors.New(
		"invocation authorization policy withdrawal is invalid",
	)
	ErrInvocationAuthorizationPolicyWithdrawalConflict = errors.New(
		"invocation authorization policy withdrawal conflicts with durable state",
	)
	ErrInvocationAuthorizationPolicyWithdrawalDigestMismatch = errors.New(
		"invocation authorization policy withdrawal digest does not match",
	)
	invocationPolicyWithdrawalDomainPattern = regexp.MustCompile(
		`^iso_[0-9a-z]{20,32}$`,
	)
	invocationPolicyWithdrawalServicePattern = regexp.MustCompile(
		`^svc_[0-9a-z]{20,32}$`,
	)
	invocationPolicyWithdrawalRevisionPattern = regexp.MustCompile(
		`^rev_[0-9a-z]{20,32}$`,
	)
	invocationPolicyWithdrawalActorPattern = regexp.MustCompile(
		`^[a-z][a-z0-9_-]{2,127}$`,
	)
	invocationPolicyWithdrawalCorrelationPattern = regexp.MustCompile(
		`^cor_[0-9a-z]{20,32}$`,
	)
)

type InvocationAuthorizationPolicyWithdrawal struct {
	Contract          string
	IsolationDomainID string
	ServiceID         string
	RevisionID        string
	PolicyDigest      []byte
	WithdrawnBy       string
	ReasonDigest      []byte
	CorrelationID     string
}

func (withdrawal InvocationAuthorizationPolicyWithdrawal) Valid() bool {
	return withdrawal.Contract == InvocationAuthorizationPolicyWithdrawalContract &&
		invocationPolicyWithdrawalDomainPattern.MatchString(withdrawal.IsolationDomainID) &&
		invocationPolicyWithdrawalServicePattern.MatchString(withdrawal.ServiceID) &&
		invocationPolicyWithdrawalRevisionPattern.MatchString(withdrawal.RevisionID) &&
		len(withdrawal.PolicyDigest) == sha256.Size &&
		invocationPolicyWithdrawalActorPattern.MatchString(withdrawal.WithdrawnBy) &&
		len(withdrawal.ReasonDigest) == sha256.Size &&
		invocationPolicyWithdrawalCorrelationPattern.MatchString(withdrawal.CorrelationID)
}

func (repository *Repository) WithdrawInvocationAuthorizationPolicy(
	ctx context.Context,
	withdrawal InvocationAuthorizationPolicyWithdrawal,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !withdrawal.Valid() {
		return ErrInvocationAuthorizationPolicyWithdrawalInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	withdrawal = cloneInvocationAuthorizationPolicyWithdrawal(withdrawal)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin invocation authorization policy withdrawal: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	`, "invocation-authorization-policy-withdrawal\n"+
		withdrawal.IsolationDomainID+"\n"+withdrawal.ServiceID+"\n"+withdrawal.RevisionID); err != nil {
		return fmt.Errorf("lock invocation authorization policy withdrawal: %w", err)
	}
	existing, exists, err := readInvocationAuthorizationPolicyWithdrawal(
		ctx,
		tx,
		withdrawal.IsolationDomainID,
		withdrawal.ServiceID,
		withdrawal.RevisionID,
	)
	if err != nil {
		return err
	}
	if exists {
		if !sameInvocationAuthorizationPolicyWithdrawal(existing, withdrawal) {
			return ErrInvocationAuthorizationPolicyWithdrawalConflict
		}
		return tx.Commit(ctx)
	}
	policy, err := getInvocationAuthorizationPolicyRecord(
		ctx,
		tx,
		withdrawal.IsolationDomainID,
		withdrawal.ServiceID,
		withdrawal.RevisionID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvocationAuthorizationPolicyRecordMissing
	}
	if err != nil {
		return fmt.Errorf("read policy for withdrawal: %w", err)
	}
	if !bytes.Equal(policy.PolicyDigest, withdrawal.PolicyDigest) {
		return ErrInvocationAuthorizationPolicyWithdrawalDigestMismatch
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invocation_authorization_policy_withdrawals (
			contract,
			isolation_domain_id,
			service_id,
			revision_id,
			policy_digest,
			withdrawn_by,
			reason_digest,
			correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`,
		withdrawal.Contract,
		withdrawal.IsolationDomainID,
		withdrawal.ServiceID,
		withdrawal.RevisionID,
		withdrawal.PolicyDigest,
		withdrawal.WithdrawnBy,
		withdrawal.ReasonDigest,
		withdrawal.CorrelationID,
	); err != nil {
		return mapInvocationAuthorizationPolicyWithdrawalWriteError(err)
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
			'invocation-authorization-policy.withdraw',
			'service-revision',
			$4,
			'accepted',
			$5,
			jsonb_build_object(
				'policyDigest', $6::text,
				'reasonDigest', $7::text
			),
			clock_timestamp()
		)
	`,
		identity.New("aud"),
		withdrawal.IsolationDomainID,
		withdrawal.WithdrawnBy,
		withdrawal.RevisionID,
		withdrawal.CorrelationID,
		"sha256:"+hex.EncodeToString(withdrawal.PolicyDigest),
		"sha256:"+hex.EncodeToString(withdrawal.ReasonDigest),
	); err != nil {
		return fmt.Errorf("audit invocation authorization policy withdrawal: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit invocation authorization policy withdrawal: %w", err)
	}
	return nil
}

func (repository *Repository) GetActiveInvocationAuthorizationPolicy(
	ctx context.Context,
	isolationDomainID string,
	serviceID string,
	revisionID string,
) (InvocationAuthorizationPolicyRecord, error) {
	if repository == nil || repository.pool == nil || ctx == nil ||
		isolationDomainID == "" || serviceID == "" || revisionID == "" {
		return InvocationAuthorizationPolicyRecord{}, ErrInvocationAuthorizationPolicyRecordInvalid
	}
	var record InvocationAuthorizationPolicyRecord
	err := repository.pool.QueryRow(ctx, `
		SELECT
			policy.contract,
			policy.isolation_domain_id,
			policy.service_id,
			policy.revision_id,
			policy.policy_set_id,
			policy.policy_digest,
			policy.cedar_schema,
			policy.cedar_policies,
			policy.cedar_entities,
			policy.installed_by,
			policy.installation_correlation_id,
			policy.reason_digest
		FROM invocation_authorization_policies AS policy
		WHERE policy.isolation_domain_id = $1
		  AND policy.service_id = $2
		  AND policy.revision_id = $3
		  AND NOT EXISTS (
			SELECT 1
			FROM invocation_authorization_policy_withdrawals AS withdrawal
			WHERE withdrawal.isolation_domain_id = policy.isolation_domain_id
			  AND withdrawal.service_id = policy.service_id
			  AND withdrawal.revision_id = policy.revision_id
		  )
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
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationAuthorizationPolicyRecord{}, ErrInvocationAuthorizationPolicyRecordMissing
	}
	if err != nil {
		return InvocationAuthorizationPolicyRecord{}, err
	}
	if !record.Valid() {
		return InvocationAuthorizationPolicyRecord{}, ErrInvocationAuthorizationPolicyRecordInvalid
	}
	return cloneInvocationAuthorizationPolicyRecord(record), nil
}

type invocationAuthorizationPolicyWithdrawalQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readInvocationAuthorizationPolicyWithdrawal(
	ctx context.Context,
	querier invocationAuthorizationPolicyWithdrawalQuerier,
	isolationDomainID string,
	serviceID string,
	revisionID string,
) (InvocationAuthorizationPolicyWithdrawal, bool, error) {
	var withdrawal InvocationAuthorizationPolicyWithdrawal
	err := querier.QueryRow(ctx, `
		SELECT
			contract,
			isolation_domain_id,
			service_id,
			revision_id,
			policy_digest,
			withdrawn_by,
			reason_digest,
			correlation_id
		FROM invocation_authorization_policy_withdrawals
		WHERE isolation_domain_id = $1
		  AND service_id = $2
		  AND revision_id = $3
	`, isolationDomainID, serviceID, revisionID).Scan(
		&withdrawal.Contract,
		&withdrawal.IsolationDomainID,
		&withdrawal.ServiceID,
		&withdrawal.RevisionID,
		&withdrawal.PolicyDigest,
		&withdrawal.WithdrawnBy,
		&withdrawal.ReasonDigest,
		&withdrawal.CorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationAuthorizationPolicyWithdrawal{}, false, nil
	}
	if err != nil {
		return InvocationAuthorizationPolicyWithdrawal{}, false,
			fmt.Errorf("read invocation authorization policy withdrawal: %w", err)
	}
	if !withdrawal.Valid() {
		return InvocationAuthorizationPolicyWithdrawal{}, false,
			ErrInvocationAuthorizationPolicyWithdrawalConflict
	}
	return cloneInvocationAuthorizationPolicyWithdrawal(withdrawal), true, nil
}

func cloneInvocationAuthorizationPolicyWithdrawal(
	withdrawal InvocationAuthorizationPolicyWithdrawal,
) InvocationAuthorizationPolicyWithdrawal {
	withdrawal.PolicyDigest = append([]byte(nil), withdrawal.PolicyDigest...)
	withdrawal.ReasonDigest = append([]byte(nil), withdrawal.ReasonDigest...)
	return withdrawal
}

func sameInvocationAuthorizationPolicyWithdrawal(
	left InvocationAuthorizationPolicyWithdrawal,
	right InvocationAuthorizationPolicyWithdrawal,
) bool {
	return left.Contract == right.Contract &&
		left.IsolationDomainID == right.IsolationDomainID &&
		left.ServiceID == right.ServiceID &&
		left.RevisionID == right.RevisionID &&
		bytes.Equal(left.PolicyDigest, right.PolicyDigest) &&
		left.WithdrawnBy == right.WithdrawnBy &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest) &&
		left.CorrelationID == right.CorrelationID
}

func mapInvocationAuthorizationPolicyWithdrawalWriteError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) &&
		(databaseError.Code == "23505" || databaseError.Code == "23503" ||
			databaseError.Code == "P0001") {
		return ErrInvocationAuthorizationPolicyWithdrawalConflict
	}
	return fmt.Errorf("withdraw invocation authorization policy: %w", err)
}
