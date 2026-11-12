import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { tick } from "svelte";
import DocumentInspector from "./DocumentInspector.svelte";
import InspectorContracts from "./InspectorContracts.svelte";
import type { Contract } from "./generated/docbank.js";
import { selectedSourceFromNode, type SelectedSource } from "./selectedSource.js";
import {
  contract,
  contractID,
  contractLink,
  draftVersionID,
  nodeFixture,
  signedVersionID,
} from "./contract-test-fixtures.js";

interface Call {
  method: string;
  path: string;
  ifMatch: string | null;
}
let calls: Call[] = [];
const node = nodeFixture();
const nodeList = "/api/v1/nodes/412/contracts?limit=100&offset=0";
// The inspector shows the older draft version, not the node's current head.
const older: SelectedSource = {
  ...selectedSourceFromNode(node, node.path ?? ""),
  key: "older",
  versionID: draftVersionID,
};
const page = (items: Contract[]) => Response.json({ items, as_of: "2027-01-01", limit: 100, offset: 0 });
const problem = (status: number, code: string) =>
  Response.json({ status, code, detail: code }, { status });

function serve(route: (call: Call, url: URL) => Response | Promise<Response> | undefined) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = new URL(String(input), "http://localhost");
    const call = {
      method: init?.method ?? "GET",
      path: url.pathname + url.search,
      ifMatch: new Headers(init?.headers).get("If-Match"),
    };
    calls.push(call);
    const response = await route(call, url);
    if (response) return response;
    throw new Error(`unexpected ${call.method} ${call.path}`);
  });
}
function show(props: Partial<{ source: SelectedSource; actionsDisabled: boolean }> = {}) {
  const onauthfailure = vi.fn();
  const view = render(InspectorContracts, {
    props: { session: "synthetic", node, source: older, onauthfailure, ...props },
  });
  return { onauthfailure, view };
}
const linkButton = () =>
  screen.getByRole("button", { name: "Link to contract" }) as HTMLButtonElement;
async function chooseContract(title: string) {
  await fireEvent.click(linkButton());
  await fireEvent.click(screen.getByRole("button", { name: "Find a contract" }));
  await fireEvent.input(screen.getByRole("combobox", { name: "Find a contract" }), {
    target: { value: "north" },
  });
  await fireEvent.mouseDown(await screen.findByRole("option", { name: title }));
}
async function chooseRole(role: string) {
  await fireEvent.click(screen.getByRole("combobox", { name: /^Role/ }));
  await fireEvent.click(await screen.findByRole("option", { name: role }));
}

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

it("lists each link of this file with its version state", async () => {
  const linked = contract({
    documents: [
      contractLink(),
      contractLink({ content_version_id: draftVersionID, role: "draft", current: false }),
      contractLink({ node_id: 900, role: "attachment" }),
      contractLink({ role: "amendment", available: false }),
    ],
  });
  serve((call) => (call.path === nodeList ? page([linked]) : undefined));
  show();
  const item = await screen.findByRole("listitem", { name: "Northwind Storage - unit lease" });
  expect(within(item).getByText("Active")).toBeTruthy();
  expect(within(item).getByText("Signed")).toBeTruthy();
  expect(within(item).getByText("Older version")).toBeTruthy();
  expect(within(item).getByText("Unavailable")).toBeTruthy();
  expect(within(item).queryByText("Attachment")).toBeNull();
  expect(calls.map((call) => call.path)).toStrictEqual([nodeList]);
});

it("links the version the inspector shows with the contract revision", async () => {
  let linked = false;
  const found = contract({ revision: 9 });
  serve((call, url) => {
    if (call.path === nodeList)
      return page(
        linked
          ? [{ ...found, documents: [contractLink({ content_version_id: draftVersionID })] }]
          : [],
      );
    if (call.method === "GET" && url.pathname === "/api/v1/contracts") return page([found]);
    if (call.method === "PUT") {
      linked = true;
      return Response.json(found);
    }
    return undefined;
  });
  show();
  expect(await screen.findByText("Not linked to a contract")).toBeTruthy();
  await chooseContract("Northwind Storage - unit lease · Active");
  const link = screen.getByRole("button", { name: "Link" }) as HTMLButtonElement;
  expect(link.disabled).toBe(true);
  await chooseRole("Signed");
  expect(link.disabled).toBe(false);
  await fireEvent.click(link);
  expect(
    await screen.findByText("Linked as Signed to Northwind Storage - unit lease."),
  ).toBeTruthy();
  expect(calls).toContainEqual({
    method: "PUT",
    path: `/api/v1/contracts/${contractID}/documents/412?role=signed&content_version_id=${draftVersionID}`,
    ifMatch: '"9"',
  });
  expect(calls).toContainEqual({
    method: "GET",
    path: "/api/v1/contracts?q=north&limit=20",
    ifMatch: null,
  });
  expect(await screen.findByRole("listitem", { name: "Northwind Storage - unit lease" })).toBeTruthy();
});

