import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";
import { APIError } from "./api-transport.js";
import { DocumentFacts } from "./documentFacts.svelte.js";

const requests = vi.hoisted(
  () =>
    [] as {
      id: string;
      resource: string;
      options: { session: string; signal: AbortSignal };
      resolve: (value: unknown) => void;
      reject: (cause: unknown) => void;
    }[],
);
vi.mock("./generated/docbank.js", () => {
  const read =
    (resource: string) =>
    (id: string, _params: unknown, options: { session: string; signal: AbortSignal }) =>
      new Promise((resolve, reject) => requests.push({ id, resource, options, resolve, reject }));
  return { getVersionDates: read("dates"), getVersionPeople: read("people") };
});
let observer: FakeObserver;
class FakeObserver {
  callback: IntersectionObserverCallback;
  constructor(callback: IntersectionObserverCallback) {
    this.callback = callback;
    observer = this;
  }
  observe() {}
  unobserve() {}
  disconnect() {}
  show(target: Element) {
    this.callback(
      [{ target, isIntersecting: true } as IntersectionObserverEntry],
      this as unknown as IntersectionObserver,
    );
  }
}
const flush = async () => {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
};
beforeEach(() => {
  requests.length = 0;
  vi.stubGlobal("IntersectionObserver", FakeObserver);
});
afterEach(() => vi.unstubAllGlobals());

it("only loads visible versions and bounds HTTP reads, prioritizing the inspector", async () => {
  const facts = new DocumentFacts("session", vi.fn());
  const rows = Array.from({ length: 1000 }, (_, i) => {
    const el = document.createElement("div");
    facts.observeRow(`v${i}`)(el);
    return el;
  });
  for (let i = 0; i < 5; i++) observer.show(rows[i]!);
  const release = facts.retain("selected");
  expect(requests).toHaveLength(4);
  expect(requests.every((r) => r.options.session === "session")).toBe(true);
  requests[0]!.resolve({});
  await flush();
  expect(requests[4]!.id).toBe("selected");
  expect(new Set(requests.map((r) => r.id)).size).toBeLessThanOrEqual(6);
  release();
  facts.dispose();
});

it("shares reads, retries only failed resources and ignores late responses", async () => {
  const failAuth = vi.fn();
  const facts = new DocumentFacts("session", failAuth);
  const element = document.createElement("div");
  facts.observeRow("v")(element);
  observer.show(element);
  facts.retain("v");
  facts.observeRow(undefined)(document.createElement("div"));
  expect(requests).toHaveLength(2);
  requests[0]!.reject(new Error("offline"));
  requests[1]!.resolve({ state: "published" });
  await flush();
  expect(facts.get("v").dates.status).toBe("error");
  expect(facts.get("v").people.status).toBe("ready");
  facts.retry("v", "dates");
  expect(requests).toHaveLength(3);
  facts.dispose();
  expect(requests[2]!.options.signal.aborted).toBe(true);
  requests[2]!.reject(new APIError("expired", 401, ""));
  await flush();
  expect(failAuth).not.toHaveBeenCalled();
});

it("stops all work on the first current 401", async () => {
  const failAuth = vi.fn();
  const facts = new DocumentFacts("session", failAuth);
  facts.retain("v");
  requests[0]!.reject(new APIError("expired", 401, ""));
  await flush();
  expect(failAuth).toHaveBeenCalledOnce();
  expect(requests[1]!.options.signal.aborted).toBe(true);
  facts.retain("next");
  expect(requests).toHaveLength(2);
});

it("rereads pending facts only after the last interest leaves, preserving published facts", async () => {
  const facts = new DocumentFacts("session", vi.fn());
  const first = facts.retain("v");
  const second = facts.retain("v");
  requests[0]!.resolve({ state: "pending" });
  requests[1]!.resolve({ state: "published" });
  await flush();
  first();
  expect(facts.get("v").dates.status).toBe("ready");
  second();
  expect(facts.get("v").dates.status).toBe("idle");
  const release = facts.retain("v");
  expect(requests.map((r) => r.resource)).toEqual(["dates", "people", "dates"]);
  release();
  facts.dispose();
});

it("ignores a canceled read that resolves after renewed interest", async () => {
  const facts = new DocumentFacts("session", vi.fn());
  const release = facts.retain("v");
  release();
  facts.retain("v");
  requests[0]!.resolve({ state: "published", old: true });
  await flush();
  expect(facts.get("v").dates.status).toBe("loading");
  requests[2]!.resolve({ state: "published", old: false });
  await flush();
  expect(facts.get("v").dates.value).toEqual({ state: "published", old: false });
  facts.dispose();
});

it("keeps a published result released before its request finishes cleanup", async () => {
  const facts = new DocumentFacts("session", vi.fn());
  const release = facts.retain("v");
  requests[0]!.resolve({ state: "published" });
  await Promise.resolve();
  release();
  await flush();
  facts.retain("v");
  expect(facts.get("v").dates.value).toEqual({ state: "published" });
  expect(requests.filter((r) => r.resource === "dates")).toHaveLength(1);
  facts.dispose();
});
