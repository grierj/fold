import { useEffect } from "react";

// The visible h1 is the page's title, including async event names and errors.
// Observe the content rather than maintaining a second, easily stale route map.
export function usePageTitle(site: string | undefined, routeKey = "") {
  useEffect(() => {
    const main = document.getElementById("main");
    if (!main) return;
    const update = () => {
      const page = main.querySelector("h1")?.textContent?.trim() || "Loading";
      const title = `${page} · ${site || "Fold"}`;
      if (document.title !== title) document.title = title;
    };
    update();
    const observer = new MutationObserver(update);
    observer.observe(main, { childList: true, characterData: true, subtree: true });
    return () => observer.disconnect();
  }, [site, routeKey]);
}
