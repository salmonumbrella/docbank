// Synthetic dates and people for component tests (invented names only).
import type {
  VersionDateClaim,
  VersionDates,
  VersionPeople,
  VersionPersonEdge,
  VersionPersonAssertion,
  WhenV1,
} from "./generated/docbank.js";

export const toronto = { name: "America/Toronto", supported: true };

export function when(fields: Partial<WhenV1>): WhenV1 {
  return {
    state: "exact",
    precision: "minute",
    placed_by: { claim_basis: "", event_id: "", fallback: false, rule: "", source: "" },
    ...fields,
  };
}

export function claim(
  id: string,
  date_kind: string,
  fields: Partial<WhenV1>,
  primary = false,
): VersionDateClaim {
  return {
    axis_key: "",
    claim_basis: "source_asserted",
    date_kind,
    date_value: "",
    event_id: id,
    evidence_id: `ev-${id}`,
    evidence_kind: "pdf_info",
    parse_confidence: "high",
    precision: fields.precision ?? "minute",
    primary,
    raw_value: "",
    timezone_kind: "",
    when: when(fields),
    zone_text: "",
  };
}

export function dates(claims: VersionDateClaim[], state = "published"): VersionDates {
  return { claims, content_version_id: "v", diagnostics: [], node_id: 1, state };
}

export function edge(role: string, person_id: string, display_name = person_id): VersionPersonEdge {
  return {
    role,
    person_id,
    display_name,
    external: [],
    basis: "",
    claim_count: 1,
    confidence: "",
    evidence_id: "",
    evidence_kind: "",
    linked_contact: false,
    raw_label: display_name,
  };
}

export function assertion(
  role: string,
  person_id: string,
  display_name = person_id,
): VersionPersonAssertion {
  return {
    action: "assert",
    assertion_id: `assert-${person_id}`,
    display_name,
    person_id,
    role,
    note: "",
    recorded_at: "",
    revision: 1,
  };
}

export function people(fields: Partial<VersionPeople> = {}): VersionPeople {
  return {
    content_version_id: "v",
    state: "published",
    edges: [],
    assertions: [],
    unresolved: [],
    ...fields,
  };
}

export const threeClaims = dates([
  claim("c1", "modified", {
    at: "2026-03-12T14:05:00Z",
    source_zone: "-04:00",
    zone_state: "offset",
  }),
  claim(
    "c2",
    "authored",
    { at: "2026-03-11T03:40:00Z", source_zone: "-04:00", zone_state: "offset" },
    true,
  ),
  claim("c3", "document_date", { state: "date", precision: "date", date: "2026-03-09" }),
]);

// The safe facts route for one exact version, with no claims or people. App
// tests whose fetch mocks predate Dates and People answer the selected
// version's reads with this instead of an unrelated fallback payload.
export function versionFactsResponse(url: string): Response | undefined {
  const match = /\/api\/v1\/versions\/([^/?]+)\/(dates|people)(?:\?|$)/.exec(url);
  if (!match) return undefined;
  const content_version_id = decodeURIComponent(match[1]!);
  return Response.json(
    match[2] === "dates"
      ? { ...dates([]), content_version_id }
      : { ...people(), content_version_id },
  );
}
