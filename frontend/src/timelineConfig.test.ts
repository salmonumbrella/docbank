import { afterEach, expect, it } from "vite-plus/test";
import { timelineZone } from "./timelineConfig.js";
afterEach(() => document.head.querySelector('meta[name="docbank-timeline-timezone"]')?.remove());
it("reads the configured zone and reports unsupported names", () => {
  expect(timelineZone()).toBeUndefined();
  const meta = document.createElement("meta");
  meta.name = "docbank-timeline-timezone";
  document.head.append(meta);
  meta.content = "America/Toronto";
  expect(timelineZone()).toEqual({ name: "America/Toronto", supported: true });
  meta.content = "UTC";
  expect(timelineZone()?.supported).toBe(true);
  meta.content = "Mars/Olympus";
  expect(timelineZone()).toEqual({ name: "Mars/Olympus", supported: false });
});
