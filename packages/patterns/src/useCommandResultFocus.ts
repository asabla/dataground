import { useEffect, useRef } from "react";

// A completed command can remove its focused submit control. Restore that lost
// focus to its receipt, without interrupting another control or a hidden session.
export function useCommandResultFocus(enabled: boolean, resultKey: string | undefined) {
  const receipt = useRef<HTMLDivElement>(null);
  const previous = useRef(resultKey);
  useEffect(() => {
    const changed = previous.current !== resultKey;
    previous.current = resultKey;
    if (!enabled || !changed || !resultKey || document.activeElement !== document.body) return;
    const element = receipt.current;
    if (element && !element.closest("[hidden], [inert]")) element.focus();
  }, [enabled, resultKey]);
  return receipt;
}
