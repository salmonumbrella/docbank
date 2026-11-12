import { afterEach, expect, it, vi } from "vite-plus/test";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import InspectorDates from "./InspectorDates.svelte";
import { claim, dates, threeClaims, toronto } from "./document-facts-test-fixtures.js";

const originalTZ = process.env.TZ;
afterEach(() => {
  cleanup();
  // Assigning undefined would store the string "undefined", so unset instead.
  if (originalTZ === undefined) delete process.env.TZ;
  else process.env.TZ = originalTZ;
});

it("lists every claim with the primary first and marked Primary (Acceptance 1)", () => {
  render(InspectorDates, {
    read: { status: "ready", value: threeClaims },
    zone: toronto,
    onretry: vi.fn(),
  });
  const items = screen.getAllByRole("listitem");
  expect(items).toHaveLength(3);
  expect(within(items[0]!).getByText("Primary")).toBeTruthy();
  expect(within(items[0]!).getByText("Authored")).toBeTruthy();
  expect(within(items[0]!).getByText("Mar 10, 2026, 11:40 PM EDT")).toBeTruthy();
  expect(
    within(items[0]!).getByText(/pdf info · to the minute · source asserted · -04:00/u),
  ).toBeTruthy();
  expect(within(items[1]!).queryByText("Primary")).toBeNull();
  expect(within(items[2]!).getByText("Mar 9, 2026").textContent).not.toContain(":");
  expect(screen.getByText("3 claims")).toBeTruthy();
  expect(screen.getByText("Times show in America/Toronto.")).toBeTruthy();
});

it("shows a floating capture identically in every browser zone (Acceptance 2)", () => {
  const read = {
    status: "ready" as const,
    value: dates([
      claim(
        "f",
        "captured",
        { state: "floating", local: "2026-03-10T23:30:00", zone_state: "omitted" },
        true,
      ),
    ]),
  };
  const text: string[] = [];
  for (const tz of ["Asia/Tokyo", "Pacific/Honolulu"]) {
    process.env.TZ = tz;
    const view = render(InspectorDates, { read, zone: toronto, onretry: vi.fn() });
    text.push(view.container.textContent ?? "");
    view.unmount();
  }
  expect(text[0]).toContain("Mar 10, 2026, 11:30 PM (no zone)");
  expect(text[0]).toContain("floating (no zone)");
  expect(text[1]).toBe(text[0]);
});

it("says so when the configured zone is unavailable instead of falling back", () => {
  render(InspectorDates, {
    read: { status: "ready", value: threeClaims },
    zone: { name: "Mars/Olympus", supported: false },
    onretry: vi.fn(),
  });
  expect(screen.getAllByText("Timeline zone unavailable (Mars/Olympus)")).toHaveLength(2);
  expect(
    screen.getByText(/Timeline zone unavailable \(Mars\/Olympus\); exact times/u),
  ).toBeTruthy();
  expect(screen.queryByText(/UTC/u)).toBeNull();
  expect(screen.getByText("Mar 9, 2026")).toBeTruthy();
});

it("tells loading, pending, failed, empty and errors apart", async () => {
  const onretry = vi.fn();
  const view = render(InspectorDates, { read: { status: "loading" }, zone: toronto, onretry });
  expect(screen.getByLabelText("Loading dates")).toBeTruthy();
  await view.rerender({
    read: { status: "ready", value: dates([], "pending") },
    zone: toronto,
    onretry,
  });
  expect(screen.getByText("Dates are still being worked out.")).toBeTruthy();
  await view.rerender({
    read: { status: "ready", value: dates([], "none") },
    zone: toronto,
    onretry,
  });
  expect(screen.getByText("No dates found.")).toBeTruthy();
  await view.rerender({ read: { status: "error" }, zone: toronto, onretry });
  expect(screen.getByText(/Dates couldn't be loaded/u)).toBeTruthy();
  await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(onretry).toHaveBeenCalledOnce();
});
