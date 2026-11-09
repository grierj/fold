import React from "react";
import ReactDOM from "react-dom/client";
import { RouterProvider, createBrowserRouter } from "react-router-dom";
import App from "./App";
import { configureApi } from "./api/core";
import { AuthProvider } from "./auth";
import { syncAppBadge } from "./lib/push";
import "./index.css";

// A page restored from the back/forward cache may show a session that has
// since ended. The shell is served no-store, which keeps browsers from
// caching it; reload anyway if one does restore it.
window.addEventListener("pageshow", (e) => {
  if (e.persisted) window.location.reload();
});

// A data router with one splat route around the app's own <Routes>: it only
// adds useBlocker, which the "Leave without saving?" guard needs (DEC-98).
const router = createBrowserRouter([
  { path: "*", element: <AuthProvider><App /></AuthProvider> },
]);

// The API returns 401 JSON (never redirects); the SPA owns routing, so any
// unauthorized API call sends the user to /login, preserving their place.
// Configured before the first render, so no request can run without it.
configureApi({
  onUnauthorized: () => {
    // Signed out without Log out (the session expired): the icon's count
    // was this person's. The device stays subscribed; it belongs to the
    // person, not the session (logout and turning off end it).
    syncAppBadge(0);
    const { pathname, search } = router.state.location;
    void router.navigate("/login", { state: { from: pathname + search }, replace: true });
  },
});

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>
);
