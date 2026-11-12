<script lang="ts">
  import { Avatar, Chip } from "@kenn-io/kit-ui";
  import type { DocumentFacts } from "./documentFacts.svelte.js";
  import { dateKindLabel, formatWhen, precisionLabel } from "./documentDates.js";
  import { tablePeople } from "./documentPeople.js";
  import type { TimelineZone } from "./timelineConfig.js";

  // versionID is undefined for folders and while the columns are hidden, so
  // the cells stay empty and nothing is read.
  let {
    versionID,
    facts,
    zone,
  }: { versionID?: string; facts?: DocumentFacts; zone: TimelineZone } = $props();

  const read = $derived(versionID && facts ? facts.get(versionID) : undefined);
  const dates = $derived(read?.dates.status === "ready" ? read.dates.value : undefined);
  const people = $derived(read?.people.status === "ready" ? read.people.value : undefined);
  const primary = $derived(dates?.claims.find((claim) => claim.primary));
  const shown = $derived.by(() => {
    if (!primary) return undefined;
    const when = primary.when;
    if (when.state === "exact" && !zone?.supported)
      return { value: "Zone unavailable", floating: false };
    return formatWhen(when, zone);
  });
  const settled = $derived(people?.state !== "pending" && people?.state !== "failed");
  // A pending or failed derivation still shows the independent operator
  // assertions; only derived edges and candidates wait for it.
  const team = $derived(
    people ? tablePeople(settled ? people : { ...people, edges: [], unresolved: [] }) : undefined,
  );
  const visible = $derived(team?.people.slice(0, 3) ?? []);
  const more = $derived((team?.people.length ?? 0) - visible.length);
</script>

<td class="facts-cell col-facts">
  {#if read?.dates.status === "error" || dates?.state === "failed"}<span class="fact-muted"
      >Unavailable</span
    >{:else if dates?.state === "pending"}<span class="fact-muted">Pending</span
    >{:else if dates && primary && shown}<div class="fact-date">
      <span>{shown.value}</span>
      <span class="sub-date"
        >{dateKindLabel(primary.date_kind)} · {precisionLabel(
          primary.when.precision,
        )}{shown.floating ? " · floating" : ""}</span
      >
    </div>{:else if dates}<span class="fact-muted">—</span>{/if}
</td>
<td class="facts-cell col-facts">
  {#if read?.people.status === "error"}<span class="fact-muted">Unavailable</span
    >{:else if team}<div class="fact-people">
      {#if visible.length}<span class="avatar-stack"
          >{#each visible as person (person.id)}<span class="stack-item"
              ><Avatar name={person.name} colorKey={person.colorKey} size={20} /></span
            >{/each}</span
        >{#if more > 0}<span class="fact-muted">+{more}</span>{/if}{/if}
      {#if !settled}<span class="fact-muted"
          >{people?.state === "pending" ? "Pending" : "Unavailable"}</span
        >{:else if !visible.length && !team.review}<span class="fact-muted">—</span>{/if}
      {#if team.review}<Chip size="xs" tone="warning" uppercase={false}
          >{team.review} to review</Chip
        >{/if}
    </div>{/if}
</td>

<style>
  .facts-cell {
    white-space: nowrap;
  }
  .fact-date {
    display: grid;
    line-height: 1.3;
  }
  .sub-date,
  .fact-muted {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
  .fact-people {
    display: flex;
    align-items: center;
    gap: var(--space-3);
  }
  .avatar-stack {
    display: inline-flex;
  }
  .stack-item {
    display: inline-flex;
    border-radius: 50%;
    box-shadow: 0 0 0 1.5px var(--bg-surface);
  }
  .stack-item + .stack-item {
    margin-left: -2px;
  }
</style>
