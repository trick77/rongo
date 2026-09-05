import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import SharePage from "./share/SharePage";
import { routeFromLocation } from "./routing";
import "./index.css";

/**
 * The share page is mounted INSTEAD of the app, never inside it.
 *
 * App's session gate sends any 401 straight to /api/auth/login, and the one
 * audience a share link exists for has no session at all: rendered inside the
 * app, an anonymous reader would be bounced to the identity provider instead
 * of reading the thread. Everything else routes inside App, where the rail and
 * the header belong.
 */
const route = routeFromLocation();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    {route.view === "share" ? <SharePage token={route.token} /> : <App />}
  </StrictMode>,
);
