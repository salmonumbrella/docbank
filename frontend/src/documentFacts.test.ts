import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";
import { APIError } from "./api-transport.js";
import { DocumentFacts } from "./documentFacts.svelte.js";
import { dates, people } from "./document-facts-test-fixtures.js";

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
let notify: IntersectionObserverCallback;
const observer = {
  show(target: Element) {
    notify(
      [{ target, isIntersecting: true } as IntersectionObserverEntry],
      {} as IntersectionObserver,
    );
  },
};
class FakeObserver {
  constructor(callback: IntersectionObserverCallback) {
    notify = callback;
  }
  observe() {}
  unobserve() {}
  disconnect() {}
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
  for (let i = 500; i < 505; i++) observer.show(rows[i]!);
  const release = facts.retain("selected");
  expect(requests).toHaveLength(4);
  expect(requests.every((r) => r.options.session === "session")).toBe(true);
  requests[0]!.resolve(dates([]));
  await flush();
  expect(requests[4]!.id).toBe("selected");
  for (let i = 1; i < 12; i++) {
    requests[i]!.resolve(requests[i]!.resource === "dates" ? dates([]) : people());
    await flush();
  }
  expect(new Set(requests.map((r) => r.id))).toEqual(
    new Set(["v500", "v501", "v502", "v503", "v504", "selected"]),
  );
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
  const noVersion = document.createElement("div");
  facts.observeRow(undefined)(noVersion);
  observer.show(noVersion);
  expect(requests).toHaveLength(2);
  requests[0]!.reject(new Error("offline"));
  requests[1]!.resolve(people());
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

it("keeps facts correct as the last interest leaves and returns", async () => {
  {
    const facts = new DocumentFacts("session", vi.fn());
    const first = facts.retain("v");
    const second = facts.retain("v");
    requests[0]!.resolve(dates([], "pending"));
    requests[1]!.resolve(people());
    await flush();
    first();
    expect(facts.get("v").dates.status).toBe("ready");
    second();
    expect(facts.get("v").dates.status).toBe("idle");
    const release = facts.retain("v");
    expect(requests.map((r) => r.resource)).toEqual(["dates", "people", "dates"]);
    release();
    facts.dispose();
  }
  requests.length = 0;
  {
    const facts = new DocumentFacts("session", vi.fn());
    const release = facts.retain("v");
    release();
    facts.retain("v");
    requests[0]!.resolve({ ...dates([]), content_version_id: "old" });
    await flush();
    expect(facts.get("v").dates.status).toBe("loading");
    requests[2]!.resolve({ ...dates([]), content_version_id: "v" });
    await flush();
    expect(facts.get("v").dates.value?.content_version_id).toBe("v");
    facts.dispose();
  }
  requests.length = 0;
  {
    const facts = new DocumentFacts("session", vi.fn());
    const release = facts.retain("v");
    requests[0]!.resolve(dates([]));
    await Promise.resolve();
    release();
    await flush();
    facts.retain("v");
    expect(facts.get("v").dates.value).toEqual(dates([]));
    expect(requests.filter((r) => r.resource === "dates")).toHaveLength(1);
    facts.dispose();
  }
});

it("cleans inactive scheduling state and retries errors when interest returns", async () => {
  const facts = new DocumentFacts("session", vi.fn());
  const internals = facts as unknown as { entries: Map<string, unknown>; values: Record<string, unknown> };
  const element = document.createElement("div");
  const detach = facts.observeRow("never-visible")(element);
  expect(internals.entries.size).toBe(0);
  expect(Object.keys(internals.values)).toEqual([]);
  if (typeof detach === "function") detach();

  const release = facts.retain("v");
  requests[0]!.reject(new Error("offline"));
  requests[1]!.resolve(people());
  await flush();
  release();
  expect(internals.entries.size).toBe(0);
  expect(facts.get("v").people.value).toEqual(people());
  facts.retain("v");
  expect(requests.map((r) => r.resource)).toEqual(["dates", "people", "dates"]);
  requests[2]!.resolve(dates([]));
  await flush();
  expect(facts.get("v").dates.value).toEqual(dates([]));
  facts.dispose();
});
