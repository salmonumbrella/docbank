<script lang="ts">
  import { Button, Chip, Spinner } from "@kenn-io/kit-ui";
  import StarIcon from "@lucide/svelte/icons/star";
  import ClockIcon from "@lucide/svelte/icons/clock";
  import type { FactRead } from "./documentFacts.svelte.js";
  import { claimMeta, dateKindLabel, formatWhen } from "./documentDates.js";
  import type { VersionDates } from "./generated/docbank.js";
  import type { TimelineZone } from "./timelineConfig.js";

  let {
    read,
    zone,
    onretry,
  }: { read: FactRead<VersionDates>; zone: TimelineZone; onretry: () => void } = $props();

  const dates = $derived(read.status === "ready" ? read.value : undefined);
  // The primary claim leads; the rest keep the server's order.
  const claims = $derived(
    dates
      ? [...dates.claims.filter((c) => c.primary), ...dates.claims.filter((c) => !c.primary)]
      : [],
  );
  const hint = $derived(
    zone?.supported
      ? `Times show in ${zone.name}.`
      : `Timeline zone unavailable${zone?.name ? ` (${zone.name})` : ""}; exact times are not shown.`,
  );
</script>

<section class="fact-section" aria-label="Dates">
  <div class="fact-head">
    <h3>Dates</h3>
    {#if dates && claims.length}<span class="fact-count"
        >{claims.length} claim{claims.length === 1 ? "" : "s"}</span
      >{/if}
  </div>
  {#if read.status === "idle" || read.status === "loading"}
    <p class="fact-note"><Spinner size={13} label="Loading dates" /></p>
  {:else if read.status === "error"}
    <p class="fact-note" role="status">
      Dates couldn't be loaded.
      <Button size="sm" surface="soft" onclick={onretry}>Retry</Button>
    </p>
  {:else if dates?.state === "pending"}
    <p class="fact-note" role="status">Dates are still being worked out.</p>
  {:else if dates?.state === "failed"}
    <p class="fact-note" role="status">Dates couldn't be worked out for this version.</p>
  {:else if claims.length === 0}
    <p class="fact-note">No dates found.</p>
  {:else}
    <ul class="claims">
      {#each claims as claim (`${claim.event_id}:${claim.evidence_id}`)}
        {@const shown = formatWhen(claim.when, zone)}
        <li class="claim" class:primary={claim.primary}>
          <span class="claim-icon" aria-hidden="true"
            >{#if claim.primary}<StarIcon size={14} />{:else}<ClockIcon size={14} />{/if}</span
          >
          <div class="claim-body">
            <div class="claim-top">
              <span class="claim-kind">{dateKindLabel(claim.date_kind)}</span>
              <span class="claim-value">{shown.value}</span>
              {#if claim.primary}<Chip size="sm" tone="info" uppercase={false}>Primary</Chip>{/if}
            </div>
            <div class="claim-meta">{claimMeta(claim, zone)}</div>
          </div>
        </li>
      {/each}
    </ul>
    <p class="fact-hint">{hint}</p>
  {/if}
</section>

<style>
  .fact-section {
    display: grid;
    gap: var(--space-4);
    min-width: 0;
    padding-top: var(--space-5);
    border-top: 1px solid var(--border-muted);
  }
  .fact-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: var(--space-4);
  }
  h3 {
    margin: 0;
    font-size: var(--font-size-sm);
    font-weight: 500;
  }
  .fact-count,
  .fact-hint,
  .fact-note,
  .claim-meta {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
  .fact-note,
  .fact-hint {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--space-4);
    margin: 0;
    font-size: var(--font-size-sm);
  }
  .fact-hint {
    font-size: var(--font-size-xs);
  }
  .claims {
    display: grid;
    gap: var(--space-3);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .claim {
    display: grid;
    grid-template-columns: 18px minmax(0, 1fr);
    gap: var(--space-3);
    padding: var(--space-3) var(--space-4);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
  }
  .claim.primary {
    border-color: color-mix(in srgb, var(--accent-blue) 35%, var(--border-muted));
    background: color-mix(in srgb, var(--accent-blue) 5%, var(--bg-surface));
  }
  .claim-icon {
    display: flex;
    padding-top: 2px;
    color: var(--text-muted);
  }
  .claim.primary .claim-icon {
    color: var(--accent-blue);
  }
  .claim-body {
    display: grid;
    gap: 2px;
    min-width: 0;
  }
  .claim-top {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: var(--space-2) var(--space-4);
    font-size: var(--font-size-sm);
  }
  .claim-kind {
    color: var(--text-primary);
    font-weight: 600;
  }
  .claim-value {
    color: var(--text-primary);
    font-variant-numeric: tabular-nums;
    overflow-wrap: anywhere;
  }
  .claim-meta {
    overflow-wrap: anywhere;
  }
</style>
