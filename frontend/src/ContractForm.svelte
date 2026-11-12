<script lang="ts">
  import { onDestroy, untrack } from "svelte";
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
  import XIcon from "@lucide/svelte/icons/x";
  import {
    Button,
    DatePicker,
    IconButton,
    Notice,
    SelectDropdown,
    TextInput,
    Typeahead,
  } from "@kenn-io/kit-ui";
  import "./toolForm.css";
  import PartyPicker from "./PartyPicker.svelte";
  import { APIError, holdMutations } from "./api-transport.js";
  import {
    attachContractParty,
    createContract,
    detachContractParty,
    getContract,
    listParties,
    updateContract,
    type Contract,
    type ContractParty,
    type CreateContractRequest,
    type CreateContractRequestStatus,
    type Party,
  } from "./generated/docbank.js";
  import {
    contractPatch,
    contractProblem,
    contractTypeLabel,
    draftFrom,
    editableStatusOptions,
    partyChanges,
    revisionFence,
    suggestedTypeOptions,
    unconfirmed,
  } from "./contracts.js";

  let {
    session,
    contract,
    onsaved,
    oncancel,
    onauthfailure,
  }: {
    session: string;
    contract?: Contract;
    onsaved: (contract: Contract) => void;
    oncancel: () => void;
    onauthfailure: (cause: unknown) => void;
  } = $props();

  const id = $props.id();
  const pageSize = 1000;
  const dates = [
    { field: "starts_on", label: "Starts", clear: "Clear start date" },
    { field: "expires_on", label: "Expires", clear: "Clear expiry date" },
    { field: "signed_on", label: "Signed", clear: "Clear signed date" },
  ] as const;
  const blocks = [
    { role: "own", label: "Own entities" },
    { role: "counterparty", label: "Counterparties" },
  ] as const;
  const terms = [
    { field: "summary", label: "Summary" },
    { field: "purpose", label: "Purpose" },
    { field: "consideration", label: "Consideration" },
    { field: "termination", label: "Termination" },
  ] as const;
  // base is always the last contract Docbank confirmed.
  let base = $state<Contract | undefined>(untrack(() => contract));
  let draft = $state(draftFrom(untrack(() => contract)));
  let catalog = $state<Party[]>([]);
  let catalogState = $state<"loading" | "ready" | "error">("loading");
  let catalogOffset = 0;
  let saving = $state(false);
  let locked = $state(false);
  let missing = $state(false);
  let conflict = $state("");
  let alert = $state("");
  let alive = true;
  const controller = new AbortController();
  onDestroy(() => {
    alive = false;
    controller.abort();
  });

  const patch = $derived(base ? contractPatch(base, draft) : {});
  const changes = $derived(base ? partyChanges(base.parties, draft.parties) : []);
  const unchanged = $derived(
    base ? Object.keys(patch).length === 0 && changes.length === 0 : !draft.title.trim(),
  );
  const typeOptions = $derived(
    suggestedTypeOptions.some((option) => option.name === draft.contract_type) ||
      !draft.contract_type
      ? suggestedTypeOptions
      : [
          ...suggestedTypeOptions,
          { name: draft.contract_type, label: contractTypeLabel(draft.contract_type) },
        ],
  );

  void loadCatalog(true);

  function authFailure(cause: unknown): boolean {
    if (!(cause instanceof APIError) || cause.status !== 401) return false;
    onauthfailure(cause);
    return true;
  }

  // The API has no name filter, so the whole active catalog is loaded before
  // a picker may claim no match or offer to create a party.
  async function loadCatalog(fromStart: boolean): Promise<void> {
    if (fromStart) {
      catalog = [];
      catalogOffset = 0;
    }
    catalogState = "loading";
    try {
      while (true) {
        const page = await listParties(
          { limit: pageSize, offset: catalogOffset },
          { session, signal: controller.signal },
        );
        catalogOffset += page.items.length;
        const known = new Set(catalog.map((party) => party.party_id));
        catalog = [...catalog, ...page.items.filter((party) => !known.has(party.party_id))];
        if (page.items.length < pageSize) break;
      }
      catalogState = "ready";
    } catch (cause) {
      if (!alive || authFailure(cause)) return;
      catalogState = "error";
    }
  }

  async function existing(partyID: string): Promise<Party | undefined> {
    const find = () => catalog.find((party) => party.party_id === partyID);
    if (!find()) await loadCatalog(true);
    return find();
  }

  function add(party: Party, role: "own" | "counterparty"): void {
    if (!catalog.some((item) => item.party_id === party.party_id)) catalog = [...catalog, party];
    const { party_id, display_name, kind, own, retired_at } = party;
    draft.parties = [...draft.parties, { party_id, display_name, kind, own, retired_at, role }];
  }
  function remove(party: ContractParty): void {
    draft.parties = draft.parties.filter(
      (row) => row.role !== party.role || row.party_id !== party.party_id,
    );
  }

  async function loadLatest(): Promise<void> {
    if (!base) return;
    alert = "";
    try {
      const fresh = await getContract(base.contract_id, undefined, { session });
      if (!alive) return;
      base = fresh;
      draft = draftFrom(fresh);
      conflict = "";
      locked = false;
    } catch (cause) {
      if (!alive || authFailure(cause)) return;
      if (cause instanceof APIError && cause.status === 404) missing = true;
      else alert = contractProblem(cause);
    }
  }

  function createRequest(): CreateContractRequest {
    const body: CreateContractRequest = {
      title: draft.title,
      status: draft.status as CreateContractRequestStatus,
      parties: draft.parties.map(({ party_id, role }) => ({
        party_id,
        role: role as "own" | "counterparty",
      })),
    };
    // Unset dates and empty text are omitted rather than sent.
    for (const field of ["contract_type", ...terms.map((term) => term.field)] as const) {
      const value = draft[field];
      if (value) body[field] = value;
    }
    for (const { field } of dates) {
      const value = draft[field];
      if (value) body[field] = value;
    }
    return body;
  }

  function steps(
    current: Contract,
  ): { label: string; run: (revision: number) => Promise<Contract> }[] {
    const contractID = current.contract_id;
    const fields = patch;
    const options = { session };
    return [
      ...(Object.keys(fields).length
        ? [
            {
              label: "details",
              run: (revision: number) =>
                updateContract(contractID, fields, revisionFence(revision), options),
            },
          ]
        : []),
      ...changes.map(({ attach, party }) => ({
        label: `${attach ? "add" : "remove"} ${party.display_name}`,
        run: (revision: number) =>
          (attach ? attachContractParty : detachContractParty)(
            contractID,
            party.party_id,
            { role: party.role as "own" | "counterparty" },
            revisionFence(revision),
            options,
          ),
      })),
    ];
  }

  async function save(): Promise<void> {
    // A locked form never sends again, even if a click reaches it.
    if (saving || locked || unchanged) return;
    saving = true;
    alert = "";
    conflict = "";
    try {
      await holdMutations(async () => {
        if (!base) {
          try {
            const created = await createContract(createRequest(), { session });
            if (alive) onsaved(created);
          } catch (cause) {
            if (!alive || authFailure(cause)) return;
            locked = unconfirmed(cause);
            alert = locked
              ? "Docbank didn't confirm the new contract. Refresh the register before saving again."
              : contractProblem(cause);
          }
          return;
        }
        // Each step carries the revision the previous confirmed step returned.
        const plan = steps(base);
        const done: string[] = [];
        for (const [index, step] of plan.entries()) {
          try {
            base = await step.run(base.revision);
            done.push(step.label);
          } catch (cause) {
            if (!alive || authFailure(cause)) return;
            failed(
              cause,
              step.label,
              done,
              plan.slice(index).map((item) => item.label),
            );
            return;
          }
        }
        if (alive) onsaved(base);
      });
    } finally {
      saving = false;
    }
  }

  function failed(cause: unknown, step: string, done: string[], rest: string[]): void {
    const saved = done.length ? `Saved: ${done.join(", ")}. ` : "";
    if (unconfirmed(cause)) {
      locked = true;
      alert = `${saved}Docbank didn't confirm ${step}. Reload the contract before saving again.`;
    } else if (cause instanceof APIError && cause.status === 404) {
      missing = true;
    } else {
      const stale = cause instanceof APIError && cause.status === 412;
      if (stale)
        conflict = `This contract changed. It was saved elsewhere after you opened it${
          done.length ? "." : "; nothing was saved."
        }`;
      const reason = stale ? "this contract changed elsewhere." : contractProblem(cause);
      if (done.length || !stale)
        alert = done.length ? `${saved}Not saved: ${rest.join(", ")} — ${reason}` : reason;
    }
  }
