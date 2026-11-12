<script lang="ts">
  import { onDestroy, untrack } from "svelte";
  import { Button, Chip, SelectDropdown, Spinner, Typeahead } from "@kenn-io/kit-ui";
  import "./toolForm.css";
  import AppTooltip from "./AppTooltip.svelte";
  import { APIError, holdMutations } from "./api-transport.js";
  import {
    linkContractDocument,
    listContracts,
    listNodeContracts,
    unlinkContractDocument,
    type Contract,
    type ContractDocument,
    type LinkContractDocumentRole,
    type Node,
  } from "./generated/docbank.js";
  import {
    contractPageSize,
    contractProblem,
    contractStatusLabel,
    documentRoleLabel,
    documentRoleOptions,
    revisionFence,
    statusTone,
    unconfirmed,
  } from "./contracts.js";
  import type { SelectedSource } from "./selectedSource.js";

  let {
    session,
    node,
    source,
    actionsDisabled = false,
    onauthfailure,
  }: {
    session: string;
    node: Node;
    source?: SelectedSource;
    actionsDisabled?: boolean;
    onauthfailure: (cause: unknown) => void;
  } = $props();

  const id = $props.id();
  let items = $state<Contract[]>([]);
  let loading = $state(true);
  let error = $state("");
  let reloads = $state(0);
  let linking = $state(false);
  let results = $state<Contract[]>([]);
  let searching = $state(false);
  let picked = $state<Contract>();
  let role = $state("");
  let busy = $state(false);
  let confirming = $state("");
  let done = $state("");
  let failure = $state("");
  let alive = true;
  let searchController: AbortController | undefined;
  onDestroy(() => {
    alive = false;
    searchController?.abort();
  });

  // Links pin the exact version the inspector shows, never the live head.
  const exact = $derived(source !== undefined && source.nodeID === node.id);
  const linkBlocked = $derived(
    actionsDisabled ? "Refresh this view first" : exact ? "" : "Open the file to link it",
  );

  $effect(() => {
    const nodeID = node.id;
    void source?.key;
    void reloads;
    const controller = new AbortController();
    loading = true;
    error = "";
    listNodeContracts(
      nodeID,
      { limit: contractPageSize, offset: 0 },
      {
        session,
        signal: controller.signal,
      },
    ).then(
      (page) => {
        if (controller.signal.aborted) return;
        items = page.items;
        loading = false;
      },
      (cause: unknown) => {
        if (controller.signal.aborted) return;
        loading = false;
        if (!authFailure(cause)) error = "Contracts couldn't be loaded.";
      },
    );
    return () => controller.abort();
  });

  function authFailure(cause: unknown): boolean {
    if (!(cause instanceof APIError) || cause.status !== 401) return false;
    untrack(() => onauthfailure(cause));
    return true;
  }

  function search(query: string): void {
    searchController?.abort();
    const controller = (searchController = new AbortController());
    searching = true;
    const q = query.trim();
    listContracts(q ? { q, limit: 20 } : { limit: 20 }, { session, signal: controller.signal })
      .then((page) => {
        if (!controller.signal.aborted) results = page.items;
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted && !authFailure(cause)) results = [];
      })
      .finally(() => {
        if (!controller.signal.aborted) searching = false;
      });
  }

  // write runs one link change, then reloads whatever its outcome, so an
  // unconfirmed write is never retried against a list that may be stale.
  async function write(run: () => Promise<Contract>, success: string): Promise<boolean> {
    busy = true;
    done = "";
    failure = "";
    try {
      await holdMutations(run);
      if (alive) done = success;
      return true;
    } catch (cause) {
      if (!alive || authFailure(cause)) return false;
      failure =
        cause instanceof APIError && cause.status === 412
          ? "That contract changed. Try again."
          : unconfirmed(cause)
            ? "Docbank didn't confirm the change. The list shows what is saved now."
            : contractProblem(cause);
      return false;
    } finally {
      busy = false;
      confirming = "";
      reloads += 1;
    }
  }

  async function link(): Promise<void> {
    if (!picked || !role || !source || !exact) return;
    const target = picked;
    const chosen = role as LinkContractDocumentRole;
    const params = { role: chosen, content_version_id: source.versionID };
    const ok = await write(
      () =>
        linkContractDocument(target.contract_id, node.id, params, revisionFence(target.revision), {
          session,
        }),
      `Linked as ${documentRoleLabel(chosen)} to ${target.title}.`,
    );
    // A refused link needs a fresh contract revision, so the choice resets.
    picked = undefined;
    if (ok) {
      linking = false;
      role = "";
    }
  }

  function unlink(contract: Contract, row: ContractDocument): void {
    const params = {
      role: row.role as LinkContractDocumentRole,
      content_version_id: row.content_version_id,
    };
    const fence = revisionFence(contract.revision);
    void write(
      () => unlinkContractDocument(contract.contract_id, node.id, params, fence, { session }),
      `Unlinked ${documentRoleLabel(row.role)} from ${contract.title}.`,
    );
  }

  // Only this file's links show; a contract may link other files too.
  const linksOf = (contract: Contract) =>
    contract.documents.filter((row) => row.node_id === node.id);
  const rowKey = (contract: Contract, row: ContractDocument) =>
    `${contract.contract_id}:${row.role}:${row.content_version_id}`;
