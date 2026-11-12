import type { VersionDateClaim, WhenV1 } from "./generated/docbank.js";
import type { TimelineZone } from "./timelineConfig.js";

const precisionLabels: Record<string, string> = {
  second: "to the second",
  fraction: "to the second",
  minute: "to the minute",
  hour: "to the hour",
  date: "date only",
  month: "month only",
  year: "year only",
};
const kindLabels: Record<string, string> = {
  authored: "Authored",
  document_date: "Document date",
  vault_recorded: "Recorded in vault",
  sent: "Sent",
  received: "Received",
  created: "Created",
  modified: "Modified",
  captured: "Captured",
  started: "Started",
  ended: "Ended",
  printed: "Printed",
  accessed: "Accessed",
  produced: "Produced",
  exported: "Exported",
  imported: "Imported",
  observed: "Observed",
};
export const precisionLabel = (p: string) => precisionLabels[p] ?? p;
export const dateKindLabel = (k: string) => kindLabels[k] ?? k;
export const claimBasisLabel = (claim: VersionDateClaim) =>
  (claim.source_kind_raw || claim.evidence_kind).replaceAll("_", " ");

// Civil fields are formatted in UTC solely to preserve their written digits.
function civilDate(value: string): Date {
  const [date, time = ""] = value.split("T");
  const [year, month = "1", day = "1"] = date!.split("-");
  const [hour = "0", minute = "0", second = "0"] = time.split(":");
  const result = new Date(0);
  result.setUTCFullYear(Number(year), Number(month) - 1, Number(day));
  result.setUTCHours(Number(hour), Number(minute), Number(second), 0);
  return result;
}

export function formatWhen(
  when: WhenV1 & { end_date?: string },
  zone: TimelineZone,
  locale = "en-US",
) {
  const floating = when.state === "floating";
  // Civil dates and periods carry no zone, so they get no zone note.
  const zoneNote = floating
    ? "floating (no zone)"
    : when.state !== "exact"
      ? ""
      : when.zone_state === "unknown"
        ? "source zone not recorded"
        : when.source_zone || "source zone not recorded";
  if (when.state === "exact") {
    if (!zone?.supported)
      return {
        value: `Timeline zone unavailable${zone?.name ? ` (${zone.name})` : ""}`,
        zoneNote,
        floating,
      };
    const value = new Intl.DateTimeFormat(locale, {
      timeZone: zone.name,
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
      timeZoneName: "short",
    }).format(new Date(when.at!));
    return { value, zoneNote, floating };
  }
  const civil = when.local || when.date || when.period;
  if (!civil) return { value: "—", zoneNote, floating };
  const options: Intl.DateTimeFormatOptions = {
    timeZone: "UTC",
    year: "numeric",
    month: when.precision === "month" ? "long" : "short",
  };
  if (when.precision === "year") delete options.month;
  else if (when.precision !== "month") options.day = "numeric";
  if (floating) {
    options.hour = "numeric";
    options.minute = "2-digit";
  }
  const formatter = new Intl.DateTimeFormat(locale, options);
  const value = when.end_date
    ? formatter.formatRange(civilDate(civil), civilDate(when.end_date))
    : formatter.format(civilDate(civil));
  return { value: floating ? `${value} (no zone)` : value, zoneNote, floating };
}

export function claimMeta(claim: VersionDateClaim, zone: TimelineZone): string {
  const { zoneNote } = formatWhen(claim.when, zone);
  return [
    claimBasisLabel(claim),
    precisionLabel(claim.when.precision),
    claim.claim_basis === "docbank_observed"
      ? "Docbank observed"
      : claim.claim_basis.replaceAll("_", " "),
    zoneNote,
  ]
    .filter(Boolean)
    .join(" · ");
}
