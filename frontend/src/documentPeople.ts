import type { VersionPeople } from "./generated/docbank.js";

export type PersonRef = { id: string; name: string; colorKey: string };
const roles = [
  ["author", "Authors"],
  ["custodian", "Custodian"],
  ["sender", "From"],
  ["recipient", "To"],
  ["copied", "Cc"],
  ["organizer", "Organizer"],
  ["attendee", "Attendee"],
  ["participant", "Participant"],
  ["last_saved_by", "Last saved by"],
  ["speaker", "Speaker"],
] as const;

export function roleRows(data: VersionPeople) {
  return roles
    .map(([role, label]) => {
      const people = new Map<string, PersonRef>();
      for (const edge of data.state === "pending" || data.state === "failed" ? [] : data.edges) {
        if (edge.role === role && !people.has(edge.person_id))
          people.set(edge.person_id, {
            id: edge.person_id,
            name: edge.display_name,
            colorKey: edge.person_id,
          });
      }
      for (const assertion of data.assertions) {
        if (
          assertion.role === role &&
          assertion.action === "assert" &&
          !people.has(assertion.person_id)
        )
          people.set(assertion.person_id, {
            id: assertion.person_id,
            name: assertion.display_name,
            colorKey: assertion.person_id,
          });
      }
      return { role, label, people: [...people.values()] };
    })
    .filter((row) => row.role === "author" || row.people.length > 0);
}

export function pendingNames(data: VersionPeople) {
  const names = new Map<string, string>();
  for (const candidate of data.state === "pending" || data.state === "failed"
    ? []
    : data.unresolved)
    if (!names.has(candidate.candidate_id)) names.set(candidate.candidate_id, candidate.raw_label);
  return [...names].map(([candidateID, name]) => ({ candidateID, name }));
}

export function tablePeople(data: VersionPeople) {
  const people = new Map<string, PersonRef>();
  for (const row of roleRows(data))
    if (row.role === "author" || row.role === "custodian") {
      for (const person of row.people) if (!people.has(person.id)) people.set(person.id, person);
    }
  return { people: [...people.values()], review: pendingNames(data).length };
}
