import { afterEach } from "vitest";
import { cleanup } from "@testing-library/svelte";
import "@testing-library/jest-dom/vitest";

afterEach(() => cleanup());

// jsdom has no layout engine or ResizeObserver; give components a stub.
class RO {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
(globalThis as unknown as { ResizeObserver: typeof RO }).ResizeObserver = RO;
Element.prototype.scrollBy ??= function () {};
Element.prototype.scrollIntoView ??= function () {};
