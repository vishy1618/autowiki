import "@testing-library/jest-dom";
import { vi } from "vitest";

// jsdom doesn't implement scrollIntoView — stub it globally.
window.HTMLElement.prototype.scrollIntoView = vi.fn();
