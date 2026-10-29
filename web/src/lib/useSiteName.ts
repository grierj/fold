import { useEffect, useState } from "react";
import { Site } from "../api";

export function useSiteName() {
  const [name, setName] = useState<string | undefined>(undefined);
  useEffect(() => {
    let alive = true;
    Site.get().then(
      (site) => { if (alive) setName(site.name); },
      () => { if (alive) setName(""); },
    );
    return () => { alive = false; };
  }, []);
  return name;
}
