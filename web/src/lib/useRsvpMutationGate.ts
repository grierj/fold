import { useCallback, useMemo, useRef, useState } from "react";
import { flushSync } from "react-dom";

export type RsvpMutationGate = {
  busy: boolean;
  acquire: () => (() => void) | null;
};

export function useRsvpMutationGate(): RsvpMutationGate {
  const held = useRef(false);
  const [busy, setBusy] = useState(false);
  const acquire = useCallback(() => {
    // The ref closes the same-tick gap before React can render disabled controls.
    if (held.current) return null;
    held.current = true;
    setBusy(true);
    let released = false;
    return () => {
      if (released) return;
      released = true;
      held.current = false;
      // Flush pending authoritative response state before controls become usable.
      flushSync(() => setBusy(false));
    };
  }, []);
  return useMemo(() => ({ busy, acquire }), [acquire, busy]);
}
