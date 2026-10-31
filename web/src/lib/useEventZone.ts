import { useEffect, useRef, useState } from "react";
import { Site } from "../api";

// The church's time zone from /site, or null until it loads.
export function useChurchZone(): string | null {
  return useChurchZoneLoad().zone;
}

// The church's zone and whether /site has answered (done is true after a
// failure too, with zone null), for pages that must wait for the zone
// rather than briefly use the browser's (the composer's schedule).
export function useChurchZoneLoad(): { zone: string | null; done: boolean } {
  const [state, setState] = useState<{ zone: string | null; done: boolean }>({ zone: null, done: false });
  useEffect(() => {
    let alive = true;
    Site.get()
      .then((s) => {
        if (alive) setState({ zone: s.timezone || null, done: true });
      })
      .catch(() => {
        if (alive) setState({ zone: null, done: true });
      });
    return () => {
      alive = false;
    };
  }, []);
  return state;
}

// A new event's zone (DEC-50): the church's zone, not the browser's. Until
// /site answers the field is empty (the server also defaults to the church
// zone); once the person edits it we leave it alone.
export function useEventZone(): { tzid: string; setTzid: (v: string) => void } {
  const church = useChurchZone();
  const [tzid, setRaw] = useState("");
  const touched = useRef(false);
  useEffect(() => {
    if (church && !touched.current) setRaw(church);
  }, [church]);
  return {
    tzid,
    setTzid: (v: string) => {
      touched.current = true;
      setRaw(v);
    },
  };
}
