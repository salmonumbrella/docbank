import { expect, test, type Locator, type Response } from "@playwright/test";
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { access, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { inspect, promisify } from "node:util";
import { fileURLToPath } from "node:url";
import {
  getCreateWorkspaceQueryUrl,
  getParseQueryUrl,
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
  const evidence: Record<string, unknown> & { captures: unknown[]; states: unknown[] } = {
    tree,
    captures: [],
    states: [],
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
    };
  };
  // Light and dark at desktop; narrow in dark only (more than four screenshots).
  const capture = async (state: string) => {
    for (const [viewport, size, themes] of [
      ["desktop", desktop, ["light", "dark"]],
      ["narrow", narrow, ["dark"]],
    ] as const) {
      await page.setViewportSize(size);
      for (const theme of themes) {
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
  const posted = (route: string, text: string) =>
    page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === route &&
        response.request().method() === "POST" &&
        (route === getParseQueryUrl()
          ? response.request().postDataJSON()?.text
          : response.request().postDataJSON()?.query?.text) === text,
    );
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
        expect(body.detail).toContain("date range is reversed");
        await expect(alert).toContainText("date range is reversed");
      } else {
        expect(status).toBe(422);
        expect(body.code).toBe("invalid_query");
        expect(body.detail).toContain("unknown expression field");
        await expect(alert).toContainText("unknown expression field");
        await expect(editor.getByRole("button", { name: "Focus query error" })).toBeVisible();
      }
      await expect(editor.getByRole("status", { name: "Checking query" })).toHaveCount(0);
      await capture("invalid-range");
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
      await expect(editor.getByRole("status", { name: "Checking query" })).toHaveCount(0);
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
        await expect(page.getByRole("table", { name: "Snapshot documents" })).toHaveCount(0);
      }
      await capture("results");
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
