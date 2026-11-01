import { useEffect, useState } from "react";
import { Site } from "../api";

// usePushEnabled is the church's switch for notifications on devices
// (DEC-124) from /site. Nothing caches /site, so it starts false and turns
// on once /site answers with push on: a control that depends on it appears
// once rather than flashing and then vanishing for a church with push off.
// A failed read turns it on; the server still decides what pushes.
export function usePushEnabled(): boolean {
  const [on, setOn] = useState(false);
  useEffect(() => {
    let alive = true;
    Site.get().then(
      (s) => { if (alive) setOn(s.pushEnabled !== false); },
      () => { if (alive) setOn(true); },
    );
    return () => { alive = false; };
  }, []);
  return on;
}
