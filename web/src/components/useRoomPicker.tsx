import { useEffect, useRef } from "react";
import { errorText } from "../lib/errors";
import { useLoad } from "../lib/useLoad";
import { useRoomsEnabled } from "../lib/useSite";
import { Button } from "./ui/Button";
import { FormError } from "./ui/Field";
import { Skeleton } from "./ui/Skeleton";

// What the room pickers share: a church with rooms off, or a site we can't
// read, shows no rooms control at all; the rooms load only while it is on; and a
// reply that lands after the picker is gone changes nothing (`alive`).
export function useRoomPicker<T>(fetch: () => Promise<T>, deps: readonly unknown[]) {
  const roomsOn = useRoomsEnabled();
  const enabled = roomsOn === true;
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);
  const list = useLoad(fetch, deps, { enabled });
  return {
    enabled,
    alive,
    list,
    loading: roomsOn === null || list.loading,
    loadError: list.error ? errorText(list.error, "We couldn't load the rooms.") : null,
  };
}

export function RoomLoadStatus({ loading, error, onRetry }: { loading: boolean; error: string | null; onRetry: () => void }) {
  return <>
    {loading && <Skeleton kind="lines" count={1} label="Loading rooms…" />}
    {error && (
      <div className="space-y-2">
        <FormError>{error}</FormError>
        <Button variant="secondary" onClick={onRetry}>Try again</Button>
      </div>
    )}
  </>;
}
