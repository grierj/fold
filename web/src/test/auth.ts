import { render, type RenderOptions } from "@testing-library/react";
import { createElement, useContext, type PropsWithChildren, type ReactNode } from "react";
import { AuthContext, type useAuth } from "../auth";
import type { User } from "../types";
import { testUser } from "./fixtures";

type AuthFixture = Partial<Omit<ReturnType<typeof useAuth>, "user">> & {
  user?: Partial<User> | null;
};

export function renderWithAuth(getSession: () => AuthFixture) {
  return (ui: ReactNode, options?: RenderOptions) => {
    const Outer = options?.wrapper;

    function Session({ children }: PropsWithChildren) {
      const defaults = useContext(AuthContext);
      const { user, ...session } = getSession();
      const value = { ...defaults, loading: false, ...session, user: user ? testUser(user) : null };
      const provider = createElement(AuthContext.Provider, { value }, children);

      return Outer ? createElement(Outer, null, provider) : provider;
    }

    return render(ui, { ...options, wrapper: Session });
  };
}
