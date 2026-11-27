import React from "react";
import ReactDOM from "react-dom/client";

import { App } from "./App";
import "./i18n";
import "./index.css";
import { AuthGate } from "./shell";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <AuthGate>
      <App />
    </AuthGate>
  </React.StrictMode>,
);
