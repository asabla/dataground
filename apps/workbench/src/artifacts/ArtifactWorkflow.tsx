import { ArtifactCard } from "@dataground/patterns";
import "@dataground/patterns/styles.css";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import { ArtifactContentWorkflow } from "./ArtifactContentWorkflow";
import {
  type ArtifactFailure,
  type ArtifactResult,
  type InvocationArtifact,
  type InvocationArtifactReference,
  readInvocationArtifact,
} from "./client";

interface ArtifactWorkflowState {
  client: DataGroundClient;
  artifact?: InvocationArtifact;
  error?: ArtifactFailure;
  loading: boolean;
  referenceKey: string;
}

type ArtifactWorkflowAction =
  | { client: DataGroundClient; referenceKey: string; type: "load-started" }
  | {
      client: DataGroundClient;
      referenceKey: string;
      result: ArtifactResult;
      type: "load-finished";
    };

export interface ArtifactWorkflowProps {
  client: DataGroundClient;
  reference: InvocationArtifactReference;
}

export function artifactReferenceKey(reference: InvocationArtifactReference): string {
  return `${reference.isolationDomainId}:${reference.invocationId}:${reference.artifactId}`;
}

export function artifactWorkflowReducer(
  state: ArtifactWorkflowState,
  action: ArtifactWorkflowAction,
): ArtifactWorkflowState {
  switch (action.type) {
    case "load-started":
      if (state.referenceKey !== action.referenceKey || state.client !== action.client) {
        return { client: action.client, loading: true, referenceKey: action.referenceKey };
      }
      return { ...state, error: undefined, loading: true };
    case "load-finished":
      if (state.referenceKey !== action.referenceKey || state.client !== action.client) {
        return state;
      }
      if (!action.result.ok) {
        return { ...state, error: action.result.error, loading: false };
      }
      return {
        client: action.client,
        artifact: action.result.artifact,
        loading: false,
        referenceKey: action.referenceKey,
      };
  }
}

export function ArtifactWorkflow({ client, reference }: ArtifactWorkflowProps) {
  const currentReferenceKey = artifactReferenceKey(reference);
  const [state, setState] = useState<ArtifactWorkflowState>({
    client,
    loading: true,
    referenceKey: currentReferenceKey,
  });
  const requestGeneration = useRef(0);
  const stableReference = useMemo(
    () => ({
      artifactId: reference.artifactId,
      invocationId: reference.invocationId,
      isolationDomainId: reference.isolationDomainId,
    }),
    [reference.artifactId, reference.invocationId, reference.isolationDomainId],
  );

  const dispatch = useCallback((action: ArtifactWorkflowAction) => {
    setState((current) => artifactWorkflowReducer(current, action));
  }, []);

  const refresh = useCallback(async () => {
    const generation = ++requestGeneration.current;
    const referenceKey = artifactReferenceKey(stableReference);
    dispatch({ client, referenceKey, type: "load-started" });
    const result = await readInvocationArtifact(client, stableReference);
    if (requestGeneration.current === generation) {
      dispatch({ client, referenceKey, result, type: "load-finished" });
    }
  }, [client, dispatch, stableReference]);

  useEffect(() => {
    void refresh();
    return () => {
      requestGeneration.current++;
    };
  }, [refresh]);

  const stateMatchesReference =
    state.referenceKey === currentReferenceKey && state.client === client;

  return (
    <>
      <ArtifactCard
        artifact={stateMatchesReference ? state.artifact : undefined}
        error={stateMatchesReference ? state.error : undefined}
        isLoading={!stateMatchesReference || state.loading}
        onRefresh={() => void refresh()}
        reference={reference}
      />
      {stateMatchesReference && !state.loading && !state.error && state.artifact ? (
        <ArtifactContentWorkflow client={client} artifact={state.artifact} />
      ) : null}
    </>
  );
}
