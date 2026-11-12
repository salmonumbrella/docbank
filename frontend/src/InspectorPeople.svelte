<script lang="ts">
  import { Button, Spinner } from "@kenn-io/kit-ui";
  import UserIcon from "@lucide/svelte/icons/user";
  import PersonChip from "./PersonChip.svelte";
  import type { FactRead } from "./documentFacts.svelte.js";
  import { pendingNames, roleRows } from "./documentPeople.js";
  import type { VersionPeople } from "./generated/docbank.js";

  let {
    read,
    onperson,
    onretry,
  }: {
    read: FactRead<VersionPeople>;
    onperson: (id: string) => void;
    onretry: () => void;
  } = $props();

  const people = $derived(read.status === "ready" ? read.value : undefined);
  const rows = $derived(people ? roleRows(people) : []);
  const pending = $derived(people ? pendingNames(people) : []);
  const found = $derived(rows.some((row) => row.people.length > 0) || pending.length > 0);
</script>

<section class="fact-section" aria-label="People">
  <h3>People</h3>
  {#if read.status === "idle" || read.status === "loading"}
    <p class="fact-note"><Spinner size={13} label="Loading people" /></p>
  {:else if read.status === "error"}
    <p class="fact-note" role="status">
      People couldn't be loaded.
      <Button size="sm" surface="soft" onclick={onretry}>Retry</Button>
    </p>
  {:else if people?.state === "pending"}
    <p class="fact-note" role="status">People are still being worked out.</p>
  {:else if people?.state === "failed"}
    <p class="fact-note" role="status">People couldn't be worked out for this version.</p>
  {:else}
    {#if !found}<p class="fact-note">No people found.</p>{/if}
    <dl class="people-grid">
      {#each rows as row (row.role)}
        <div>
          <dt>{row.label}</dt>
          <dd>
            {#each row.people as person (person.id)}<PersonChip
                {person}
                onselect={onperson}
              />{:else}<span class="muted">—</span>{/each}
          </dd>
        </div>
      {/each}
      {#if pending.length}
        <div>
          <dt>In metadata</dt>
          <dd>
            {#each pending as name (name.candidateID)}<span class="pending-chip"
                ><UserIcon size={12} aria-hidden="true" /><span>{name.name}</span><span
                  class="muted">needs review</span
                ></span
              >{/each}
          </dd>
        </div>
      {/if}
    </dl>
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
  h3 {
    margin: 0;
    font-size: var(--font-size-sm);
    font-weight: 500;
  }
  .fact-note {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--space-4);
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
  .people-grid {
    display: grid;
    grid-template-columns: minmax(84px, max-content) minmax(0, 1fr);
    gap: var(--space-4) var(--space-5);
    align-items: baseline;
    margin: 0;
    min-width: 0;
  }
  .people-grid > div {
    display: contents;
  }
  dt {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    white-space: nowrap;
  }
  dd {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-3);
    min-width: 0;
    margin: 0;
  }
  .muted {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
  .pending-chip {
    display: inline-flex;
    align-items: center;
    gap: var(--space-3);
    max-width: 100%;
    padding: 2px var(--space-4);
    border: 1px dashed var(--border-default);
    border-radius: 999px;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    overflow-wrap: anywhere;
  }
  .pending-chip .muted {
    font-size: var(--font-size-xs);
  }
</style>
