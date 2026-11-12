<script lang="ts">
  import { Avatar } from "@kenn-io/kit-ui";
  import type { PersonRef } from "./documentPeople.js";

  let { person, onselect }: { person: PersonRef; onselect: (id: string) => void } = $props();
</script>

<button type="button" class="person-chip" onclick={() => onselect(person.id)}>
  <Avatar name={person.name} colorKey={person.colorKey} size={20} decorative />
  <span>{person.name}</span>
</button>

<style>
  .person-chip {
    display: inline-flex;
    align-items: center;
    gap: var(--space-3);
    max-width: 100%;
    min-width: 0;
    padding: 2px var(--space-4) 2px 2px;
    border: 1px solid var(--border-default);
    border-radius: 999px;
    background: var(--bg-surface);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    cursor: pointer;
  }
  .person-chip:hover {
    background: var(--bg-surface-hover);
  }
  .person-chip:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: 1px;
  }
  .person-chip > span {
    min-width: 0;
    overflow-wrap: anywhere;
  }
</style>