</script>

<section class="tool-section contract-form" aria-labelledby={`${id}-heading`}>
  <div class="title">
    <IconButton size="sm" ariaLabel="Cancel" title="Cancel" disabled={saving} onclick={oncancel}
      ><ArrowLeftIcon size="14" aria-hidden="true" /></IconButton
    >
    <h3 id={`${id}-heading`}>{base ? `Edit ${base.title}` : "New contract"}</h3>
  </div>

  {#if missing}
    <div class="tool-heading">
      <p class="tool-muted">This contract no longer exists.</p>
      <Button size="sm" onclick={oncancel}>All contracts</Button>
    </div>
  {:else}
    {#if conflict}
      <Notice tone="warning" message={conflict} actionLabel="Load latest" onaction={loadLatest} />
    {/if}

    <div class="tool-section">
      <h3>Details</h3>
      <div class="tool-grid">
        <div class="tool-field tool-span-all">
          <label for={`${id}-title`}>Title</label>
          <TextInput id={`${id}-title`} block required bind:value={draft.title} disabled={saving} />
        </div>
        <div class="tool-field tool-span-3">
          <span class="tool-label">Type</span>
          <Typeahead
            title="Type"
            placeholder="Type"
            fallbackLabel="No type"
            value={draft.contract_type}
            options={typeOptions}
            allowClear
            clearLabel="No type"
            allowCustom
            customLabel={'Use type "{query}"'}
            disabled={saving}
            onselect={(value) => void (draft.contract_type = value)}
          />
        </div>
        <div class="tool-field tool-span-3">
          <span class="tool-label">Status</span>
          <SelectDropdown
            title="Status"
            value={draft.status}
            options={editableStatusOptions}
            disabled={saving}
            onchange={(value) => (draft.status = value)}
          />
        </div>
        {#each dates as date (date.field)}
          <div class="tool-field tool-span-2">
            <span class="tool-label">{date.label}</span>
            <div class="date-row">
              <DatePicker
                block
                ariaLabel={date.label}
                value={draft[date.field]}
                disabled={saving}
                onchange={(value) => (draft[date.field] = value)}
              />
              {#if draft[date.field]}<IconButton
                  size="sm"
                  ariaLabel={date.clear}
                  title={date.clear}
                  disabled={saving}
                  onclick={() => (draft[date.field] = null)}
                  ><XIcon size="14" aria-hidden="true" /></IconButton
                >{/if}
            </div>
          </div>
        {/each}
      </div>
    </div>

    <div class="tool-section">
      <h3>Parties</h3>
      <div class="tool-grid">
        {#each blocks as block (block.role)}
          <PartyPicker
            {session}
            role={block.role}
            label={block.label}
            chosen={draft.parties.filter((party) => party.role === block.role)}
            {catalog}
            {catalogState}
            disabled={saving}
            onadd={(party) => add(party, block.role)}
            onremove={remove}
            onretry={() => void loadCatalog(false)}
            onexisting={existing}
            {onauthfailure}
          />
        {/each}
      </div>
    </div>

    <div class="tool-section">
      <h3>Terms</h3>
      <div class="tool-grid">
        {#each terms as term (term.field)}
          <div class="tool-field tool-span-3">
            <label for={`${id}-${term.field}`}>{term.label}</label>
            <textarea
              id={`${id}-${term.field}`}
              rows="3"
              bind:value={draft[term.field]}
              disabled={saving}></textarea>
          </div>
        {/each}
      </div>
    </div>

    {#if alert}<p class="tool-error" role="alert">{alert}</p>{/if}
    <div class="tool-actions">
      {#if locked && base}<Button size="sm" onclick={() => void loadLatest()}>Load latest</Button
        >{/if}
      <Button size="sm" disabled={saving} onclick={oncancel}>Cancel</Button>
      <Button
        size="sm"
        tone="info"
        surface="solid"
        disabled={saving || locked || unchanged}
        onclick={() => void save()}
        >{saving ? "Saving…" : base ? "Save changes" : "Create contract"}</Button
      >
    </div>
  {/if}
</section>

<style>
  .contract-form {
    gap: var(--space-7);
  }
  .title {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
  }
  .title h3 {
    margin: 0;
    font-size: var(--font-size-md);
    overflow-wrap: anywhere;
  }
  .date-row {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
  }
  .date-row > :global(:first-child) {
    flex: 1;
    min-width: 0;
  }
</style>
