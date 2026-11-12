import { expect, it } from "vite-plus/test";
import type { WhenV1 } from "./generated/docbank.js";
import { dateKindLabel, formatWhen, precisionLabel } from "./documentDates.js";
const when = (fields: Partial<WhenV1>): WhenV1 => ({
  state: "exact",
  precision: "minute",
  placed_by: { claim_basis: "", event_id: "", fallback: false, rule: "", source: "" },
  ...fields,
});
it("formats instants only in the configured zone", () => {
  const w = when({ at: "2026-03-11T03:40:00Z", source_zone: "-04:00" });
  expect(formatWhen(w, { name: "America/Toronto", supported: true }).value).toBe(
    "Mar 10, 2026, 11:40 PM EDT",
  );
  expect(formatWhen(w, { name: "UTC", supported: true }).value).toBe("Mar 11, 2026, 3:40 AM UTC");
  expect(formatWhen(w, undefined).value).toContain("Timeline zone unavailable");
  expect(formatWhen(w, { name: "Mars/Olympus", supported: false }).value).toContain("Mars/Olympus");
});
it("keeps floating and civil dates independent of the configured zone", () => {
  const w = when({ state: "floating", local: "2026-03-10T23:30:00" });
  expect(formatWhen(w, undefined).value).toBe("Mar 10, 2026, 11:30 PM (no zone)");
  expect(formatWhen(w, { name: "Asia/Tokyo", supported: true })).toEqual(formatWhen(w, undefined));
  expect(
    formatWhen(when({ state: "date", precision: "date", date: "2026-03-10" }), undefined).value,
  ).toBe("Mar 10, 2026");
  expect(
    formatWhen(when({ state: "coarse", precision: "month", period: "2026-03" }), undefined).value,
  ).toBe("March 2026");
  expect(
    formatWhen(when({ state: "coarse", precision: "year", period: "2026" }), undefined).value,
  ).toBe("2026");
});
it("labels known precision and date kinds", () => {
  expect(precisionLabel("fraction")).toBe("to the second");
  expect(dateKindLabel("vault_recorded")).toBe("Recorded in vault");
  expect(dateKindLabel("captured")).toBe("Captured");
  expect(
    formatWhen(when({ state: "date", precision: "date", date: "2026-03-10" }), undefined).zoneNote,
  ).toBe("");
  expect(dateKindLabel("other")).toBe("other");
});

it("preserves source-zone uncertainty and formats inclusive civil ranges", () => {
  const zone = { name: "America/Toronto", supported: true };
  const exact = when({ at: "2026-03-11T03:40:00Z", zone_state: "unknown" });
  expect(formatWhen(exact, zone).zoneNote).toBe("source zone not recorded");
  expect(formatWhen(exact, zone).value).toBe("Mar 10, 2026, 11:40 PM EDT");
  expect(
    formatWhen(
      { ...when({ state: "date", precision: "date", date: "2026-03-09" }), end_date: "2026-03-11" },
      zone,
    ).value,
  ).toMatch(/^Mar 9\s–\s11, 2026$/u);
});
it("never converts floating values using the process timezone", () => {
  const saved = process.env.TZ;
  try {
    process.env.TZ = "Asia/Tokyo";
    const local = when({ state: "floating", local: "2026-03-10T23:30:00", zone_state: "named" });
    const first = formatWhen(local, undefined);
    process.env.TZ = "Pacific/Honolulu";
    expect(formatWhen(local, undefined)).toEqual(first);
    for (const state of ["omitted", "invalid"])
      expect(formatWhen({ ...local, zone_state: state }, undefined).value).toBe(first.value);
  } finally {
    if (saved === undefined) delete process.env.TZ;
    else process.env.TZ = saved;
  }
});