it("reloads and asks to retry when the contract changed before the link", async () => {
  serve((call, url) => {
    if (call.path === nodeList) return page([]);
    if (url.pathname === "/api/v1/contracts") return page([contract()]);
    if (call.method === "PUT") return problem(412, "stale_revision");
    return undefined;
  });
  show();
  await chooseContract("Northwind Storage - unit lease · Active");
  await chooseRole("Attachment");
  await fireEvent.click(screen.getByRole("button", { name: "Link" }));
  expect(await screen.findByText("That contract changed. Try again.")).toBeTruthy();
  expect(calls.filter((call) => call.path === nodeList)).toHaveLength(2);
});

it("unlinks only the chosen version of a role", async () => {
  const both = contract({
    documents: [
      contractLink(),
      contractLink({ content_version_id: draftVersionID, current: false }),
    ],
  });
  serve((call) => {
    if (call.path === nodeList) return page([both]);
    if (call.method === "DELETE") return Response.json(both);
    return undefined;
  });
  show();
  const item = await screen.findByRole("listitem", { name: "Northwind Storage - unit lease" });
  const rows = within(item).getAllByRole("button", { name: "Unlink" });
  await fireEvent.click(rows[1]!);
  await fireEvent.click(within(item).getByRole("button", { name: "Confirm unlink" }));
  await waitFor(() =>
    expect(calls).toContainEqual({
      method: "DELETE",
      path: `/api/v1/contracts/${contractID}/documents/412?role=signed&content_version_id=${draftVersionID}`,
      ifMatch: '"4"',
    }),
  );
  expect(calls.some((call) => call.path.includes(signedVersionID))).toBe(false);
});

it("disables linking without an exact source or while the view is stale", async () => {
  serve((call) => (call.path === nodeList ? page([]) : undefined));
  const { view } = show({ source: { ...older, nodeID: 7 } });
  await screen.findByText("Not linked to a contract");
  expect(linkButton().disabled).toBe(true);
  await view.rerender({ source: older, actionsDisabled: true });
  expect(linkButton().disabled).toBe(true);
  await view.rerender({ source: older, actionsDisabled: false });
  expect(linkButton().disabled).toBe(false);
});

it("ignores a late reply for an earlier source", async () => {
  let finish: (response: Response) => void = () => {};
  let first = true;
  serve((call) => {
    if (call.path !== nodeList) return undefined;
    if (!first) return page([]);
    first = false;
    return new Promise<Response>((resolve) => (finish = resolve));
  });
  const { view } = show();
  await view.rerender({ source: { ...older, key: "newer", versionID: signedVersionID } });
  expect(await screen.findByText("Not linked to a contract")).toBeTruthy();
  finish(page([contract({ documents: [contractLink()] })]));
  await tick();
  await Promise.resolve();
  expect(screen.queryByRole("listitem", { name: "Northwind Storage - unit lease" })).toBeNull();
});

it("hands a refused session to the app", async () => {
  serve(() => problem(401, "unauthorized"));
  const { onauthfailure } = show();
  await waitFor(() => expect(onauthfailure).toHaveBeenCalledOnce());
  expect(screen.queryByText("Contracts couldn't be loaded.")).toBeNull();
});

it("asks nothing about contracts for a folder", async () => {
  const fetch = serve(() => undefined);
  render(DocumentInspector, {
    node: { ...node, kind: "dir" as const },
    path: "/agreements",
    tags: [],
    tagsLoading: false,
    tagsError: "",
    session: "synthetic",
    onauthfailure: vi.fn(),
    onprovenance: vi.fn(),
    onhistory: vi.fn(),
  });
  await tick();
  expect(screen.queryByRole("heading", { name: "Contracts" })).toBeNull();
  expect(fetch.mock.calls.some(([input]) => String(input).includes("/contracts"))).toBe(false);
});
