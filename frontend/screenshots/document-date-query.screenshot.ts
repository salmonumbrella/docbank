import { expect, test, type Locator, type Response } from "@playwright/test";
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { access, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { inspect, promisify } from "node:util";
import { fileURLToPath } from "node:url";
import {
  getCreateSavedQueryUrl,
  getCreateWorkspaceQueryUrl,
  getListSavedQueriesUrl,
  getParseQueryUrl,
  getUpdateSavedQueryUrl,
  type QueryPreview,
} from "../src/generated/docbank.js";
import type { SnapshotPage } from "../src/snapshots.js";
import { captureTheme } from "./capture-themes.js";

const exec = promisify(execFile);
const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const captures = process.env.DOCBANK_DATE_QUERY_CAPTURE_DIR;
const tree = process.env.DOCBANK_DATE_QUERY_TREE;
test.skip(!captures, "DOCBANK_DATE_QUERY_CAPTURE_DIR enables this proof capture");

const desktop = { width: 1440, height: 960 };
const narrow = { width: 390, height: 844 };
const reversed = "date:2026-03-31..2026-03-01";
const day = "date:2026-03-10";
const savedName = "Synthetic March 10";
const sortLabels = ["Name", "Path", "Modified", "Document date", "Size", "File type"];
type Sort = { field: string; direction: "asc" | "desc" };
type Views = readonly (readonly [string, { width: number; height: number }, readonly string[]])[];
// The original nine images: light and dark at desktop, narrow in dark only.
const originalViews: Views = [
  ["desktop", desktop, ["light", "dark"]],
  ["narrow", narrow, ["dark"]],
];
// The sort and saved-search states add more than four images, so dark only.
const darkViews: Views = [
  ["desktop", desktop, ["dark"]],
  ["narrow", narrow, ["dark"]],
];
const email = (date: string, subject: string) =>
  Buffer.from(
    [
      "From: Synthetic Sender <sender@example.test>",
      "To: Synthetic Recipient <recipient@example.test>",
      `Subject: ${subject}`,
      `Date: ${date}`,
      `Message-ID: <${subject.toLowerCase().replaceAll(" ", "-")}@example.test>`,
      "MIME-Version: 1.0",
      "Content-Type: text/plain; charset=utf-8",
      "",
      `Synthetic ${subject.toLowerCase()} body.`,
      "",
    ].join("\r\n"),
  );
// Same synthetic bytes for the baseline and branch. Instants are compared in
// the effective zone (UTC by default), not by their written local dates.
const fixtures = [
  {
    name: "evening-offset.eml",
    bytes: email("Mon, 09 Mar 2026 23:30:00 -0400", "Evening offset"),
    instant: "2026-03-10T03:30:00",
    matches: true,
  },
  {
    name: "morning-utc.eml",
    bytes: email("Tue, 10 Mar 2026 09:15:00 +0000", "Morning UTC"),
    instant: "2026-03-10T09:15:00",
    matches: true,
  },
  {
    name: "east-offset.eml",
    bytes: email("Tue, 10 Mar 2026 08:00:00 +1000", "East offset"),
    instant: "2026-03-09T22:00:00",
    matches: false,
  },
  {
    name: "april.eml",
    bytes: email("Wed, 01 Apr 2026 10:00:00 +0000", "April"),
    instant: "2026-04-01T10:00:00",
    matches: false,
  },
  { name: "undated.txt", bytes: Buffer.from("Synthetic undated note.\n"), matches: false },
] as const;

interface DateClaim {
  primary: boolean;
  date_kind: string;
  date_value: string;
  timezone_kind: string;
  utc_key?: string;
}

interface FixtureNode {
  name: string;
  node_id: number;
  version_id: string;
  sha256: string;
  mime_type: string;
  matches_branch_query: boolean;
  primary: DateClaim | null;
}

test("captures document-date query validation and results", async ({ page }) => {
  test.setTimeout(240_000);
  const branch = tree === "branch";
  if (!branch && tree !== "baseline") throw new Error("DOCBANK_DATE_QUERY_TREE is invalid");
  const binary = process.env.DOCBANK_SCREENSHOT_BINARY;
  if (!binary || !path.isAbsolute(binary)) {
    throw new Error("DOCBANK_SCREENSHOT_BINARY must name an absolute binary");
  }
  await access(binary);
  const scratch = await mkdtemp(path.join(tmpdir(), "docbank-date-query-"));
  const vault = path.join(scratch, "vault");
  const run = async (...args: string[]) =>
    (
      await exec(binary, args, {
        cwd: repository,
        env: { ...process.env, DOCBANK_HOME: vault },
        timeout: 90_000,
      })
    ).stdout.trim();
  const dates = async (name: string) =>
    JSON.parse(await run("document", "dates", `/${name}`, "--json"));
  const sha256 = (bytes: Buffer) => createHash("sha256").update(bytes).digest("hex");
  const evidence: Record<string, unknown> & {
    captures: unknown[];
    states: unknown[];
    sort: Record<string, unknown>;
    saved: Record<string, unknown>;
  } = {
    tree,
    captures: [],
    states: [],
    sort: {},
    saved: {},
  };
  let session = "";
  let started = false;
  let stopped = false;
  let failure: unknown;
  const stop = async () => {
    if (!started || stopped) return;
    const before = JSON.parse(await run("daemon", "status", "--json"));
    if (before.running) await run("daemon", "stop");
    const after = JSON.parse(await run("daemon", "status", "--json"));
    if (after.running !== false) {
      throw new Error(`Synthetic daemon ${before.pid} is still running`);
    }
    stopped = true;
    evidence.cleanup = { daemon_pid: before.pid ?? null, daemon_running_after_stop: false };
  };
  const editor = page.getByRole("dialog", { name: "Filters and sort" });
  const expression = editor.getByLabel("Query syntax", { exact: true });
  const checking = editor.getByRole("status", { name: "Checking query" });
  const focusError = editor.getByRole("button", { name: "Focus query error" });
  const sortBy = editor.getByRole("combobox", { name: /^Sort by: / });
  const direction = editor.getByRole("combobox", { name: /^Direction: / });
  const listbox = editor.getByRole("listbox");
  const drawer = page.getByRole("dialog", { name: "Searches" });
  const definitionEditor = drawer.getByRole("region", { name: "Definition editor" });
  const queryJSON = drawer.getByRole("textbox", { name: "Complete query JSON" });
  const results = page.getByRole("region", { name: "Frozen query results" });
  const rows = page.locator("table tr[data-snapshot-node]");
  const visibleBox = async (locator: Locator) =>
    (await locator.count()) > 0 && (await locator.first().isVisible())
      ? await locator.first().boundingBox()
      : null;
  const measure = async () => {
    const field = await visibleBox(expression);
    const alerts = (await editor.isVisible())
      ? await Promise.all((await editor.getByRole("alert").all()).map((a) => a.boundingBox()))
      : [];
    const cells = await Promise.all(
      (await rows.locator("td.name-cell").all()).map((cell) => cell.boundingBox()),
    );
    const editorOpen = await editor.isVisible();
    const metrics = await page.evaluate(() => ({
      inner_width: innerWidth,
      inner_height: innerHeight,
      device_pixel_ratio: devicePixelRatio,
      scroll_width: document.documentElement.scrollWidth,
      dark: document.documentElement.classList.contains("dark"),
    }));
    return {
      ...metrics,
      horizontal_overflow: metrics.scroll_width > metrics.inner_width,
      dialog: await visibleBox(editor),
      expression: field,
      alerts,
      alert_left_offsets: alerts.map((box) => (box && field ? box.x - field.x : null)),
      alert_gaps_below_expression: alerts.map((box) =>
        box && field ? box.y - (field.y + field.height) : null,
      ),
      results: await visibleBox(results),
      row_name_cells: cells,
      row_left_edges: [...new Set(cells.map((box) => box?.x ?? null))],
      listbox: await visibleBox(listbox),
      drawer: await visibleBox(drawer),
      sort_label: editorOpen ? await sortBy.getAttribute("aria-label") : null,
      direction_label: editorOpen ? await direction.getAttribute("aria-label") : null,
    };
  };
  const capture = async (state: string, views = originalViews, ready?: () => Promise<void>) => {
    for (const [viewport, size, themes] of views) {
      await page.setViewportSize(size);
      for (const theme of themes as readonly ("light" | "dark")[]) {
        // Search replaces the open dialog's query, which revalidates it; every
        // image and its measurements show the settled validation state.
        if (await editor.isVisible()) await expect(checking).toHaveCount(0);
        await ready?.();
        await captureTheme(page, captures!, `${state}-${viewport}`, theme);
        evidence.captures.push({
          file: `${state}-${viewport}-${theme}.png`,
          state,
          viewport,
          theme,
          size,
          measurements: await measure(),
        });
      }
    }
    await page.setViewportSize(desktop);
  };
  const posted = (route: string, text: string, sort?: Sort) =>
    page.waitForResponse((response) => {
      if (new URL(response.url()).pathname !== route || response.request().method() !== "POST")
        return false;
      const body = response.request().postDataJSON();
      const sent = route === getParseQueryUrl() ? body : body?.query;
      return (
        sent?.text === text &&
        (sort === undefined ||
          (sent.sort?.field === sort.field && sent.sort?.direction === sort.direction))
      );
    });
  const record = async (state: string, response: Response) => {
    const body = await response.json();
    const result = { state, status: response.status(), body };
    evidence.states.push(result);
    return result;
  };
  try {
    await mkdir(captures!, { recursive: true, mode: 0o700 });
    await mkdir(vault, { mode: 0o700 });
    const sources: string[] = [];
    for (const fixture of fixtures) {
      const source = path.join(scratch, fixture.name);
      await writeFile(source, fixture.bytes, { mode: 0o600 });
      sources.push(source);
    }
    started = true; // add may start the daemon even if the command fails.
    await run("add", ...sources, "--dest", "/");
    const nodes: FixtureNode[] = [];
    const reads: Record<string, unknown> = {};
    evidence.dates_reads = reads;
    for (const fixture of fixtures) {
      const node = JSON.parse(await run("stat", `/${fixture.name}`, "--json"));
      expect(node.blob_hash).toBe(sha256(fixture.bytes));
      if (fixture.name.endsWith(".eml")) expect(node.mime_type).toBe("message/rfc822");
      // A later metadata publication re-derives a published head, so readiness
      // and the asserted primary come from one response; the last is retained.
      const utcKey = "instant" in fixture ? `${fixture.instant}.000000000Z` : undefined;
      let read: { state: string; claims: DateClaim[] } = { state: "", claims: [] };
      await expect
        .poll(
          async () => {
            read = await dates(fixture.name);
            reads[fixture.name] = read;
            const primary = read.claims.find((claim) => claim.primary);
            return (
              read.state === "published" &&
              primary !== undefined &&
              (utcKey === undefined || (primary.date_kind === "sent" && primary.utc_key === utcKey))
            );
          },
          { timeout: 90_000 },
        )
        .toBe(true);
      const primary = read.claims.find((claim) => claim.primary);
      if (utcKey !== undefined) {
        expect(primary?.date_kind).toBe("sent");
        expect(primary?.utc_key).toBe(utcKey);
      }
      nodes.push({
        name: fixture.name,
        node_id: node.id,
        version_id: node.current_version_id,
        sha256: node.blob_hash,
        mime_type: node.mime_type,
        matches_branch_query: fixture.matches,
        primary: primary ?? null,
      });
    }
    evidence.fixtures = nodes;
    const expected = nodes.filter((node) => node.matches_branch_query);

    const issued = new URL(await run("web", "--no-browser"));
    session = new URLSearchParams(issued.hash.slice(1)).get("web_session") ?? "";
    if (!session) throw new Error("Synthetic web session missing");
    try {
      await page.goto(issued.toString());
    } catch (cause) {
      const detail = String(cause).replaceAll(session, "[redacted]");
      throw new Error("Could not open the synthetic browser session", { cause: new Error(detail) });
    }
    expect(await page.evaluate(() => devicePixelRatio)).toBe(2);
    // The existing Query syntax flow from the Filters and sort dialog.
    await page.getByRole("button", { name: "Imports", exact: true }).click();
    const imports = page.getByRole("main", { name: "Imports" });
    await imports
      .getByRole("button", { name: /Browse import/ })
      .first()
      .click();
    await imports.getByRole("button", { name: "Search this import" }).click();
    await expect(editor.getByRole("status", { name: "Query valid" })).toBeAttached();
    await page.keyboard.press("Escape");
    await expect(editor).toBeHidden();
    await page.getByRole("combobox", { name: /^Search syntax/ }).click();
    await page.getByRole("option", { name: "Query syntax", exact: true }).click();
    await page.getByRole("button", { name: "Advanced", exact: true }).click();
    await expect(expression).toBeVisible();

    await test.step("reversed date range", async () => {
      const previewing = posted(getParseQueryUrl(), reversed);
      await expression.fill(reversed);
      const { status, body } = await record("invalid-range", await previewing);
      const alert = editor.getByRole("alert").first();
      if (branch) {
        expect(status).toBe(400);
        expect(body.code).toBe("invalid_date");
        expect(body.detail).toBe("date range is reversed");
        expect(body.position).toEqual({ offset: 0, end: reversed.length });
        await expect(alert).toHaveText("date range is reversed");
      } else {
        expect(status).toBe(422);
        expect(body.code).toBe("invalid_query");
        expect(body.detail).toContain("unknown expression field");
        await expect(alert).toContainText("unknown expression field");
      }
      await expect(focusError).toBeVisible();
      await expect(checking).toHaveCount(0);
      await capture("invalid-range");
      // The existing control selects the rejected span: the whole range, or A's unknown field.
      await focusError.click();
      await expect(expression).toBeFocused();
      expect(
        await expression.evaluate((field) => {
          const textarea = field as HTMLTextAreaElement;
          return [textarea.selectionStart, textarea.selectionEnd];
        }),
      ).toEqual([0, branch ? reversed.length : "date:".length]);
    });

    let fingerprint = "";
    await test.step("one document date", async () => {
      const previewing = posted(getParseQueryUrl(), day);
      await expression.fill(day);
      const { status, body } = await record("date-query", await previewing);
      if (branch) {
        expect(status).toBe(200);
        const preview = body as QueryPreview;
        expect(preview.query.text).toBe(day);
        expect(Object.hasOwn(preview.query, "timezone")).toBe(false);
        fingerprint = preview.query_fingerprint;
        expect(fingerprint).toMatch(/^sha256:[0-9a-f]{64}$/);
        await expect(editor.getByRole("status", { name: "Query valid" })).toBeAttached();
        await expect(editor.getByRole("alert")).toHaveCount(0);
      } else {
        expect(status).toBe(422);
        expect(body.code).toBe("invalid_query");
        expect(body.detail).toContain("unknown expression field");
        await expect(editor.getByRole("alert").first()).toContainText("unknown expression field");
      }
      await expect(checking).toHaveCount(0);
      await capture("date-query");
    });

    await test.step("search the document date", async () => {
      const searching = posted(getCreateWorkspaceQueryUrl(), day);
      await editor.getByRole("button", { name: "Search", exact: true }).click();
      const response = await searching;
      const submitted = response.request().postDataJSON();
      expect(Object.hasOwn(submitted.query, "timezone")).toBe(false);
      const { status, body } = await record("results", response);
      if (branch) {
        expect(status).toBe(200);
        const snapshot = body as SnapshotPage;
        expect(snapshot.timezone).toBe("UTC");
        expect(snapshot.timezone_defaulted).toBe(true);
        expect(snapshot.query.text).toBe(day);
        expect(snapshot.query_fingerprint).toBe(fingerprint);
        expect(snapshot.total).toBe(expected.length);
        expect(snapshot.rows.map((row) => row.node_id).toSorted((a, b) => a - b)).toEqual(
          expected.map((node) => node.node_id).toSorted((a, b) => a - b),
        );
        await expect(editor).toBeHidden();
        await expect(results).toHaveAttribute("data-snapshot-id", snapshot.snapshot_id);
        await expect(results).toHaveAttribute("data-member-hash", snapshot.member_hash);
        await expect(rows).toHaveCount(expected.length);
        const displayed = await rows.evaluateAll((elements) =>
          elements.map((row) => ({
            node_id: Number(row.getAttribute("data-snapshot-node")),
            path: row.querySelector("td.name-cell")?.getAttribute("aria-label") ?? null,
          })),
        );
        expect(displayed.map((row) => row.path).toSorted()).toEqual(
          expected.map((node) => `/${node.name}`).toSorted(),
        );
        evidence.receipt = {
          displayed,
          snapshot_id: snapshot.snapshot_id,
          member_hash: snapshot.member_hash,
          query_fingerprint: snapshot.query_fingerprint,
          snapshot_fingerprint: snapshot.snapshot_fingerprint,
          timezone: snapshot.timezone,
          timezone_defaulted: snapshot.timezone_defaulted,
          requested_timezone: null,
          total: snapshot.total,
        };
      } else {
        expect(status).toBe(422);
        expect(body.code).toBe("invalid_query");
        expect(body.detail).toContain("unknown expression field");
        await expect(editor).toBeVisible();
        await expect(
          editor.getByRole("alert").filter({ hasText: "unknown expression field" }).first(),
        ).toBeVisible();
        await expect(checking).toHaveCount(0);
        await expect(focusError).toBeVisible();
        await expect(page.getByRole("table", { name: "Snapshot documents" })).toHaveCount(0);
      }
      await capture("results");
    });

    // Document date sort and saved searches, from the same inputs in both trees.
    const nodeID = (name: string) => nodes.find((node) => node.name === name)!.node_id;
    const ascending = [nodeID("evening-offset.eml"), nodeID("morning-utc.eml")];
    const sortField = branch ? "document_date" : "name";
    const choose = async (combobox: Locator, title: string, label: string) => {
      await combobox.click();
      await listbox.getByRole("option", { name: label, exact: true }).click();
      await expect(combobox).toHaveAttribute("aria-label", `${title}: ${label}`);
    };
    // C closes the dialog on a successful search; A keeps it open on the rejection.
    const reopen = async () => {
      if (await editor.isVisible()) return;
      await page.getByRole("button", { name: "Advanced", exact: true }).click();
      await expect(editor).toBeVisible();
      await expect(expression).toHaveValue(day);
      await expect(checking).toHaveCount(0);
      await expect(editor.getByRole("status", { name: "Query valid" })).toBeAttached();
    };
    const settledRejection = async () => {
      await expect(checking).toHaveCount(0);
      await expect(
        editor.getByRole("alert").filter({ hasText: "unknown expression field" }).first(),
      ).toBeVisible();
      await expect(focusError).toBeVisible();
    };
    const search = async (state: string, sort: Sort) => {
      const searching = posted(getCreateWorkspaceQueryUrl(), day, sort);
      await editor.getByRole("button", { name: "Search", exact: true }).click();
      const response = await searching;
      const submitted = response.request().postDataJSON();
      expect(submitted.query.sort).toEqual(sort);
      expect(Object.hasOwn(submitted.query, "timezone")).toBe(false);
      const { status, body } = await record(state, response);
      let order: number[] | null = null;
      if (branch) {
        expect(status).toBe(200);
        const snapshot = body as SnapshotPage;
        expect(snapshot.query.sort).toEqual(sort);
        expect(snapshot.timezone).toBe("UTC");
        expect(snapshot.timezone_defaulted).toBe(true);
        expect(snapshot.total).toBe(expected.length);
        // Dated rows by document date; both instants share their order in any zone.
        order = snapshot.rows.map((row) => row.node_id);
        expect(order).toEqual(sort.direction === "asc" ? ascending : ascending.toReversed());
        await expect(editor).toBeHidden();
        await expect(results).toHaveAttribute("data-snapshot-id", snapshot.snapshot_id);
        await expect(rows).toHaveCount(order.length);
        expect(
          await rows.evaluateAll((elements) =>
            elements.map((row) => Number(row.getAttribute("data-snapshot-node"))),
          ),
        ).toEqual(order);
      } else {
        expect(status).toBe(422);
        expect(body.code).toBe("invalid_query");
        expect(body.detail).toContain("unknown expression field");
        await expect(editor).toBeVisible();
        await settledRejection();
        await expect(page.getByRole("table", { name: "Snapshot documents" })).toHaveCount(0);
      }
      return { status, request_sort: submitted.query.sort as Sort, rows: order };
    };

    await test.step("sort options", async () => {
      await reopen();
      await expect(expression).toHaveValue(day);
      await expect(sortBy).toHaveAttribute("aria-label", "Sort by: Name");
      await expect(direction).toHaveAttribute("aria-label", "Direction: Ascending");
      await sortBy.click();
      // A has no Document date option; its actual list is the Before.
      const labels = branch ? sortLabels : sortLabels.filter((label) => label !== "Document date");
      await expect(listbox.getByRole("option")).toHaveText(labels);
      evidence.sort.options = labels;
      await capture("sort-options", darkViews, async () => {
        if (!(await listbox.isVisible())) await sortBy.click();
        await expect(listbox.getByRole("option")).toHaveText(labels);
        await listbox.scrollIntoViewIfNeeded();
      });
    });

    await test.step("sort by document date, ascending", async () => {
      let preview: { status: number; sort: Sort } | null = null;
      if (branch) {
        // Direction is already Ascending, so this one choice sends the settled preview.
        const sort: Sort = { field: "document_date", direction: "asc" };
        const previewing = posted(getParseQueryUrl(), day, sort);
        if (!(await listbox.isVisible())) await sortBy.click();
        await listbox.getByRole("option", { name: "Document date", exact: true }).click();
        const { status, body } = await record("sort-ascending-preview", await previewing);
        expect(status).toBe(200);
        expect((body as QueryPreview).query.sort).toEqual(sort);
        expect(Object.hasOwn((body as QueryPreview).query, "timezone")).toBe(false);
        preview = { status, sort: (body as QueryPreview).query.sort as Sort };
        await expect(sortBy).toHaveAttribute("aria-label", "Sort by: Document date");
        await expect(checking).toHaveCount(0);
        await expect(editor.getByRole("status", { name: "Query valid" })).toBeAttached();
        await expect(editor.getByRole("alert")).toHaveCount(0);
      } else {
        await expect(listbox.getByRole("option", { name: "Document date" })).toHaveCount(0);
        await sortBy.click();
        await expect(listbox).toBeHidden();
        await expect(sortBy).toHaveAttribute("aria-label", "Sort by: Name");
        await settledRejection();
      }
      await expect(listbox).toBeHidden();
      await expect(direction).toHaveAttribute("aria-label", "Direction: Ascending");
      await capture("sort-ascending", darkViews);
      const submitted = await search("sort-ascending-results", {
        field: sortField,
        direction: "asc",
      });
      evidence.sort.ascending = { preview, results: submitted };
    });

    await test.step("sort by document date, descending", async () => {
      await reopen();
      await expect(sortBy).toHaveAttribute(
        "aria-label",
        branch ? "Sort by: Document date" : "Sort by: Name",
      );
      const sort: Sort = { field: sortField, direction: "desc" };
      const previewing = posted(getParseQueryUrl(), day, sort);
      await choose(direction, "Direction", "Descending");
      const { status, body } = await record("sort-descending-preview", await previewing);
      if (branch) {
        expect(status).toBe(200);
        expect((body as QueryPreview).query.sort).toEqual(sort);
        await expect(checking).toHaveCount(0);
        await expect(editor.getByRole("alert")).toHaveCount(0);
      } else {
        expect(status).toBe(422);
        expect(body.code).toBe("invalid_query");
        await settledRejection();
      }
      const preview = { status, sort };
      const submitted = await search("sort-descending-results", sort);
      evidence.sort.descending = { preview, results: submitted };
      await capture("sort-descending", darkViews);
    });

    // The drawer's own create/update functions check each receipt; a rejected
    // receipt shows an alert and disables both save buttons.
    let stored: {
      id: string;
      revision: number;
      created_at: string;
      payload: Record<string, unknown>;
    };
    const drawerSettled = async (heading: string) => {
      await expect(drawer.getByText("Loading…")).toHaveCount(0);
      await expect(drawer.getByRole("alert")).toHaveCount(0);
      await expect(definitionEditor.getByRole("heading", { level: 3 })).toHaveText(heading);
      await expect(queryJSON).toBeVisible();
    };
    const editorJSON = async () => JSON.parse(await queryJSON.inputValue());
    const listed = () =>
      page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === getListSavedQueriesUrl() &&
          response.request().method() === "GET",
      );
    const mutation = async (
      state: string,
      button: string,
      route: string,
      method: string,
      status: number,
      revision: number,
    ) => {
      const mutating = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === route && response.request().method() === method,
      );
      const listing = listed();
      await definitionEditor.getByRole("button", { name: button, exact: true }).click();
      const response = await mutating;
      const sent = response.request().postDataJSON();
      const ifMatch = response.request().headers()["if-match"] ?? null;
      const { body } = await record(state, response);
      // A rejected mutation never refreshes the list, so check it before waiting.
      expect(response.status()).toBe(status);
      const listedPage = await (await listing).json();
      const item = listedPage.items.find((entry: { id: string }) => entry.id === body.id);
      expect(item?.revision).toBe(revision);
      expect(body.revision).toBe(revision);
      expect(response.headers().etag).toBe(`"${revision}"`);
      await expect(drawer.getByText(`Saved ${savedName}.`, { exact: true })).toBeVisible();
      await drawerSettled(`Edit ${savedName}`);
      await expect(
        definitionEditor.getByRole("button", { name: "Save changes", exact: true }),
      ).toBeEnabled();
      return {
        sent,
        body,
        summary: {
          request: {
            method,
            if_match: ifMatch,
            payload_timezone: sent.payload.timezone ?? null,
            sort: sent.payload.sort,
          },
          status,
          etag: response.headers().etag,
          revision: body.revision,
          payload_timezone: body.payload.timezone ?? null,
          listed_revision: item?.revision ?? null,
          notice: `Saved ${savedName}.`,
        },
      };
    };

    await test.step("save the dated search", async () => {
      await reopen();
      await editor.getByRole("button", { name: "Save…", exact: true }).click();
      await expect(editor).toBeHidden();
      await expect(drawer).toBeVisible();
      await expect(drawer.getByText("Loading…")).toHaveCount(0);
      await expect(drawer.getByText(/^No searches yet\./)).toBeVisible();
      await expect(definitionEditor.getByRole("heading", { level: 3 })).toHaveText("New search");
      await definitionEditor.getByText("Edit as JSON", { exact: true }).click();
      await expect(queryJSON).toBeVisible();
      const draft = await editorJSON();
      expect(draft.text).toBe(day);
      expect(draft.sort).toEqual({ field: sortField, direction: "desc" });
      expect(Object.hasOwn(draft, "timezone")).toBe(false);
      await definitionEditor.getByRole("textbox", { name: "Definition name" }).fill(savedName);
      const { sent, body, summary } = await mutation(
        "saved-create",
        "Save as new",
        getCreateSavedQueryUrl(),
        "POST",
        201,
        1,
      );
      expect(sent.name).toBe(savedName);
      expect(Object.hasOwn(sent.payload, "timezone")).toBe(false);
      // C pins its default zone into a date query saved without one; A stores it as sent.
      if (branch) expect(body.payload.timezone).toBe("UTC");
      else expect(Object.hasOwn(body.payload, "timezone")).toBe(false);
      expect((await editorJSON()).timezone).toBe(branch ? "UTC" : undefined);
      stored = body;
      evidence.saved.id = body.id;
      evidence.saved.create = summary;
      await capture("saved-create", darkViews);
    });

    await test.step("change the saved search", async () => {
      const value = await editorJSON();
      value.sort.direction = "asc";
      await queryJSON.fill(JSON.stringify(value, null, 2));
      const { sent, body, summary } = await mutation(
        "saved-update",
        "Save changes",
        getUpdateSavedQueryUrl(stored.id),
        "PATCH",
        200,
        2,
      );
      expect(summary.request.if_match).toBe('"1"');
      expect(sent.payload.sort).toEqual({ field: sortField, direction: "asc" });
      expect(sent.payload.timezone).toBe(branch ? "UTC" : undefined);
      expect(body.id).toBe(stored.id);
      expect(body.created_at).toBe(stored.created_at);
      expect(body.payload.sort).toEqual({ field: sortField, direction: "asc" });
      expect(body.payload.timezone).toBe(branch ? "UTC" : undefined);
      stored = body;
      evidence.saved.update = summary;
      await capture("saved-update", darkViews);
    });

    await test.step("save again without the zone", async () => {
      const value = await editorJSON();
      expect(value.timezone).toBe(branch ? "UTC" : undefined);
      delete value.timezone;
      await queryJSON.fill(JSON.stringify(value, null, 2));
      const { sent, body, summary } = await mutation(
        "saved-noop",
        "Save changes",
        getUpdateSavedQueryUrl(stored.id),
        "PATCH",
        200,
        2,
      );
      expect(summary.request.if_match).toBe('"2"');
      expect(Object.hasOwn(sent.payload, "timezone")).toBe(false);
      // The server re-pins the same zone, so the stored definition and revision stay.
      expect(body.id).toBe(stored.id);
      expect(body.created_at).toBe(stored.created_at);
      expect(body.payload).toEqual(stored.payload);
      expect((await editorJSON()).timezone).toBe(branch ? "UTC" : undefined);
      evidence.saved.noop = summary;
      await capture("saved-noop", darkViews);
    });

    await stop();
  } catch (error) {
    failure = error;
    evidence.error = session ? inspect(error).replaceAll(session, "[redacted]") : inspect(error);
    throw error;
  } finally {
    try {
      await stop();
      await rm(scratch, { recursive: true, force: true });
      evidence.scratch_removed = true;
    } catch (cleanupError) {
      evidence.cleanup_error = session
        ? inspect(cleanupError).replaceAll(session, "[redacted]")
        : inspect(cleanupError);
      if (failure !== undefined) {
        throw new Error(`Date query cleanup failed: ${inspect(cleanupError)}`, { cause: failure });
      }
      throw cleanupError;
    } finally {
      await writeFile(
        path.join(captures!, "evidence.json"),
        `${JSON.stringify(evidence, null, 2)}\n`,
      ).catch(() => {});
    }
  }
});
