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
const knownKinds = new Set(
  "authored document_date sent received created modified captured started ended printed accessed produced exported imported observed".split(
    " ",
  ),
);
export const precisionLabel = (p: string) => precisionLabels[p] ?? p;
export const dateKindLabel = (k: string) => {
  if (k === "vault_recorded") return "Recorded in vault";
  if (!knownKinds.has(k)) return k;
  const label = k.replaceAll("_", " ");
  return label[0]!.toUpperCase() + label.slice(1);
};
export const claimBasisLabel = (claim: VersionDateClaim) =>
  (claim.source_kind_raw || claim.evidence_kind).replaceAll("_", " ");

// Civil fields are formatted in UTC solely to preserve their written digits.
function civilDate(value: string): Date {
  const [date, time = ""] = value.split("T");
  const [year, month = "1", day = "1"] = date!.split("-");
  const [hour = "0", minute = "0", second = "0"] = time.split(":");
  const result = new Date(0);
  result.setUTCFullYear(Number(year), Number(month) - 1, Number(day));
  const [seconds, fraction = ""] = second.split(".");
  result.setUTCHours(
    Number(hour),
    Number(minute),
    Number(seconds),
    Number(fraction.padEnd(3, "0").slice(0, 3)),
  );
  return result;
}

function clockOptions(precision: string, source: string): Intl.DateTimeFormatOptions {
  const options: Intl.DateTimeFormatOptions = { hour: "numeric" };
  if (precision !== "hour") options.minute = "2-digit";
  if (precision === "second" || precision === "fraction") options.second = "2-digit";
  if (precision === "fraction" && /\.\d+/.test(source)) options.fractionalSecondDigits = 3;
  return options;
}

// Intl accepts at most three fractional digits; retain the source's remaining digits.
function formatClock(
  formatter: Intl.DateTimeFormat,
  date: Date,
  source: string,
  precision: string,
) {
  const fraction = precision === "fraction" ? source.match(/\.(\d+)/)?.[1] : undefined;
  if (!fraction) return formatter.format(date);
  return formatter
    .formatToParts(date)
    .map((part) => (part.type === "fractionalSecond" ? fraction : part.value))
    .join("");
}

// Civil dates and periods carry no zone, so they get no zone note.
function sourceZoneNote(when: WhenV1): string {
  return when.state === "floating"
    ? "floating (no zone)"
    : when.state !== "exact"
      ? ""
      : when.zone_state === "unknown"
        ? "source zone not recorded"
        : when.source_zone || "source zone not recorded";
}

export function formatWhen(
  when: WhenV1 & { end_date?: string },
  zone: TimelineZone,
  locale = "en-US",
) {
  const floating = when.state === "floating";
  const zoneNote = sourceZoneNote(when);
  if (when.state === "exact") {
    if (!zone?.supported)
      return {
        value: `Timeline zone unavailable${zone?.name ? ` (${zone.name})` : ""}`,
        zoneNote,
        floating,
      };
    const instant = new Date(when.at!);
    // Zone offsets can introduce minutes or seconds absent in the source's
    // precision. Show those fields to avoid displaying a different instant;
    // the claim's precision label remains unchanged.
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: zone.name,
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
    }).formatToParts(instant);
    const minute = parts.find((part) => part.type === "minute")?.value;
    const second = parts.find((part) => part.type === "second")?.value;
    const displayPrecision =
      (when.precision === "hour" || when.precision === "minute") && second !== "00"
        ? "second"
        : when.precision === "hour" && minute !== "00"
          ? "minute"
          : when.precision;
    const formatter = new Intl.DateTimeFormat(locale, {
      timeZone: zone.name,
      year: "numeric",
      month: "short",
      day: "numeric",
      ...clockOptions(displayPrecision, when.local || when.at!),
      timeZoneName: "short",
    });
    return {
      value: formatClock(formatter, instant, when.local || when.at!, when.precision),
      zoneNote,
      floating,
    };
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
    Object.assign(options, clockOptions(when.precision, civil));
  }
  const formatter = new Intl.DateTimeFormat(locale, options);
  const value = when.end_date
    ? formatter.formatRange(civilDate(civil), civilDate(when.end_date))
    : formatClock(formatter, civilDate(civil), civil, when.precision);
  return { value: floating ? `${value} (no zone)` : value, zoneNote, floating };
}

export function claimMeta(claim: VersionDateClaim, _zone: TimelineZone): string {
  const zoneNote = sourceZoneNote(claim.when);
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
