import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import ContractForm from "./ContractForm.svelte";
import type { Contract, Party } from "./generated/docbank.js";
import { contract, contractID, party } from "./contract-test-fixtures.js";

interface Call {
  method: string;
  path: string;
  body?: unknown;
  ifMatch: string | null;
}
type Route = (call: Call, url: URL) => Response | Promise<Response> | undefined;
let calls: Call[] = [];

const own = party({
  party_id: "44444444-4444-4444-8444-444444444444",
  display_name: "Example Holdings Inc",
  own: true,
});
const northwind = party({
  party_id: "55555555-5555-4555-8555-555555555555",
  display_name: "Northwind Storage Ltd",
});
const zenith = party();
const contractPath = `/api/v1/contracts/${contractID}`;

function serve(route: Route) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = new URL(String(input), "http://localhost");
    const call: Call = {
      method: init?.method ?? "GET",
      path: url.pathname + url.search,
      ifMatch: new Headers(init?.headers).get("If-Match"),
      ...(typeof init?.body === "string" ? { body: JSON.parse(init.body) } : {}),
    };
    calls.push(call);
    const response = await route(call, url);
    if (response) return response;
    throw new Error(`unexpected ${call.method} ${call.path}`);
  });
}
const problem = (status: number, code: string, extra: object = {}) =>
  Response.json({ status, code, detail: code, ...extra }, { status });
// catalog serves the active parties in pages of the requested size; it
// answers only the list, so a party create reaches the test's own route.
function catalog(call: Call, rows: Party[]): Response | undefined {
  const url = new URL(call.path, "http://localhost");
  if (call.method !== "GET" || url.pathname !== "/api/v1/parties") return undefined;
  const limit = Number(url.searchParams.get("limit"));
  const offset = Number(url.searchParams.get("offset"));
  return Response.json({ items: rows.slice(offset, offset + limit), limit, offset });
}
const many = (count: number) =>
  Array.from({ length: count }, (_, i) =>
    party({
      party_id: `aaaaaaaa-${String(i).padStart(4, "0")}-4000-8000-000000000000`,
      display_name: `Archive party ${String(i).padStart(4, "0")}`,
    }),
  );
const sent = (method: string, prefix: string) =>
  calls.filter((call) => call.method === method && call.path.startsWith(prefix));

