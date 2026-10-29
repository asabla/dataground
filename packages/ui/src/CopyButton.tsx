import { useEffect, useRef, useState } from "react";
import { Button } from "./Button";

export interface CopyButtonProps {
  label: string;
  value: string;
  isDisabled?: boolean;
  writeText?: (value: string) => Promise<void>;
}

async function writeClipboard(value: string) {
  if (!globalThis.navigator?.clipboard) throw new Error("Clipboard unavailable");
  await navigator.clipboard.writeText(value);
}

export function CopyButton(props: CopyButtonProps) {
  // A changed value starts a fresh feedback session. A late completion must not
  // announce that a newly selected resource was copied.
  return <CopySession key={props.value} {...props} />;
}

function CopySession({ label, value, isDisabled, writeText = writeClipboard }: CopyButtonProps) {
  const [state, setState] = useState<"idle" | "pending" | "copied" | "failed">("idle");
  const sequence = useRef(0);
  const pending = useRef(false);
  useEffect(
    () => () => {
      sequence.current++;
    },
    [],
  );
  const copy = async () => {
    if (isDisabled || pending.current || !value) return;
    pending.current = true;
    const request = ++sequence.current;
    setState("pending");
    try {
      await writeText(value);
      if (sequence.current === request) setState("copied");
    } catch {
      if (sequence.current === request) setState("failed");
    } finally {
      if (sequence.current === request) pending.current = false;
    }
  };
  return (
    <span className="dg-copy-button">
      <Button
        variant="quiet"
        aria-label={`Copy ${label}`}
        isDisabled={isDisabled || !value}
        aria-disabled={state === "pending" || undefined}
        aria-busy={state === "pending" || undefined}
        onPress={() => void copy()}
      >
        {state === "pending" ? "Copying…" : "Copy"}
      </Button>
      <span role="status">{state === "copied" ? `${label} copied.` : ""}</span>
      {state === "failed" && (
        <span role="alert">Could not copy {label}. Select the value and copy it.</span>
      )}
    </span>
  );
}
