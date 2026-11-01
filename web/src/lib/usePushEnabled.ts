import { useEffect, useState } from "react";
import { Site } from "../api";

// usePushEnabled is the church's switch for notifications on devices
// (DEC-124) from /site: true until it answers (most churches leave it on,
// so a form doesn't jump), false only when the church turned it off. A
// failed read leaves it on; the server still decides what pushes.
export function usePushEnabled(): boolean {
  const [on, setOn] = useState(true);
  useEffect(() => {
    let alive = true;
    Site.get().then(
      (s) => { if (alive) setOn(s.pushEnabled !== false); },
      () => {},
    );
    return () => { alive = false; };
  }, []);
  return on;
}
