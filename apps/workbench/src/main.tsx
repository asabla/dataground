import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "@dataground/ui/styles.css";
import "./app.css";

const rootElement = document.getElementById("root");

if (!rootElement) {
  throw new Error("DataGround workbench root element is missing");
}

createRoot(rootElement).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
