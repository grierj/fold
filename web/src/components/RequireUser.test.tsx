import { isObject, isString } from "../lib/wireValues";
import { renderWithAuth } from "../test/auth";
import { screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { expect, it } from "vitest";

type TestSession = { user: { id: number } | null };

const actor: TestSession = { user: null };

const render = renderWithAuth(() => ({ user: actor.user }));

import { RequireUser } from "./RequireUser";

function Login() {
  const state: unknown = useLocation().state;
  const from = isObject(state) && "from" in state && isString(state.from) ? state.from : undefined;

  return <p>Log in, then back to {from}</p>;
}

const mount = () => render(<MemoryRouter initialEntries={["/groups/3/events/new?x=1"]}><Routes>
  <Route path="/groups/:id/events/new" element={<RequireUser><p>The form</p></RequireUser>} />
  <Route path="/login" element={<Login />} />
</Routes></MemoryRouter>);

it("sends a signed-out visitor to Log in with the page they asked for, query included", () => {
  actor.user = null;
  mount();
  expect(screen.getByText("Log in, then back to /groups/3/events/new?x=1")).toBeInTheDocument();
  expect(screen.queryByText("The form")).not.toBeInTheDocument();
});

it("shows the page to a signed-in person", () => {
  actor.user = { id: 1 };
  mount();
  expect(screen.getByText("The form")).toBeInTheDocument();
});
