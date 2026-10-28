import { render, waitFor } from "@testing-library/react";
import { expect, it } from "vitest";
import { usePageTitle } from "./usePageTitle";

function Page({ title, site = "Grace Church", route = "/" }: { title?: string; site?: string; route?: string }) {
  usePageTitle(site, route);
  return <main id="main">{title && <h1>{title}</h1>}</main>;
}

it("titles navigation, asynchronously loaded names and error pages with the church", async () => {
  const view = render(<Page title="Calendar" />);
  await waitFor(() => expect(document.title).toBe("Calendar · Grace Church"));
  view.rerender(<Page route="/events/1" />);
  await waitFor(() => expect(document.title).toBe("Loading · Grace Church"));
  view.rerender(<Page route="/events/1" title="Family potluck" />);
  await waitFor(() => expect(document.title).toBe("Family potluck · Grace Church"));
  view.rerender(<Page route="/missing" title="Page not found" site="Another Church" />);
  await waitFor(() => expect(document.title).toBe("Page not found · Another Church"));
});
