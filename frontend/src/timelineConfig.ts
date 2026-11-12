export type TimelineZone = { name: string; supported: boolean } | undefined;

export function timelineZone(): TimelineZone {
  const meta = document.querySelector<HTMLMetaElement>('meta[name="docbank-timeline-timezone"]');
  if (!meta) return undefined;
  const name = meta.content;
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: name });
    return { name, supported: true };
  } catch {
    return { name, supported: false };
  }
}
