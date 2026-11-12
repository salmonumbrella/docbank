import { afterEach, expect, it, vi } from "vite-plus/test";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import InspectorPeople from "./InspectorPeople.svelte";
import { edge, people } from "./document-facts-test-fixtures.js";

afterEach(cleanup);

const value = people({
  edges: [
    edge("author", "p1", "Mara Okonkwo-Fell"),
    edge("recipient", "p2", "Ines Albarrán"),
    edge("blind_copy", "p3", "Private Reviewer"),
  ],
  assertions: [
    {
      action: "assert",
      assertion_id: "a1",
      display_name: "Teodor Vance",
      person_id: "p4",
      role: "author",
      note: "",
      recorded_at: "2026-03-12T00:00:00Z",
      revision: 1,
    },
  ],
  unresolved: [
    { candidate_id: "k1", raw_label: "L. Beerwijk", role: "author", evidence_kind: "pdf_info" },
    { candidate_id: "k1", raw_label: "L. Beerwijk", role: "author", evidence_kind: "xmp" },
  ],
});

it("groups people by role, keeps asserted authors and pending names, never Bcc", async () => {
  const onperson = vi.fn();
  render(InspectorPeople, { read: { status: "ready", value }, onperson, onretry: vi.fn() });
  expect(screen.getByRole("button", { name: "Mara Okonkwo-Fell" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Teodor Vance" })).toBeTruthy();
  expect(screen.getByText("To")).toBeTruthy();
  // Acceptance 3: plain text, not a control.
  const pending = screen.getByText("L. Beerwijk");
  expect(pending.closest("button")).toBeNull();
  expect(screen.getAllByText("needs review")).toHaveLength(1);
  // Acceptance 4.
  expect(screen.queryByText("Private Reviewer")).toBeNull();
  await fireEvent.click(screen.getByRole("button", { name: "Mara Okonkwo-Fell" }));
  expect(onperson).toHaveBeenCalledWith("p1");
});

it("shows pending and failed reads with a retry for that resource", async () => {
  const onretry = vi.fn();
  const view = render(InspectorPeople, {
    read: { status: "ready", value: people({ state: "pending" }) },
    onperson: vi.fn(),
    onretry,
  });
  expect(screen.getByText("People are still being worked out.")).toBeTruthy();
  await view.rerender({ read: { status: "ready", value: people() }, onperson: vi.fn(), onretry });
  expect(screen.getByText("No people found.")).toBeTruthy();
  expect(screen.getByText("Authors")).toBeTruthy();
  await view.rerender({ read: { status: "error" }, onperson: vi.fn(), onretry });
  await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(onretry).toHaveBeenCalledOnce();
});
