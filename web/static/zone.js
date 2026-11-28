// Tells the server this browser's time zone the first time a person signs
// in, so their times are right without asking (DEC-58). The server keeps it
// only while they have none; they can change it on their profile.
(() => {
  const script = document.currentScript;
  let zone = "";
  try {
    zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {
    return;
  }
  if (!zone || !script) return;
  const body = new URLSearchParams({ csrf: script.dataset.csrf, zone, first: "1" });
  fetch("/profile/zone", { method: "POST", body, credentials: "same-origin" }).catch(() => {});
})();
