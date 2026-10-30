import React from "react";
import ReactDOM from "react-dom/client";
import { RouterProvider, createBrowserRouter } from "react-router-dom";
import App from "./App";
import { AuthProvider } from "./auth";
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

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>
);
