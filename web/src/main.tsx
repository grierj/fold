import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import App from "./App";
import { AuthProvider } from "./auth";
import "./index.css";

// A page restored from the back/forward cache may show a session that has
// since ended. The shell is served no-store, which keeps browsers from
// caching it; reload anyway if one does restore it.
window.addEventListener("pageshow", (e) => {
  if (e.persisted) window.location.reload();
});

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <BrowserRouter>
      <AuthProvider>
        <App />
      </AuthProvider>
    </BrowserRouter>
  </React.StrictMode>
);
