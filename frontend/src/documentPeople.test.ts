import { expect, it } from "vite-plus/test";
import type { VersionPeople, VersionPersonEdge } from "./generated/docbank.js";
import { pendingNames, roleRows, tablePeople } from "./documentPeople.js";
const edge = (role: string, person_id: string, uid = ""): VersionPersonEdge => ({
  role,
  person_id,
  display_name: person_id,
  external: uid ? [{ uid, system: "test", archive_id: "test" }] : [],
  basis: "",
  claim_count: 1,
  confidence: "",
  evidence_id: "",
  evidence_kind: "",
  linked_contact: false,
  raw_label: "",
});
it("groups safe roles, assertions, and distinct pending names", () => {
  const data: VersionPeople = {
    content_version_id: "v",
    state: "published",
    edges: [
      edge("author", "A", "uid"),
      edge("author", "A"),
      edge("recipient", "B"),
      edge("blind_copy", "C"),
      edge("custodian", "D"),
    ],
    assertions: [
      {
        action: "assert",
        assertion_id: "i",
        display_name: "E",
        person_id: "E",
        role: "author",
        note: "",
        recorded_at: "",
        revision: 1,
      },
    ],
    unresolved: [
      { candidate_id: "k", raw_label: "Name", evidence_kind: "", role: "author" },
      { candidate_id: "k", raw_label: "Name", evidence_kind: "", role: "author" },
    ],
  };
  expect(roleRows(data).map((row) => [row.role, row.people.map((p) => p.id)])).toEqual([
    ["author", ["A", "E"]],
    ["custodian", ["D"]],
    ["recipient", ["B"]],
  ]);
  expect(roleRows(data)[0]!.people[0]!.colorKey).toBe("uid");
  expect(pendingNames(data)).toEqual([{ candidateID: "k", name: "Name" }]);
  expect(tablePeople(data).people.map((p) => p.id)).toEqual(["A", "E", "D"]);
  expect(tablePeople(data).review).toBe(1);
});
