import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/svelte";

vi.mock("../api", () => ({
  cacheInfo: vi.fn(),
  onEvent: vi.fn(() => () => {}),
}));

import Footer from "./Footer.svelte";
import { cache } from "../stores/cache.svelte";
import { app } from "../stores/app.svelte";

beforeEach(() => {
  cache.reset();
  app.route = "main";
});

describe("Footer cache size", () => {
  it("shows the size with a detail tooltip, not as a live region", () => {
    cache.info = {
      bytes: Math.round(1.2 * 1024 ** 3),
      messages: 12345,
      bodies: 12000,
    };
    render(Footer);
    const el = screen.getByText("Cache 1.2 GB");
    expect(el.getAttribute("title")).toMatch(
      /12,?345 messages · 12,?000 bodies cached/,
    );
    expect(el.closest("[aria-live]")).toBeNull();
  });

  it("is hidden when the cache info is unavailable", () => {
    cache.info = null;
    render(Footer);
    expect(screen.queryByText(/^Cache /)).toBeNull();
  });
});
