package persistence

import (
	"context"
	"strconv"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/identity"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func getRuntimeCommandArtifact(ctx context.Context, querier operationQuerier, scope, invocationID, operationID string, sequence uint64, reference dgruntime.CommandArtifact) (artifact.Record, error) {
	expectedID := identity.Derived("art", scope+":"+invocationID+":runtime-command:"+strconv.FormatUint(sequence, 10))
	if reference.ArtifactID != expectedID {
		return artifact.Record{}, ErrInvocationRuntimeEventInvalid
	}
	record, found, err := findInvocationArtifactRecord(ctx, querier, scope, reference.ArtifactID)
	if err != nil {
		return artifact.Record{}, err
	}
	if !found || record.InvocationID != invocationID || record.OperationID != operationID ||
		record.EffectID != identity.Derived("eff", scope+":"+OperationKindInvocation+":"+operationID+":run-invocation") ||
		record.Digest != reference.Digest || record.SizeBytes != reference.SizeBytes ||
		record.Name != "Completed command output" || record.Kind != "log" || record.MediaType != "text/plain; charset=utf-8" || !record.Sensitive {
		return artifact.Record{}, ErrInvocationRuntimeEventInvalid
	}
	return record, nil
}