</script>

<section class="contracts" aria-labelledby={`${id}-heading`}>
  <header>
    <h3 id={`${id}-heading`}>Contracts</h3>
    <AppTooltip text={linkBlocked}
      ><Button
        size="sm"
        surface="soft"
        disabled={!!linkBlocked || busy}
        ariaExpanded={linking}
        onclick={() => (linking = !linking)}>Link to contract</Button
      ></AppTooltip
    >
  </header>
  {#if linking && !linkBlocked}
    <div class="panel">
      <Typeahead
        remote
        title="Contract"
        placeholder="Find a contract"
        fallbackLabel="Find a contract"
        value={picked?.contract_id ?? ""}
        options={results.map((contract) => ({
          name: contract.contract_id,
          label: `${contract.title} · ${contractStatusLabel(contract.effective_status)}`,
        }))}
        loading={searching}
        emptyLabel="No matching contracts"
        disabled={busy}
        onquery={search}
        onselect={(value) => {
          picked = results.find((contract) => contract.contract_id === value);
          return picked !== undefined;
        }}
      />
      <SelectDropdown
        title="Role"
        value={role}
        options={[{ value: "", label: "Choose a role" }, ...documentRoleOptions]}
        disabled={busy}
        onchange={(value) => (role = value)}
      />
      <div class="tool-actions">
        <Button size="sm" disabled={busy} onclick={() => (linking = false)}>Cancel</Button>
        <Button
          size="sm"
          tone="info"
          surface="solid"
          disabled={busy || !picked || !role}
          onclick={() => void link()}>Link</Button
        >
      </div>
    </div>
  {/if}
  {#if done}<p class="muted" role="status">{done}</p>{/if}
  {#if failure}<p class="tool-error" role="alert">{failure}</p>{/if}
  {#if loading}<span role="status"><Spinner size={13} /> Loading contracts…</span>
  {:else if error}
    <div class="row">
      {error}<Button size="sm" surface="soft" onclick={() => (reloads += 1)}>Retry contracts</Button
      >
    </div>
  {:else if items.length === 0}
    <p class="muted">Not linked to a contract</p>
  {:else}
    <ul>
      {#each items as contract (contract.contract_id)}
        <li aria-labelledby={`${id}-${contract.contract_id}`}>
          <div class="row">
            <strong id={`${id}-${contract.contract_id}`}>{contract.title}</strong>
            <Chip size="xs" tone={statusTone(contract.effective_status)}
              >{contractStatusLabel(contract.effective_status)}</Chip
            >
          </div>
          {#each linksOf(contract) as row (rowKey(contract, row))}
            <div class="row">
              <span>{documentRoleLabel(row.role)}</span>
              {#if !row.current}<Chip size="xs" tone="info">Older version</Chip>{/if}
              {#if !row.available}<Chip size="xs" tone="warning">Unavailable</Chip>{/if}
              <span class="actions">
                {#if confirming === rowKey(contract, row)}
                  Unlink?
                  <Button size="sm" disabled={busy} onclick={() => (confirming = "")}>Cancel</Button
                  >
                  <Button
                    size="sm"
                    tone="danger"
                    ariaLabel="Confirm unlink"
                    disabled={busy}
                    onclick={() => unlink(contract, row)}>Unlink</Button
                  >
                {:else}
                  <Button
                    size="sm"
                    surface="soft"
                    disabled={busy || actionsDisabled}
                    onclick={() => (confirming = rowKey(contract, row))}>Unlink</Button
                  >
                {/if}
              </span>
            </div>
          {/each}
        </li>
      {/each}
    </ul>
    {#if items.length === contractPageSize}
      <p class="muted">Showing the first {contractPageSize} contracts.</p>
    {/if}
  {/if}
</section>

<style>
  .contracts {
    display: grid;
    gap: var(--space-5);
    border-top: 1px solid var(--border-muted);
    padding-top: var(--space-6);
    min-width: 0;
    font-size: var(--font-size-sm);
  }
  header,
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
  }
  header {
    justify-content: space-between;
  }
  h3 {
    margin: 0;
    font-size: inherit;
    font-weight: 500;
  }
  .panel {
    display: grid;
    gap: var(--space-4);
  }
  .panel :global(.kit-typeahead),
  .panel :global(.kit-select-dropdown) {
    width: 100%;
  }
  ul {
    display: grid;
    gap: var(--space-5);
    list-style: none;
    padding: 0;
    margin: 0;
  }
  li {
    display: grid;
    gap: var(--space-3);
    min-width: 0;
  }
  strong {
    overflow-wrap: anywhere;
  }
  .actions {
    display: inline-flex;
    align-items: center;
    gap: var(--space-3);
    margin-left: auto;
  }
  p {
    margin: 0;
  }
  .muted {
    color: var(--text-muted);
  }
</style>