function show(existing?: Contract) {
  const props = { onsaved: vi.fn(), oncancel: vi.fn(), onauthfailure: vi.fn() };
  render(ContractForm, { props: { session: "synthetic", contract: existing, ...props } });
  return props;
}
async function search(block: "own entity" | "counterparty", query: string) {
  await fireEvent.click(
    await screen.findByRole("button", { name: `Find or create a party: Add ${block}` }),
  );
  await fireEvent.input(screen.getByRole("combobox", { name: "Find or create a party" }), {
    target: { value: query },
  });
}
async function add(block: "own entity" | "counterparty", name: string) {
  await search(block, name);
  await fireEvent.mouseDown(await screen.findByRole("option", { name }));
}
async function createInline(block: "own entity" | "counterparty", name: string) {
  await search(block, name);
  await fireEvent.mouseDown(await screen.findByRole("option", { name: `Create party "${name}"` }));
  await fireEvent.click(screen.getByRole("button", { name: "Create party" }));
}
const titleInput = () => screen.getByLabelText("Title") as HTMLInputElement;

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  Object.defineProperty(Element.prototype, "scrollIntoView", {
    configurable: true,
    value: vi.fn(),
  });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("new contract", () => {
  it("creates the contract and its parties in one request without empty fields", async () => {
    const created = contract({ title: "Archive storage", status: "draft", revision: 1 });
    serve(
      (call, url) =>
        catalog(call, [own, northwind]) ??
        (call.method === "POST" && url.pathname === "/api/v1/contracts"
          ? Response.json(created, { status: 201 })
          : undefined),
    );
    const props = show();
    await fireEvent.input(titleInput(), { target: { value: "Archive storage" } });
    await add("own entity", "Example Holdings Inc");
    await add("counterparty", "Northwind Storage Ltd");
    await fireEvent.click(screen.getByRole("button", { name: "Create contract" }));
    await waitFor(() => expect(props.onsaved).toHaveBeenCalledWith(created));
    expect(sent("POST", "/api/v1/contracts")).toStrictEqual([
      {
        method: "POST",
        path: "/api/v1/contracts",
        ifMatch: null,
        body: {
          title: "Archive storage",
          status: "draft",
          parties: [
            { party_id: own.party_id, role: "own" },
            { party_id: northwind.party_id, role: "counterparty" },
          ],
        },
      },
    ]);
  });

  it("creates an own entity in place", async () => {
    const created = party({ party_id: "88888888-8888-4888-8888-888888888888", own: true });
    serve(
      (call, url) =>
        catalog(call, [northwind]) ??
        (call.method === "POST" && url.pathname === "/api/v1/parties"
          ? Response.json({ ...created, display_name: "Harbor Holdings" }, { status: 201 })
          : undefined),
    );
    show();
    await createInline("own entity", "Harbor Holdings");
    expect(sent("POST", "/api/v1/parties")[0]?.body).toStrictEqual({
      kind: "organization",
      display_name: "Harbor Holdings",
      own: true,
    });
    expect(await screen.findByRole("button", { name: "Remove Harbor Holdings" })).toBeTruthy();
  });

  it("loads every party page before offering to create one", async () => {
    const zeta = party({
      party_id: "99999999-9999-4999-8999-999999999999",
      display_name: "Zeta Holdings",
      own: true,
    });
    serve((call) => catalog(call, [...many(1148), zeta, zenith]));
    show();
    await search("counterparty", "Zenith");
    await fireEvent.mouseDown(await screen.findByRole("option", { name: "Zenith Storage Ltd" }));
    expect(sent("GET", "/api/v1/parties").map((call) => call.path)).toStrictEqual([
      "/api/v1/parties?limit=1000&offset=0",
      "/api/v1/parties?limit=1000&offset=1000",
    ]);
    expect(await screen.findByRole("button", { name: "Remove Zenith Storage Ltd" })).toBeTruthy();
    await search("own entity", "Z");
    expect(await screen.findByRole("option", { name: "Zeta Holdings" })).toBeTruthy();
    expect(screen.queryByRole("option", { name: "Zenith Storage Ltd" })).toBeNull();
  });

  it("reports a partial party list instead of offering to create a party", async () => {
    let failSecond = true;
    serve((call, url) => {
      if (url.searchParams.get("offset") === "1000" && failSecond) {
        failSecond = false;
        return problem(500, "internal");
      }
      return catalog(call, [...many(1000), zenith]);
    });
    show();
    await search("counterparty", "Zenith");
    expect(
      (await screen.findByText("Only 1000 parties loaded. Retry to search the rest.")).textContent,
    ).toBeTruthy();
    expect(screen.queryByRole("option", { name: 'Create party "Zenith"' })).toBeNull();
    await fireEvent.mouseDown(screen.getByRole("option", { name: "Retry" }));
    expect(await screen.findByRole("option", { name: "Zenith Storage Ltd" })).toBeTruthy();
    expect(sent("POST", "/api/v1/parties")).toHaveLength(0);
  });

  it("chooses the existing party when the name is taken", async () => {
    serve(
      (call) =>
        catalog(call, [northwind]) ??
        (call.method === "POST"
          ? problem(409, "party_exists", { party_id: northwind.party_id })
          : undefined),
    );
    show();
    await createInline("counterparty", "Northwind Storage");
    expect(
      await screen.findByText("Northwind Storage Ltd already exists, so it was chosen."),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove Northwind Storage Ltd" })).toBeTruthy();
    expect(sent("POST", "/api/v1/parties")).toHaveLength(1);
    expect(sent("GET", "/api/v1/parties")).toHaveLength(1);
  });

  it("reloads the parties once to find an existing party created meanwhile", async () => {
    let rows = [northwind];
    serve((call) => {
      if (call.method === "POST") {
        rows = [northwind, zenith];
        return problem(409, "party_exists", { party_id: zenith.party_id });
      }
      return catalog(call, rows);
    });
    show();
    await createInline("counterparty", "Zenith");
    expect(await screen.findByRole("button", { name: "Remove Zenith Storage Ltd" })).toBeTruthy();
    expect(sent("GET", "/api/v1/parties")).toHaveLength(2);
    expect(sent("POST", "/api/v1/parties")).toHaveLength(1);
  });

  it("says when the existing party is no longer available", async () => {
    serve(
      (call) =>
        catalog(call, [northwind]) ??
        (call.method === "POST"
          ? problem(409, "party_exists", { party_id: zenith.party_id })
          : undefined),
    );
    show();
    await createInline("counterparty", "Zenith");
    expect(
      await screen.findByText("That party is no longer available. Retry parties."),
    ).toBeTruthy();
    expect(sent("GET", "/api/v1/parties")).toHaveLength(2);
    expect(sent("POST", "/api/v1/parties")).toHaveLength(1);
  });
});

describe("edit contract", () => {
  const saved = (overrides: Partial<Contract>) => Response.json(contract(overrides));

  it("disables Save until something changes", async () => {
    serve((call) => catalog(call, [northwind]));
    show(contract());
    const save = screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    await fireEvent.input(titleInput(), { target: { value: "Northwind lease" } });
    expect(save.disabled).toBe(false);
  });

  it("patches only the changed fields at the contract revision", async () => {
    serve(
      (call) =>
        catalog(call, [northwind]) ??
        (call.method === "PATCH"
          ? saved({ title: "Northwind lease", expires_on: null, revision: 5 })
          : undefined),
    );
    const props = show(contract());
    await fireEvent.input(titleInput(), { target: { value: "Northwind lease" } });
    await fireEvent.click(screen.getByRole("button", { name: "Clear expiry date" }));
    await fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(props.onsaved).toHaveBeenCalledOnce());
    expect(sent("PATCH", contractPath)).toStrictEqual([
      {
        method: "PATCH",
        path: contractPath,
        ifMatch: '"4"',
        body: { title: "Northwind lease", expires_on: null },
      },
    ]);
  });

  it("chains each party step on the revision the previous step returned", async () => {
    serve((call) => {
      if (call.method === "PATCH") return saved({ title: "Northwind lease", revision: 5 });
      if (call.method === "DELETE") return saved({ title: "Northwind lease", revision: 6 });
      if (call.method === "PUT") return saved({ title: "Northwind lease", revision: 7 });
      return catalog(call, [northwind, zenith]);
    });
    const props = show(contract());
    await fireEvent.input(titleInput(), { target: { value: "Northwind lease" } });
    await fireEvent.click(screen.getByRole("button", { name: "Remove Riverbend Analytics LLC" }));
    await add("counterparty", "Zenith Storage Ltd");
    await fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(props.onsaved).toHaveBeenCalledOnce());
    expect(calls.filter((call) => call.method !== "GET")).toStrictEqual([
      expect.objectContaining({ method: "PATCH", ifMatch: '"4"' }),
      {
        method: "DELETE",
        path: `${contractPath}/parties/66666666-6666-4666-8666-666666666666?role=counterparty`,
        ifMatch: '"5"',
      },
      {
        method: "PUT",
        path: `${contractPath}/parties/${zenith.party_id}?role=counterparty`,
        ifMatch: '"6"',
      },
    ]);
  });

  it("keeps a retired party listed and removable but never offers it", async () => {
    serve((call) => catalog(call, [northwind]));
    show(contract());
    expect(screen.getByRole("button", { name: "Remove Riverbend Analytics LLC" })).toBeTruthy();
    expect(screen.getAllByText("Retired")).toHaveLength(1);
    await search("counterparty", "Riverbend");
    expect(screen.queryByRole("option", { name: "Riverbend Analytics LLC" })).toBeNull();
  });

  it("stops on a stale first step, keeps the draft and loads the latest on request", async () => {
    serve((call) => {
      if (call.method === "PATCH") return problem(412, "stale_revision");
      if (call.path === contractPath) return saved({ title: "Renamed elsewhere", revision: 6 });
      return catalog(call, [northwind]);
    });
    show(contract());
    await fireEvent.input(titleInput(), { target: { value: "Northwind lease" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(
      await screen.findByText(
        "This contract changed. It was saved elsewhere after you opened it; nothing was saved.",
      ),
    ).toBeTruthy();
    expect(titleInput().value).toBe("Northwind lease");
    await fireEvent.click(screen.getByRole("button", { name: "Load latest" }));
    await waitFor(() => expect(titleInput().value).toBe("Renamed elsewhere"));
  });

  it("reports what was saved when a later step is stale", async () => {
    serve((call) => {
      if (call.method === "PATCH") return saved({ title: "Northwind lease", revision: 5 });
      if (call.method === "PUT") return problem(412, "stale_revision");
      return catalog(call, [northwind, zenith]);
    });
    show(contract());
    await fireEvent.input(titleInput(), { target: { value: "Northwind lease" } });
    await add("counterparty", "Zenith Storage Ltd");
    await fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Saved: details. Not saved: add Zenith Storage Ltd");
    expect(document.body.textContent).not.toContain("nothing was saved");
    expect(screen.getByRole("button", { name: "Remove Zenith Storage Ltd" })).toBeTruthy();
  });

  it("locks Save when Docbank does not confirm a step and never resends it", async () => {
    serve((call) => {
      if (call.method === "PATCH") throw new TypeError("Failed to fetch");
      return catalog(call, [northwind]);
    });
    show(contract());
    await fireEvent.input(titleInput(), { target: { value: "Northwind lease" } });
    const save = screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement;
    await fireEvent.click(save);
    expect((await screen.findByRole("alert")).textContent).toBe(
      "Docbank didn't confirm details. Reload the contract before saving again.",
    );
    expect(save.disabled).toBe(true);
    await fireEvent.click(save);
    expect(sent("PATCH", contractPath)).toHaveLength(1);
  });
});
