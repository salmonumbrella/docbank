import { expect, test, type Page, type Request, type TestInfo } from "@playwright/test";
import { execFile } from "node:child_process";
import { access, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { inspect, promisify } from "node:util";
import { fileURLToPath } from "node:url";
import type { SnapshotPage } from "../src/snapshots.js";

const exec = promisify(execFile);
const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
// "branch" proves this checkout; "base" captures the same fixtures with the
// base binary, which has no Dates, People or fact columns.
const proof = process.env.DOCBANK_DOCUMENT_FACTS_PROOF;
test.skip(!proof, "DOCBANK_DOCUMENT_FACTS_PROOF enables this proof");
// The browser runs in Tokyo; exact times must still show in the configured
// America/Toronto timeline zone, and floating times as written.
test.use({ timezoneId: "Asia/Tokyo" });

// Invented people from the approved prototype; every address is reserved.
const mara = '"Mara Okonkwo-Fell" <mara@okonkwo.example.test>';
const ines = '"Ines Albarran" <ines@albarran.example.test>';
const teodor = '"Teodor Vask" <teodor@vask.example.test>';
const hidden = '"Private Reviewer" <reviewer@private.example.test>';
const email = [
  `From: ${mara}`,
  `To: ${ines}`,
  `Cc: ${teodor}`,
  `Bcc: ${hidden}`,
  "Date: Tue, 10 Mar 2026 23:40:00 -0400",
  "Subject: Board update",
  "",
  "Board update for the March meeting.",
  "",
].join("\r\n");
const calendar = (start: string, end: string) =>
  [
    "BEGIN:VCALENDAR",
    "VERSION:2.0",
    "PRODID:-//Docbank proof//EN",
    "BEGIN:VEVENT",
    "UID:board-offsite@example.test",
    "SUMMARY:Board offsite",
    `DTSTART;VALUE=DATE:${start}`,
    `DTEND;VALUE=DATE:${end}`,
    "END:VEVENT",
    "END:VCALENDAR",
    "",
  ].join("\r\n");
const BULK = 150;
const boardQuery = {
  v: 1,
  text: "board",
  syntax: "simple",
  mode: "lexical",
  filters: {},
  sort: { field: "name", direction: "asc" },
} as const;
// The new columns need a results region of at least 900 px beside the
// inspector, which a 1440 px window does not leave; wide captures use 1920.
const WIDE = { width: 1920, height: 1080 };
// Producers record what each source wrote: the email Date header and the PDF
// info dates both carry seconds, so both show at second precision.
const EMAIL_SENT = "Mar 10, 2026, 11:40:00 PM EDT";
const PDF_CREATED = "Mar 10, 2026, 11:30:00 PM (no zone)";
const factsURL = /\/api\/v1\/versions\/([0-9a-f-]{36})\/(dates|people)(?:\?|$)/u;

// The inspector scrolls inside its own panel beside the table, so a viewport
// capture can clip the Dates and People sections below the preview. Align the
// facts block with the panel's top, the same anchor on base and branch, and
// leave the page and the table where they are. Stacked narrow layouts have no
// inspector scroller and are captured as laid out.
async function revealFacts(page: Page) {
  await page.evaluate(() => {
    const panel = document.querySelector<HTMLElement>('aside[aria-label="Document inspector"]');
    const rows = panel?.querySelector<HTMLElement>(".detail-rows");
    if (!panel || !rows || !/(auto|scroll)/u.test(getComputedStyle(panel).overflowY)) return;
    panel.scrollTop += rows.getBoundingClientRect().top - panel.getBoundingClientRect().top;
  });
}

// Attachments are named document-facts-<base|branch>-<state>-<wide|narrow>-<theme>.png;
// wide is 1920×1080 and narrow 860×1100.
async function capture(page: Page, testInfo: TestInfo, name: string, theme: "light" | "dark") {
  await page.evaluate(async (value) => {
    localStorage.setItem("docbank-theme", value);
    document.documentElement.classList.toggle("dark", value === "dark");
    await document.fonts.ready;
  }, theme);
  await revealFacts(page);
  // The JSON reporter embeds the body as base64 in the uploaded CI report.
  const body = await page.screenshot({ animations: "disabled" });
  await testInfo.attach(`document-facts-${proof}-${name}-${theme}.png`, {
    body,
    contentType: "image/png",
  });
}

async function captureStates(
  page: Page,
  testInfo: TestInfo,
  state: string,
  ready: (narrow: boolean) => Promise<void>,
  { narrow = true, dark = true } = {},
) {
  await page.setViewportSize(WIDE);
  await ready(false);
  if (dark) await capture(page, testInfo, `${state}-wide`, "dark");
  await capture(page, testInfo, `${state}-wide`, "light");
  if (!narrow) return;
  await page.setViewportSize({ width: 860, height: 1100 });
  await ready(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  if (dark) await capture(page, testInfo, `${state}-narrow`, "dark");
  await capture(page, testInfo, `${state}-narrow`, "light");
  await page.setViewportSize(WIDE);
}

test("shows document dates and people in the inspector and both tables", async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000);
  const binary = process.env.DOCBANK_SCREENSHOT_BINARY;
  if (!binary || !path.isAbsolute(binary)) {
    throw new Error("DOCBANK_SCREENSHOT_BINARY must name an absolute binary");
  }
  await access(binary);
  const scratch = await mkdtemp(path.join(tmpdir(), "docbank-document-facts-"));
  const vault = path.join(scratch, "vault");
  const env = {
    ...process.env,
    DOCBANK_HOME: vault,
    DOCBANK_LOCK_DIR: path.join(scratch, "locks"),
    DOCBANK_TELEMETRY_ENABLED: "0",
  };
  const run = async (...args: string[]) =>
    (await exec(binary, args, { cwd: repository, env, timeout: 90_000 })).stdout.trim();
  const fixture = async (...args: string[]) =>
    (
      await exec(
        "go",
        ["run", "-tags", "fts5", "./frontend/screenshots/document-facts-fixture.go", ...args],
        { cwd: repository, env, timeout: 240_000 },
      )
    ).stdout.trim();
  let started = false;
  let failure: unknown;
  const stop = async () => {
    if (!started) return;
    const before = JSON.parse(await run("daemon", "status", "--json"));
    if (before.running) await run("daemon", "stop");
    const after = JSON.parse(await run("daemon", "status", "--json"));
    if (after.running !== false) {
      throw new Error(
        `Synthetic daemon ${before.pid ?? "unknown"} did not stop; retained ${scratch}`,
      );
    }
    started = false;
  };
  try {
    await mkdir(vault, { mode: 0o700 });
    // The base binary predates [web] timeline_timezone and rejects unknown keys.
    if (proof === "branch")
      await writeFile(
        path.join(vault, "config.toml"),
        '[web]\ntimeline_timezone = "America/Toronto"\n',
        { mode: 0o600 },
      );
    const board = path.join(scratch, "board");
    const bulk = path.join(scratch, "bulk");
    await mkdir(board);
    await mkdir(bulk);
    await writeFile(path.join(board, "board-update.eml"), email, { mode: 0o600 });
    await writeFile(path.join(board, "board-offsite.ics"), calendar("20260309", "20260312"));
    await fixture("pdf", path.join(board, "board-minutes.pdf"));
    for (let index = 0; index < BULK; index++)
      await writeFile(
        path.join(bulk, `memo-${String(index).padStart(3, "0")}.txt`),
        `Synthetic memo ${index}.\n`,
      );
    started = true; // add may start the daemon even if the command fails.
    await run("add", board, "--dest", "/");
    await run("add", bulk, "--dest", "/");

    let issued = new URL(await run("web", "--no-browser"));
    let session = new URLSearchParams(issued.hash.slice(1)).get("web_session") ?? "";
    const api = async (pathname: string, init: RequestInit = {}) =>
      fetch(`http://127.0.0.1:${issued.port}${pathname}`, {
        ...init,
        signal: AbortSignal.timeout(30_000),
        headers: {
          Host: issued.host,
          "Content-Type": "application/json",
          "X-Docbank-Web-Session": session,
        },
      });
    const freeze = async () => {
      const response = await api("/api/v1/workspace/queries", {
        method: "POST",
        body: JSON.stringify({ query: boardQuery, page_size: 50 }),
      });
      return response.ok ? ((await response.json()) as SnapshotPage) : undefined;
    };
    const versions = async () =>
      Object.fromEntries(
        (await freeze())?.rows.map((row) => [row.name, row.content_version_id]) ?? [],
      );
    const names = ["board-minutes.pdf", "board-offsite.ics", "board-update.eml"];
    await expect
      .poll(async () => Object.keys(await versions()).sort(), {
        timeout: 120_000,
        intervals: [1_000],
      })
      .toEqual(names);
    const version = await versions();

    // An operator author assertion with no derived edge yet: the inspector must
    // render it from the assertion's own display_name.
    await stop();
    await fixture("assert", vault, version["board-minutes.pdf"]!, "Jory Pellham");
    started = true;
    issued = new URL(await run("web", "--no-browser"));
    session = new URLSearchParams(issued.hash.slice(1)).get("web_session") ?? "";

    if (proof === "branch") {
      // Wait for real publication, not a guess: every fixture's dates and the
      // email's and PDF's people.
      const published = async (id: string, resource: "dates" | "people") => {
        const response = await api(`/api/v1/versions/${id}/${resource}`);
        return response.ok ? ((await response.json()) as { state: string }).state : "";
      };
      for (const name of names)
        for (const resource of ["dates", "people"] as const)
          await expect
            .poll(
              async () => ["published", "none"].includes(await published(version[name]!, resource)),
              { timeout: 120_000, intervals: [1_000] },
            )
            .toBe(true);
    }

    // Count facts reads as the browser makes them.
    const inFlight = new Set<Request>();
    let peak = 0;
    const requested: string[] = [];
    page.on("request", (request) => {
      const match = factsURL.exec(new URL(request.url()).pathname);
      if (!match) return;
      requested.push(`${match[1]}/${match[2]}`);
      inFlight.add(request);
      peak = Math.max(peak, inFlight.size);
    });
    const settle = (request: Request) => inFlight.delete(request);
    page.on("requestfinished", settle);
    page.on("requestfailed", settle);

    const inspector = page.getByRole("complementary", { name: "Document inspector" });
    const liveRow = (name: string) => page.locator("tr[data-live-node]").filter({ hasText: name });
    const openFolder = async (name: string) => {
      await liveRow(name).first().dblclick();
      await expect(page.locator("tr[data-live-node]").filter({ hasText: name })).not.toHaveCount(0);
    };
    // A fresh page, so each state starts with a new facts owner.
    const browse = async (folder: string) => {
      await page.goto("about:blank");
      await page.goto(issued.toString());
      await openFolder(folder);
    };
    await page.setViewportSize(WIDE);
    await browse("board");
    await expect(liveRow("board-update.eml")).toBeVisible();

    // Exact email time, configured zone, From/To/Cc chips and no Bcc anywhere.
    await liveRow("board-update.eml").click();
    await captureStates(page, testInfo, "folder-email", async (narrow) => {
      await expect(inspector.getByRole("heading", { name: "board-update.eml" })).toBeVisible();
      if (proof !== "branch") return;
      const dates = inspector.getByRole("region", { name: "Dates" });
      await expect(dates.getByText(EMAIL_SENT)).toBeVisible();
      await expect(dates.getByText("Times show in America/Toronto.")).toBeVisible();
      const people = inspector.getByRole("region", { name: "People" });
      for (const name of ["Mara Okonkwo-Fell", "Ines Albarran", "Teodor Vask"])
        await expect(people.getByRole("button", { name })).toBeVisible();
      await expect(page.getByText("Private Reviewer")).toHaveCount(0);
      const header = page.getByRole("columnheader", { name: "Document date" });
      if (narrow) await expect(header).toBeHidden();
      else {
        await expect(header).toBeVisible();
        await expect(liveRow("board-update.eml").getByText(EMAIL_SENT)).toBeVisible();
      }
    });

    // Floating PDF dates, an asserted author and an unresolved metadata author.
    await liveRow("board-minutes.pdf").click();
    await captureStates(page, testInfo, "folder-pdf", async (narrow) => {
      await expect(inspector.getByRole("heading", { name: "board-minutes.pdf" })).toBeVisible();
      if (proof !== "branch") return;
      const dates = inspector.getByRole("region", { name: "Dates" });
      await expect(dates.getByText(PDF_CREATED)).toBeVisible();
      await expect(dates.getByText(/floating \(no zone\)/u).first()).toBeVisible();
      const people = inspector.getByRole("region", { name: "People" });
      await expect(people.getByRole("button", { name: "Jory Pellham" })).toBeVisible();
      await expect(people.getByText("L. Beerwijk")).toBeVisible();
      await expect(people.getByText("needs review")).toBeVisible();
      if (!narrow)
        await expect(liveRow("board-minutes.pdf").getByText("1 to review")).toBeVisible();
    });

    // Civil all-day dates have no time.
    await liveRow("board-offsite.ics").click();
    await captureStates(
      page,
      testInfo,
      "folder-calendar",
      async () => {
        await expect(inspector.getByRole("heading", { name: "board-offsite.ics" })).toBeVisible();
        if (proof !== "branch") return;
        await expect(
          inspector.getByRole("region", { name: "Dates" }).getByText("Mar 9, 2026"),
        ).toBeVisible();
      },
      { narrow: false },
    );

    if (proof === "branch") {
      // A slow dates read shows its spinner while people are already shown.
      let release!: () => void;
      const held = new Promise<void>((resolve) => (release = resolve));
      const datesOf = `**/api/v1/versions/${version["board-update.eml"]}/dates*`;
      await page.route(datesOf, async (route) => {
        await held;
        await route.continue();
      });
      await browse("board");
      await liveRow("board-update.eml").click();
      await expect(
        inspector.getByRole("region", { name: "Dates" }).getByLabel("Loading dates"),
      ).toBeVisible();
      await capture(page, testInfo, "dates-loading-wide", "light");
      release();
      await expect(inspector.getByText(EMAIL_SENT)).toBeVisible();
      await page.unroute(datesOf);

      // One endpoint failing leaves the other shown; Retry reads only that one.
      const peopleOf = `**/api/v1/versions/${version["board-minutes.pdf"]}/people*`;
      let failed = false;
      await page.route(peopleOf, async (route) => {
        if (failed) return route.continue();
        failed = true;
        await route.abort("connectionfailed");
      });
      await browse("board");
      await liveRow("board-minutes.pdf").click();
      const people = inspector.getByRole("region", { name: "People" });
      await expect(people.getByText("People couldn't be loaded.")).toBeVisible();
      await expect(
        inspector.getByRole("region", { name: "Dates" }).getByText(PDF_CREATED),
      ).toBeVisible();
      await capture(page, testInfo, "people-failed-wide", "light");
      const before = requested.length;
      await people.getByRole("button", { name: "Retry" }).click();
      await expect(people.getByText("L. Beerwijk")).toBeVisible();
      // Other rows' reads may still be queued; only this version's reads count.
      expect(
        requested
          .slice(before)
          .filter((read) => read.startsWith(`${version["board-minutes.pdf"]}/`)),
      ).toEqual([`${version["board-minutes.pdf"]}/people`]);
      await capture(page, testInfo, "people-retried-wide", "light");
      await page.unroute(peopleOf);
    }

    // A frozen result keeps the version it saved after the file changes.
    const frozenStart = requested.length;
    const launch = new URL(issued);
    const fragment = new URLSearchParams(launch.hash.slice(1));
    fragment.set("query", JSON.stringify(boardQuery));
    launch.hash = fragment.toString();
    await page.goto("about:blank");
    await page.goto(launch.toString());
    const editor = page.getByRole("dialog", { name: "Filters and sort" });
    await editor.getByRole("button", { name: "Search", exact: true }).click();
    await expect(editor).toBeHidden();
    const results = page.getByRole("region", { name: "Frozen query results" });
    const frozenRow = (name: string) =>
      results.locator("tr[data-snapshot-node]").filter({ hasText: name });
    await expect(frozenRow("board-offsite.ics")).toBeVisible();
    await writeFile(path.join(board, "board-offsite.ics"), calendar("20260316", "20260317"));
    await run("add", path.join(board, "board-offsite.ics"), "--dest", "/board", "--replace");
    await expect
      .poll(async () => (await versions())["board-offsite.ics"], {
        timeout: 60_000,
        intervals: [1_000],
      })
      .not.toBe(version["board-offsite.ics"]);
    const replaced = (await versions())["board-offsite.ics"];
    await frozenRow("board-offsite.ics").click();
    await captureStates(page, testInfo, "frozen-saved-version", async () => {
      await expect(inspector.getByRole("heading", { name: "board-offsite.ics" })).toBeVisible();
      if (proof !== "branch") return;
      await expect(
        inspector.getByRole("region", { name: "Dates" }).getByText("Mar 9, 2026"),
      ).toBeVisible();
      await expect(inspector.getByText("Mar 16, 2026")).toHaveCount(0);
    });
    if (proof === "branch") {
      // The frozen row reads its saved version as soon as it renders, so count
      // from the frozen page's load, and the replacement is never read.
      const reads = requested.slice(frozenStart).filter((read) => read.endsWith("/dates"));
      expect(reads).toContain(`${version["board-offsite.ics"]}/dates`);
      expect(reads).not.toContain(`${replaced}/dates`);
    }

    if (proof === "branch") {
      // A large folder reads only rows in view, at most four at a time, and
      // hidden columns read nothing.
      await page.setViewportSize(WIDE);
      await browse("bulk");
      await expect(liveRow("memo-000.txt")).toBeVisible();
      const start = requested.length;
      await expect
        .poll(() => inFlight.size === 0 && requested.length > start, { timeout: 30_000 })
        .toBe(true);
      const rows = new Set(requested.slice(start).map((read) => read.split("/")[0]));
      expect(rows.size).toBeGreaterThan(0);
      expect(rows.size).toBeLessThan(40);
      expect(peak).toBeLessThanOrEqual(4);
      await page.setViewportSize({ width: 860, height: 1100 });
      const narrowStart = requested.length;
      await browse("bulk");
      await expect(liveRow("memo-000.txt")).toBeVisible();
      await expect(page.getByRole("columnheader", { name: "People" })).toBeHidden();
      await expect(page.getByRole("columnheader", { name: "Modified" })).toBeVisible();
      await expect.poll(() => inFlight.size, { timeout: 15_000 }).toBe(0);
      expect(requested.slice(narrowStart)).toEqual([]);
      // Phone width still hides Type and Modified, as before the new columns.
      await page.setViewportSize({ width: 600, height: 1000 });
      await expect(page.getByRole("columnheader", { name: "Size" })).toBeVisible();
      await expect(page.getByRole("columnheader", { name: "Type" })).toBeHidden();
      await expect(page.getByRole("columnheader", { name: "Modified" })).toBeHidden();
      await page.setViewportSize(WIDE);

      // A person chip searches for that person.
      await browse("board");
      await liveRow("board-update.eml").click();
      await inspector
        .getByRole("region", { name: "People" })
        .getByRole("button", { name: "Mara Okonkwo-Fell" })
        .click();
      await expect(page.getByRole("searchbox", { name: "Search documents" })).toHaveValue(
        /^person:[0-9a-f-]{36}$/u,
      );
    }
  } catch (error) {
    failure = error;
    throw error;
  } finally {
    try {
      await stop();
      await rm(scratch, { recursive: true, force: true });
    } catch (cleanupError) {
      if (failure !== undefined) {
        throw new Error(`Document facts proof cleanup failed: ${inspect(cleanupError)}`, {
          cause: failure,
        });
      }
      throw cleanupError;
    }
  }
});
