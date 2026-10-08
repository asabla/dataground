import { ArtifactContent } from "@dataground/patterns";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import type { ArtifactFailure, InvocationArtifact } from "./client";
import { canReadArtifactContent, readArtifactContent } from "./content";

export function ArtifactContentWorkflow({
  client,
  artifact,
}: {
  client: DataGroundClient;
  artifact: InvocationArtifact;
}) {
  const owner = useMemo(() => ({ client, artifact }), [client, artifact]);
  const active = useRef<{ generation: number; controller?: AbortController; url?: string }>({
    generation: 0,
  });
  const [state, setState] = useState<{
    owner: typeof owner;
    loading: boolean;
    error?: ArtifactFailure;
    download?: { url: string; filename: string };
    text?: string;
    previewMessage?: string;
  }>({ owner, loading: false });

  const release = useCallback(() => {
    active.current.generation++;
    active.current.controller?.abort();
    active.current.controller = undefined;
    if (active.current.url) URL.revokeObjectURL(active.current.url);
    active.current.url = undefined;
  }, []);
  useEffect(() => {
    // Identity and descriptor replacement invalidate both in-flight work and
    // retained content. Render also checks ownership before the effect runs.
    release();
    setState({ owner, loading: false });
    return release;
  }, [owner, release]);

  const visible = state.owner === owner ? state : undefined;
  function hide() {
    release();
    setState({ owner, loading: false });
  }
  async function read() {
    if (active.current.controller || !canReadArtifactContent(artifact)) return;
    release();
    const generation = active.current.generation;
    const controller = new AbortController();
    active.current.controller = controller;
    setState({ owner, loading: true });
    const result = await readArtifactContent(
      client,
      {
        isolationDomainId: artifact.metadata.isolationDomainId,
        invocationId: artifact.invocationId,
        artifactId: artifact.metadata.id,
      },
      artifact,
      controller.signal,
    );
    if (active.current.generation !== generation) {
      if (result.ok) result.bytes.fill(0);
      return;
    }
    active.current.controller = undefined;
    if (!result.ok) {
      setState({ owner, loading: false, error: result.error });
      return;
    }
    try {
      // A Blob copies the verified bytes. The link never uses a storage URL,
      // user-controlled filename, or an executable MIME type.
      const url = URL.createObjectURL(
        new Blob([result.bytes], { type: "application/octet-stream" }),
      );
      active.current.url = url;
      setState({
        owner,
        loading: false,
        download: { url, filename: `${artifact.metadata.id}.bin` },
        text: result.text,
        previewMessage: result.previewMessage,
      });
    } catch {
      setState({
        owner,
        loading: false,
        error: {
          code: "WORKBENCH_ARTIFACT_CONTENT_UNAVAILABLE",
          message: "The verified file could not be prepared for download.",
          retryable: true,
        },
      });
    } finally {
      result.bytes.fill(0);
    }
  }
  return (
    <ArtifactContent
      download={visible?.download}
      error={visible?.error}
      isLoading={visible?.loading}
      text={visible?.text}
      previewMessage={visible?.previewMessage}
      onRead={() => void read()}
      onHide={hide}
      unavailableMessage={
        canReadArtifactContent(artifact)
          ? undefined
          : artifact.state === "available"
            ? "Files larger than 16 MiB cannot be read in this view."
            : "Content can be read only when the artifact is available."
      }
    />
  );
}
