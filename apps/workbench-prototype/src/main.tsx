import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "@dataground/tokens/styles.css";
import "@dataground/ui/styles.css";
import "./style.css";

const root = document.getElementById("root");
if (!root) throw new Error("Prototype root is missing");
createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
