package persistence

import (
	"context"
	"strconv"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/identity"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func getRuntimeMessageArtifact(ctx context.Context, querier operationQuerier, scope, invocationID, operationID string, sequence uint64, reference dgruntime.MessageArtifact) (artifact.Record, error) {
	expectedID := identity.Derived("art", scope+":"+invocationID+":runtime-message:"+strconv.FormatUint(sequence, 10))
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
		record.Name != "Completed runtime message" || record.Kind != "file" || record.MediaType != "text/plain; charset=utf-8" || !record.Sensitive {
		return artifact.Record{}, ErrInvocationRuntimeEventInvalid
	}
	return record, nil
}
