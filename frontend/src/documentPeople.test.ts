import { expect, it } from "vite-plus/test";
import { pendingNames, roleRows, tablePeople } from "./documentPeople.js";
import { assertion, edge, people } from "./document-facts-test-fixtures.js";

it("groups safe roles, assertions, and distinct pending names", () => {
  const linked = {
    ...edge("author", "A"),
    external: [{ uid: "uid", system: "test", archive_id: "test" }],
  };
  const data = people({
    edges: [linked, edge("author", "A"), edge("recipient", "B"), edge("custodian", "D")],
    assertions: [assertion("author", "E")],
    unresolved: [
      { candidate_id: "k", raw_label: "Name", evidence_kind: "", role: "author" },
      { candidate_id: "k", raw_label: "Name", evidence_kind: "", role: "author" },
    ],
  });
  expect(roleRows(data).map((row) => [row.role, row.people.map((p) => p.id)])).toEqual([
    ["author", ["A", "E"]],
    ["custodian", ["D"]],
    ["recipient", ["B"]],
  ]);
  expect(roleRows(data)[0]!.people[0]!.colorKey).toBe("A");
  expect(pendingNames(data)).toEqual([{ candidateID: "k", name: "Name" }]);
  expect(tablePeople(data).people.map((p) => p.id)).toEqual(["A", "E", "D"]);
  expect(tablePeople(data).review).toBe(1);
  for (const state of ["pending", "failed"]) {
    const unsettled = { ...data, state };
    expect(tablePeople(unsettled).people.map((p) => p.id)).toEqual(["E"]);
    expect(pendingNames(unsettled)).toEqual([]);
  }
});
