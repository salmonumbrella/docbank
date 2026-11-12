import { afterEach, expect, it } from "vite-plus/test";
import { cleanup, render, screen } from "@testing-library/svelte";
import DocumentFactCells from "./DocumentFactCells.svelte";
import type { DocumentFacts, FactRead } from "./documentFacts.svelte.js";
import type { VersionDates, VersionPeople } from "./generated/docbank.js";
import {
  claim,
  dates,
  edge,
  people,
  threeClaims,
  toronto,
} from "./document-facts-test-fixtures.js";

afterEach(cleanup);

type Read = { dates: FactRead<VersionDates>; people: FactRead<VersionPeople> };
const owner = (reads: Record<string, Read>) => {
  const asked: string[] = [];
  const facts = {
    get(id: string) {
      asked.push(id);
      return reads[id] ?? { dates: { status: "idle" }, people: { status: "idle" } };
    },
  } as unknown as DocumentFacts;
  return { facts, asked };
};
const row = () => document.body.appendChild(document.createElement("table")).insertRow();

it("shows the primary date and an avatar stack with a review count", () => {
  const five = ["A", "B", "C", "D", "E"].map((id) => edge("author", id, `Person ${id}`));
  const { facts } = owner({
    v: {
      dates: { status: "ready", value: threeClaims },
      people: {
        status: "ready",
        value: people({
          edges: five,
          unresolved: [
            {
              candidate_id: "k1",
              raw_label: "L. Beerwijk",
              role: "author",
              evidence_kind: "pdf_info",
            },
          ],
        }),
      },
    },
  });
  render(DocumentFactCells, { target: row(), props: { versionID: "v", facts, zone: toronto } });
  expect(screen.getByText("Mar 10, 2026, 11:40 PM EDT")).toBeTruthy();
  expect(screen.getByText("Authored · to the minute")).toBeTruthy();
  expect(screen.getAllByRole("img").map((img) => img.getAttribute("aria-label"))).toEqual([
    "Person A",
    "Person B",
    "Person C",
  ]);
  expect(screen.getByText("+2")).toBeTruthy();
  expect(screen.getByText("1 to review")).toBeTruthy();
});

it("marks floating values and empty, pending and failed reads", () => {
  const { facts } = owner({
    floating: {
      dates: {
        status: "ready",
        value: dates([
          claim("f", "captured", { state: "floating", local: "2026-03-10T23:30:00" }, true),
        ]),
      },
      people: { status: "ready", value: people() },
    },
    pending: {
      dates: { status: "ready", value: dates([], "pending") },
      people: { status: "error" },
    },
  });
  render(DocumentFactCells, {
    target: row(),
    props: { versionID: "floating", facts, zone: toronto },
  });
  expect(screen.getByText(/· floating$/u)).toBeTruthy();
  expect(screen.getByText("—")).toBeTruthy();
  cleanup();
  render(DocumentFactCells, {
    target: row(),
    props: { versionID: "pending", facts, zone: toronto },
  });
  expect(screen.getByText("Pending")).toBeTruthy();
  expect(screen.getByText("Unavailable")).toBeTruthy();
});

it("uses the short zone-unavailable form for exact values", () => {
  const { facts } = owner({
    v: {
      dates: { status: "ready", value: threeClaims },
      people: { status: "ready", value: people() },
    },
  });
  render(DocumentFactCells, {
    target: row(),
    props: { versionID: "v", facts, zone: undefined },
  });
  expect(screen.getByText("Zone unavailable")).toBeTruthy();
});

it("reads nothing without a version, as for folders and hidden columns", () => {
  const { facts, asked } = owner({});
  const target = row();
  render(DocumentFactCells, { target, props: { versionID: undefined, facts, zone: toronto } });
  expect(asked).toEqual([]);
  expect(target.querySelectorAll("td.col-facts")).toHaveLength(2);
});
