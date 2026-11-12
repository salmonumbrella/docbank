import { untrack } from "svelte";
import type { Attachment } from "svelte/attachments";
import { APIError } from "./api-transport.js";
import {
  getVersionDates,
  getVersionPeople,
  type VersionDates,
  type VersionPeople,
} from "./generated/docbank.js";

export type FactRead<T> = { status: "idle" | "loading" | "ready" | "error"; value?: T };
type Resource = "dates" | "people";
type Facts = { dates: FactRead<VersionDates>; people: FactRead<VersionPeople> };
const idle: Facts = { dates: { status: "idle" }, people: { status: "idle" } };
type Entry = {
  facts: Facts;
  rows: number;
  retained: number;
  controllers: Partial<Record<Resource, AbortController>>;
};

// One owner belongs to one view and session. Interest is held by the selected
// version or by rows actually intersecting the viewport.
export class DocumentFacts {
  private values = $state<Record<string, Facts>>({});
  private entries = new Map<string, Entry>();
  private elements = new Map<Element, { id: string; visible: boolean }>();
  private observer?: IntersectionObserver;
  private active = 0;
  private stopped = false;

  constructor(
    private session: string,
    private onauthfailure: (cause: unknown) => void,
  ) {
    // Without IntersectionObserver no row is ever visible, so rows fetch nothing.
    if (typeof IntersectionObserver === "function")
      this.observer = new IntersectionObserver((changes) => {
        for (const change of changes) {
          const row = this.elements.get(change.target);
          if (!row || row.visible === change.isIntersecting) continue;
          row.visible = change.isIntersecting;
          this.interest(row.id, "rows", change.isIntersecting ? 1 : -1);
        }
        this.pump();
      });
  }

  // Reading never creates state, so templates and deriveds may call it.
  get(versionID: string): Facts {
    return this.values[versionID] ?? idle;
  }

  // Effects call retain; untrack keeps the read statuses out of their dependencies.
  retain(versionID: string): () => void {
    untrack(() => {
      this.interest(versionID, "retained", 1);
      this.pump();
    });
    let released = false;
    return () => {
      if (!released) {
        released = true;
        untrack(() => {
          this.interest(versionID, "retained", -1);
          this.pump();
        });
      }
    };
  }

  observeRow(versionID: string | undefined): Attachment<HTMLElement> {
    return (element) => {
      const observer = this.observer;
      if (!versionID || this.stopped || !observer) return;
      const row = { id: versionID, visible: false };
      untrack(() => this.entry(versionID));
      this.elements.set(element, row);
      observer.observe(element);
      return () => {
        observer.unobserve(element);
        this.elements.delete(element);
        untrack(() => {
          if (row.visible) this.interest(versionID, "rows", -1);
          this.pump();
        });
      };
    };
  }

  retry(versionID: string, resource: Resource): void {
    untrack(() => {
      const entry = this.entry(versionID);
      if (entry.facts[resource].status === "error") entry.facts[resource] = { status: "idle" };
      this.pump();
    });
  }

  dispose(): void {
    if (this.stopped) return;
    this.stopped = true;
    this.observer?.disconnect();
    this.elements.clear();
    for (const entry of this.entries.values())
      for (const controller of Object.values(entry.controllers)) controller.abort();
  }

  private entry(id: string): Entry {
    let entry = this.entries.get(id);
    if (!entry) {
      this.values[id] = { dates: { status: "idle" }, people: { status: "idle" } };
      entry = { facts: this.values[id]!, rows: 0, retained: 0, controllers: {} };
      this.entries.set(id, entry);
    }
    return entry;
  }

  private interest(id: string, kind: "rows" | "retained", delta: number): void {
    const entry = this.entry(id);
    entry[kind] += delta;
    if (!entry.rows && !entry.retained) {
      for (const resource of ["dates", "people"] as const) {
        const controller = entry.controllers[resource];
        if (controller) {
          delete entry.controllers[resource];
          controller.abort();
        }
        if (
          entry.facts[resource].status === "loading" ||
          entry.facts[resource].value?.state === "pending"
        )
          entry.facts[resource] = { status: "idle" };
      }
    }
  }

  private pump(): void {
    if (this.stopped) return;
    const candidates = [...this.entries.entries()].filter(([, e]) => e.rows || e.retained);
    candidates.sort((a, b) => Number(b[1].retained > 0) - Number(a[1].retained > 0));
    for (const [id, entry] of candidates)
      for (const resource of ["dates", "people"] as const) {
        if (this.active >= 4) return;
        if (entry.facts[resource].status !== "idle") continue;
        this.start(id, entry, resource);
      }
  }

  private start(id: string, entry: Entry, resource: Resource): void {
    const controller = new AbortController();
    entry.controllers[resource] = controller;
    entry.facts[resource] = { status: "loading" };
    this.active++;
    const options = { session: this.session, signal: controller.signal };
    const request =
      resource === "dates"
        ? getVersionDates(id, undefined, options)
        : getVersionPeople(id, undefined, options);
    void request
      .then(
        (value) => {
          if (this.stopped || entry.controllers[resource] !== controller) return;
          if (resource === "dates")
            entry.facts.dates = { status: "ready", value: value as VersionDates };
          else entry.facts.people = { status: "ready", value: value as VersionPeople };
        },
        (cause: unknown) => {
          if (this.stopped || entry.controllers[resource] !== controller) return;
          if (cause instanceof APIError && cause.status === 401) {
            this.dispose();
            this.onauthfailure(cause);
          } else entry.facts[resource] = { status: "error" };
        },
      )
      .finally(() => {
        if (entry.controllers[resource] === controller) delete entry.controllers[resource];
        this.active--;
        this.pump();
      });
  }
}
