package persistence

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/asabla/dataground/internal/artifact"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func verifyRuntimeArtifactResult(ctx context.Context, querier operationQuerier, claim OperationClaim, encoded []byte) error {
	var result struct {
		SchemaVersion  string `json:"schemaVersion"`
		Status         string `json:"status"`
		OutputArtifact struct {
			ArtifactID string `json:"artifactId"`
			Digest     string `json:"digest"`
			SizeBytes  int64  `json:"sizeBytes"`
			MediaType  string `json:"mediaType"`
		} `json:"outputArtifact"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || result.SchemaVersion != "dataground.invocation-artifact-result/v1" || result.Status != "succeeded" || result.OutputArtifact.MediaType != "text/plain; charset=utf-8" {
		return ErrInvocationRuntimeAttemptInvalid
	}
	var sequence uint64
	var payload []byte
	err := querier.QueryRow(ctx, `
 SELECT source_sequence, payload FROM invocation_events AS event
 WHERE isolation_domain_id=$1 AND invocation_id=$2 AND source_kind='runtime'
   AND event_type='output.message.artifact' AND payload->>'phase' <> 'commentary'
   AND payload->>'artifactId'=$3
   AND NOT EXISTS (SELECT 1 FROM invocation_events AS later
       WHERE later.isolation_domain_id=event.isolation_domain_id AND later.invocation_id=event.invocation_id
         AND later.source_kind='runtime' AND
           (later.event_type IN ('lifecycle.failed', 'lifecycle.cancelled') OR
            (later.source_sequence > event.source_sequence AND later.event_type IN ('output.message.completed','output.message.artifact') AND later.payload->>'phase' <> 'commentary')))
   AND EXISTS (SELECT 1 FROM invocation_events AS terminal
       WHERE terminal.isolation_domain_id=event.isolation_domain_id AND terminal.invocation_id=event.invocation_id
         AND terminal.source_kind='runtime' AND terminal.event_type='lifecycle.succeeded' AND terminal.source_sequence > event.source_sequence)
 `, claim.IsolationDomainID, claim.ResourceID, result.OutputArtifact.ArtifactID).Scan(&sequence, &payload)
	if err != nil {
		return ErrInvocationRuntimeAttemptConflict
	}
	var source map[string]any
	if json.Unmarshal(payload, &source) != nil {
		return ErrInvocationRuntimeAttemptConflict
	}
	reference, err := dgruntime.ParseMessageArtifact(source)
	if err != nil || reference.Digest != result.OutputArtifact.Digest || reference.SizeBytes != result.OutputArtifact.SizeBytes {
		return artifact.ErrInvocationArtifactConflict
	}
	_, err = getRuntimeMessageArtifact(ctx, querier, claim.IsolationDomainID, claim.ResourceID, claim.ID, sequence, reference)
	return err
}
