import { useCallback, useEffect, useRef, useState } from "react";

interface DiscoveryError {
  code: string;
  message: string;
  retryable: boolean;
}
interface Identified {
  metadata: { id: string };
}
type Result<T> =
  | { ok: true; page: { items: T[]; nextCursor?: string } }
  | { ok: false; error: DiscoveryError };
type Reader<T> = (cursor?: string) => Promise<Result<T>>;
interface Page<T> {
  read: Reader<T>;
  request: number;
  items: T[];
  cursors: string[];
  nextCursor?: string;
  loading: boolean;
  error?: DiscoveryError;
}

// The caller memoizes read over every identity, scope and server-filter input.
// Changing that function invalidates rows and actions before its effect starts.
export function useDiscoveryPage<T extends Identified>(read: Reader<T>, stalledMessage: string) {
  const generation = useRef(0),
    busy = useRef(false),
    mounted = useRef(false);
  const currentRead = useRef(read);
  currentRead.current = read;
  const [page, setPage] = useState<Page<T>>({
    read,
    request: 0,
    items: [],
    cursors: [],
    loading: true,
  });
  const latest = useRef(page);
  latest.current = page;
  const load = useCallback(
    async (cursor?: string) => {
      if (!mounted.current || busy.current || currentRead.current !== read) return;
      const request = ++generation.current;
      busy.current = true;
      setPage((previous) => ({
        read,
        request,
        items: cursor === undefined ? [] : previous.items,
        cursors: cursor === undefined ? [] : previous.cursors,
        loading: true,
      }));
      let result: Result<T>;
      try {
        result = await read(cursor);
      } catch {
        result = {
          ok: false,
          error: {
            code: "WORKBENCH_DISCOVERY_UNAVAILABLE",
            message: "The resource list could not be loaded. Refresh to try again.",
            retryable: true,
          },
        };
      }
      if (request !== generation.current || currentRead.current !== read || !mounted.current)
        return;
      busy.current = false;
      setPage((previous) => {
        if (!result.ok)
          return { ...previous, items: [], cursors: [], loading: false, error: result.error };
        const { items, nextCursor } = result.page;
        if (
          (nextCursor !== undefined && previous.cursors.includes(nextCursor)) ||
          items.some((item) => previous.items.some((old) => old.metadata.id === item.metadata.id))
        )
          return {
            ...previous,
            items: [],
            cursors: [],
            loading: false,
            error: {
              code: "WORKBENCH_DISCOVERY_STALLED",
              message: stalledMessage,
              retryable: false,
            },
          };
        return {
          ...previous,
          items: [...previous.items, ...items],
          cursors: nextCursor === undefined ? previous.cursors : [...previous.cursors, nextCursor],
          nextCursor,
          loading: false,
        };
      });
    },
    [read, stalledMessage],
  );
  useEffect(() => {
    mounted.current = true;
    busy.current = false;
    void load();
    return () => {
      mounted.current = false;
      generation.current++;
      busy.current = false;
    };
  }, [load]);
  const current = page.read === read;
  const canAct = () =>
    mounted.current &&
    currentRead.current === read &&
    latest.current === page &&
    page.request === generation.current &&
    current &&
    !page.loading &&
    !page.error &&
    !busy.current;
  return {
    items: current ? page.items : [],
    loading: !current || page.loading,
    error: current ? page.error : undefined,
    nextCursor: current ? page.nextCursor : undefined,
    load,
    canAct,
  };
}
