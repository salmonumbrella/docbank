<script lang="ts">
  import XIcon from "@lucide/svelte/icons/x";
  import { Button, Chip, IconButton, SegmentedControl, TextInput, Typeahead } from "@kenn-io/kit-ui";
  import { APIError } from "./api-transport.js";
  import {
    createParty,
    type ContractParty,
    type CreatePartyRequestKind,
    type Party,
  } from "./generated/docbank.js";
  import { contractProblem } from "./contracts.js";

  let {
    session,
    role,
    label,
    chosen,
    catalog,
    catalogState,
    disabled,
    onadd,
    onremove,
    onretry,
    onexisting,
    onauthfailure,
  }: {
    session: string;
    role: "own" | "counterparty";
    label: string;
    chosen: ContractParty[];
    catalog: Party[];
    catalogState: "loading" | "ready" | "error";
    disabled: boolean;
    onadd: (party: Party) => void;
    onremove: (party: ContractParty) => void;
    onretry: () => void;
    onexisting: (partyID: string) => Promise<Party | undefined>;
    onauthfailure: (cause: unknown) => void;
  } = $props();

  const id = $props.id();
  const retryRow = "\0more";
  let query = $state("");
  let creating = $state<{ name: string; kind: CreatePartyRequestKind } | null>(null);
  let busy = $state(false);
  let note = $state("");
  let error = $state("");

  const fold = (text: string) => text.trim().toLowerCase();
  // The catalog is the whole active list once ready, so matching is local.
  const offered = $derived(
    catalog.filter(
      (party) =>
        (role === "counterparty" || party.own) &&
        !chosen.some((row) => row.party_id === party.party_id) &&
        fold(party.display_name).includes(fold(query)),
    ),
  );
  const exactName = $derived(catalog.some((party) => fold(party.display_name) === fold(query)));
  const adder = role === "own" ? "Add own entity" : "Add counterparty";

  function choose(value: string): boolean {
    note = "";
    error = "";
    if (value === retryRow) {
      onretry();
      return false;
    }
    const party = catalog.find((item) => item.party_id === value);
    if (party) onadd(party);
    else creating = { name: value, kind: "organization" };
    return true;
  }

  async function create(): Promise<void> {
    if (!creating) return;
    busy = true;
    error = "";
    try {
      const party = await createParty(
        { kind: creating.kind, display_name: creating.name.trim(), own: role === "own" },
        { session },
      );
      creating = null;
      onadd(party);
    } catch (cause) {
      if (cause instanceof APIError && cause.status === 401) return onauthfailure(cause);
      if (!(cause instanceof APIError) || cause.code !== "party_exists" || !cause.partyID) {
        error = contractProblem(cause);
        return;
      }
      // The existing party is chosen by ID; the create is never resent.
      creating = null;
      const existing = await onexisting(cause.partyID);
      if (!existing) error = "That party is no longer available. Retry parties.";
      else if (role === "own" && !existing.own)
        error = `${existing.display_name} already exists and isn't one of your own entities.`;
      else {
        onadd(existing);
        note = `${existing.display_name} already exists, so it was chosen.`;
      }
    } finally {
      busy = false;
    }
  }
</script>

<div class="tool-field tool-span-3">
  <span class="tool-label">{label}</span>
  {#if chosen.length}
    <ul class="tool-list">
      {#each chosen as party (party.party_id)}
        <li>
          <div class="tool-row-main">
            <strong>{party.display_name}</strong>
            <span>{party.kind === "person" ? "Person" : "Organization"}</span>
          </div>
          <div class="tool-row-actions">
            {#if party.retired_at}<Chip size="xs" tone="neutral">Retired</Chip>{/if}
            <IconButton
              size="sm"
              ariaLabel={`Remove ${party.display_name}`}
              title="Remove"
              {disabled}
              onclick={() => onremove(party)}><XIcon size="14" aria-hidden="true" /></IconButton
            >
          </div>
        </li>
      {/each}
    </ul>
  {/if}
  <Typeahead
    remote
    title={adder}
    fallbackLabel={adder}
    placeholder="Find or create a party"
    value=""
    disabled={disabled || busy}
    options={[
      ...offered.map((party) => ({ name: party.party_id, label: party.display_name })),
      ...(catalogState === "error" ? [{ name: retryRow, label: "Retry" }] : []),
    ]}
    loading={catalogState === "loading"}
    loadingLabel={`Loading parties… ${catalog.length} so far`}
    error={catalogState === "error"
      ? `Only ${catalog.length} parties loaded. Retry to search the rest.`
      : ""}
    emptyLabel="No matching parties"
    allowCustom={catalogState === "ready" && !exactName}
    customLabel={'Create party "{query}"'}
    onquery={(value) => (query = value)}
    onselect={choose}
  />
  {#if creating}
    <div class="create" role="group" aria-label="New party">
      <div class="tool-field">
        <label for={`${id}-name`}>Name</label>
        <TextInput id={`${id}-name`} block bind:value={creating.name} disabled={busy} />
      </div>
      <SegmentedControl
        ariaLabel="Kind"
        value={creating.kind}
        disabled={busy}
        options={[
          { value: "organization", label: "Organization" },
          { value: "person", label: "Person" },
        ]}
        onchange={(kind) => {
          if (creating) creating.kind = kind as CreatePartyRequestKind;
        }}
      />
      {#if role === "own"}<p class="tool-muted">Created as one of your own entities.</p>{/if}
      <div class="tool-actions">
        <Button size="sm" disabled={busy} onclick={() => (creating = null)}>Cancel</Button>
        <Button
          size="sm"
          tone="info"
          surface="solid"
          disabled={busy || !creating.name.trim()}
          onclick={() => void create()}>Create party</Button
        >
      </div>
    </div>
  {/if}
  {#if note}<p class="tool-muted" role="status">{note}</p>{/if}
  {#if error}<p class="tool-error" role="alert">{error}</p>{/if}
</div>

<style>
  .create {
    display: grid;
    gap: var(--space-4);
    padding: var(--space-5);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
  }
</style>
