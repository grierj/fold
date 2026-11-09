import { useEffect, useRef, useState } from "react";
import { useChurchZone } from "./useSite";

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
