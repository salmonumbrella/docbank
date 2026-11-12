import { afterEach, expect, it, vi } from "vite-plus/test";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import InspectorPeople from "./InspectorPeople.svelte";
import { assertion, edge, people } from "./document-facts-test-fixtures.js";

afterEach(cleanup);

// Safe disclosure is served by the API (blind copies never reach the browser),
// and pendingNames owns candidate dedupe; this file tests the rendering.
const value = people({
  edges: [edge("author", "p1", "Mara Okonkwo-Fell"), edge("recipient", "p2", "Ines Albarrán")],
  assertions: [assertion("author", "p4", "Teodor Vance")],
  unresolved: [
    { candidate_id: "k1", raw_label: "L. Beerwijk", role: "author", evidence_kind: "pdf_info" },
  ],
});

it("groups people by role, keeps asserted authors and shows metadata names as text", async () => {
  const onperson = vi.fn();
  render(InspectorPeople, { read: { status: "ready", value }, onperson, onretry: vi.fn() });
  expect(screen.getByRole("button", { name: "Mara Okonkwo-Fell" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Teodor Vance" })).toBeTruthy();
  expect(screen.getByText("To")).toBeTruthy();
  // Acceptance 3: plain text, not a control.
  expect(screen.getByText("L. Beerwijk").closest("button")).toBeNull();
  expect(screen.getByText("needs review")).toBeTruthy();
  await fireEvent.click(screen.getByRole("button", { name: "Mara Okonkwo-Fell" }));
  expect(onperson).toHaveBeenCalledWith("p1");
});

it("keeps operator assertions, still selectable, beside pending and failed derivation notices", async () => {
  const onperson = vi.fn();
  const view = render(InspectorPeople, {
    read: { status: "ready", value: { ...value, state: "pending" } },
    onperson,
    onretry: vi.fn(),
  });
  expect(screen.getByText("People are still being worked out.")).toBeTruthy();
  expect(screen.queryByText("Mara Okonkwo-Fell")).toBeNull();
  expect(screen.queryByText("needs review")).toBeNull();
  await fireEvent.click(screen.getByRole("button", { name: "Teodor Vance" }));
  expect(onperson).toHaveBeenCalledWith("p4");
  await view.rerender({
    read: { status: "ready", value: { ...value, state: "failed" } },
    onperson,
    onretry: vi.fn(),
  });
  expect(screen.getByText("People couldn't be worked out for this version.")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Teodor Vance" })).toBeTruthy();
  // A derivation failure is the server's settled answer, so there is nothing to retry.
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
});

it("shows an empty settled read and retries a failed HTTP read", async () => {
  const onretry = vi.fn();
  const view = render(InspectorPeople, {
    read: { status: "ready", value: people() },
    onperson: vi.fn(),
    onretry,
  });
  expect(screen.getByText("No people found.")).toBeTruthy();
  expect(screen.getByText("Authors")).toBeTruthy();
  await view.rerender({ read: { status: "error" }, onperson: vi.fn(), onretry });
  await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(onretry).toHaveBeenCalledOnce();
});
