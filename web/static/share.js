// The date page's Share (DEC-93): the device's share sheet where there is
// one, else copying the link. Without this script the link is in a field
// to select and copy.
(() => {
  const actions = document.getElementById("share-actions");
  const field = document.getElementById("share-link");
  const button = document.getElementById("share-copy");
  const status = document.getElementById("share-status");
  if (!actions || !field || !button || !status) return;
  const sheet = typeof navigator.share === "function";
  if (!sheet && !(navigator.clipboard && navigator.clipboard.writeText)) return;
  button.textContent = sheet ? "Share" : "Copy link";
  actions.hidden = false;
  field.addEventListener("focus", () => field.select());
  button.addEventListener("click", async () => {
    status.textContent = "";
    if (sheet) {
      try {
        await navigator.share({ title: document.title, url: field.value });
      } catch {
        // Closing the share sheet is not a problem to report.
      }
      return;
    }
    try {
      await navigator.clipboard.writeText(field.value);
      status.textContent = "Link copied.";
    } catch {
      field.focus();
      field.select();
    }
  });
})();
