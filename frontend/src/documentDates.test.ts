import { expect, it } from "vite-plus/test";
import { dateKindLabel, formatWhen, precisionLabel } from "./documentDates.js";
import { when } from "./document-facts-test-fixtures.js";
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
  for (const state of ["named", "omitted", "invalid"])
    expect(formatWhen({ ...w, zone_state: state }, undefined).value).toBe(
      "Mar 10, 2026, 11:30 PM (no zone)",
    );
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
  expect(dateKindLabel("other")).toBe("other");
});

it("preserves source-zone uncertainty and formats inclusive civil ranges", () => {
  expect(
    formatWhen(when({ state: "date", precision: "date", date: "2026-03-10" }), undefined).zoneNote,
  ).toBe("");
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
it("shows only the clock fields supported by exact and floating precision", () => {
  const zone = { name: "UTC", supported: true };
  const expected = [
    ["hour", "2026-03-11T03:00:00Z", "2026-03-11T03:00:00", "3 AM"],
    ["minute", "2026-03-11T03:40:00Z", "2026-03-11T03:40:00", "3:40 AM"],
    ["second", "2026-03-11T03:40:12Z", "2026-03-11T03:40:12", "3:40:12 AM"],
    ["fraction", "2026-03-11T03:40:12.123456Z", "2026-03-11T03:40:12.123456", "3:40:12.123456 AM"],
  ] as const;
  for (const [precision, at, local, clock] of expected) {
    expect(formatWhen(when({ precision, at }), zone).value).toContain(clock);
    expect(formatWhen(when({ state: "floating", precision, local }), zone).value).toContain(clock);
  }
});

it("preserves hour-precision instants when zone offsets introduce minutes", () => {
  const utc = { name: "UTC", supported: true };
  const kolkata = { name: "Asia/Kolkata", supported: true };
  const fromUTC = when({
    precision: "hour",
    at: "2026-04-03T09:00:00Z",
    local: "2026-04-03T09:00:00",
    zone_state: "utc",
    source_zone: "UTC",
    source_offset_seconds: 0,
  });
  const fromIndia = when({
    precision: "hour",
    at: "2026-04-03T03:30:00Z",
    local: "2026-04-03T09:00:00",
    zone_state: "offset",
    source_zone: "+05:30",
    source_offset_seconds: 19800,
  });
  expect(formatWhen(fromUTC, kolkata).value).toContain("2:30 PM");
  expect(formatWhen(fromIndia, utc).value).toContain("3:30 AM");
  expect(precisionLabel(fromIndia.precision)).toBe("to the hour");
});

it("retains known fractional digits without inventing unknown source precision", () => {
  const zone = { name: "America/Toronto", supported: true };
  const known = {
    precision: "fraction",
    zone_state: "offset",
    source_zone: "+05:30",
    source_offset_seconds: 19800,
  };
  expect(
    formatWhen(
      when({ ...known, at: "2026-03-11T03:40:12.12Z", local: "2026-03-11T09:10:12.1200" }),
      zone,
    ).value,
  ).toContain("11:40:12.1200 PM");
  expect(
    formatWhen(
      when({ ...known, at: "2026-03-11T03:40:12Z", local: "2026-03-11T09:10:12.0000" }),
      zone,
    ).value,
  ).toContain("11:40:12.0000 PM");
  const unknown = { precision: "fraction", zone_state: "unknown" };
  expect(formatWhen(when({ ...unknown, at: "2026-03-11T03:40:12.12Z" }), zone).value).toContain(
    "11:40:12.12 PM",
  );
  expect(formatWhen(when({ ...unknown, at: "2026-03-11T03:40:12Z" }), zone).value).toBe(
    "Mar 10, 2026, 11:40:12 PM EDT",
  );
});
