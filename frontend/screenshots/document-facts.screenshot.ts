import { expect, test, type Page, type Request, type TestInfo } from "@playwright/test";
import { execFile } from "node:child_process";
import { access, mkdir, mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import type { SnapshotPage } from "../src/snapshots.js";
import { captureTheme } from "./capture-themes.js";

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
// The ordinary email has no Bcc. Docbank attaches a Bcc recipient to the sent
// event as a sensitive actor, and safe disclosure then drops that whole event
// (document.EventHasSensitiveEvidence), so its safe sent date needs no Bcc.
const email = [
  `From: ${mara}`,
  `To: ${ines}`,
  `Cc: ${teodor}`,
  "Date: Tue, 10 Mar 2026 23:40:00 -0400",
  "Subject: Board update",
  "",
  "Board update for the March meeting.",
  "",
].join("\r\n");
// The privacy fixture: the same send time with a Bcc, outside the board search.
const hiddenAddress = "reviewer@private.example.test";
const reviewEmail = [
  `From: ${mara}`,
  `To: ${ines}`,
  `Bcc: ${hidden}`,
  "Date: Tue, 10 Mar 2026 23:40:00 -0400",
  "Subject: Reviewer note",
  "",
  "A synthetic note with a blind-copy recipient.",
  "",
].join("\r\n");
const SENT_AT = "2026-03-11T03:40:00Z";
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

// A viewport capture can clip the Dates and People sections below the
// preview: wide, the inspector scrolls inside its own panel; narrow (≤900 px),
// it stacks under the table and the document scrolls. After every viewport
// change, align the facts block with the top of its nearest scrolling
// container, the same anchor on base and branch. Captures stay viewport-sized,
// never full-page.
async function revealFacts(page: Page) {
  await page.evaluate(() => {
    const rows = document.querySelector<HTMLElement>(
      'aside[aria-label="Document inspector"] .detail-rows',
    );
    if (!rows) return;
    let box = rows.parentElement;
    while (box && box !== document.body) {
      const overflow = getComputedStyle(box).overflowY;
      if (/(auto|scroll)/u.test(overflow) && box.scrollHeight > box.clientHeight) break;
      box = box.parentElement;
    }
    const scroller = box && box !== document.body ? box : document.scrollingElement;
    if (!scroller) return;
    const top = scroller === document.scrollingElement ? 0 : box!.getBoundingClientRect().top;
    scroller.scrollTop += rows.getBoundingClientRect().top - top;
  });
}

// Attachments are named document-facts-<base|branch>-<state>-<wide|narrow>-<theme>.png;
// wide is 1920×1080 and narrow 860×1100.
async function capture(page: Page, testInfo: TestInfo, name: string, theme: "light" | "dark") {
  await revealFacts(page);
  const prefix = `document-facts-${proof}-${name}`;
  const body = await captureTheme(page, testInfo.outputPath(), prefix, theme);
  // The JSON reporter embeds the body as base64 in the uploaded CI report.
  await testInfo.attach(`${prefix}-${theme}.png`, {
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
  const stop = async () => {
    if (started) await run("daemon", "stop");
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
    const privateDir = path.join(scratch, "private");
    await mkdir(board);
    await mkdir(bulk);
    await mkdir(privateDir);
    await writeFile(path.join(privateDir, "reviewer-note.eml"), reviewEmail, { mode: 0o600 });
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
    await run("add", privateDir, "--dest", "/");

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

    // An operator author assertion: the inspector renders it from the
    // assertion's own display_name. (Assertions beside a pending or failed
    // derivation are covered by the component tests, not here.)
    await stop();
    await fixture("assert", vault, version["board-minutes.pdf"]!, "Jory Pellham");
    started = true;
    issued = new URL(await run("web", "--no-browser"));
    session = new URLSearchParams(issued.hash.slice(1)).get("web_session") ?? "";

    if (proof === "branch") {
      // Wait until each exact saved version serves the safe facts this proof
      // expects. A state alone is not enough: "none" precedes the first
      // derivation, and "published" can precede the metadata a claim needs.
      // On timeout, report the last HTTP status and the (synthetic) payload so
      // a remote run tells unpublished, empty and mismatched facts apart.
      type Facts = {
        state?: string;
        claims?: { date_kind: string; when: Record<string, unknown> }[];
        edges?: { role: string; display_name: string }[];
        assertions?: { role: string; action: string; display_name: string }[];
        unresolved?: { raw_label: string }[];
      };
      const expectFacts = async (
        name: string,
        resource: "dates" | "people",
        ready: (facts: Facts) => boolean,
      ) => {
        let last: { status: number; body: unknown } = { status: 0, body: undefined };
        const poll = async () => {
          const response = await api(`/api/v1/versions/${version[name]}/${resource}`);
          const body = (await response.json().catch(() => undefined)) as Facts | undefined;
          last = { status: response.status, body };
          return response.ok && !!body && ready(body);
        };
        try {
          await expect.poll(poll, { timeout: 120_000, intervals: [1_000] }).toBe(true);
        } catch (error) {
          // Best effort: diagnostics never replace the readiness failure below.
          await diagnose(name, resource).catch(() => {});
          throw new Error(`${name} ${resource} not ready: ${JSON.stringify(last)}`, {
            cause: error,
          });
        }
      };
      // On a readiness timeout, attach what the synthetic daemon knows about
      // the document: the exact current version and its source metadata
      // (sensitive values redacted), the metadata and event workers' jobs, and
      // retry or failure lines from the daemon log. Each probe is bounded and
      // records its own error instead of throwing.
      const diagnose = async (name: string, resource: string) => {
        const probe = async (fn: () => Promise<unknown>) => {
          try {
            return { ok: true, value: await fn() };
          } catch (error) {
            return { ok: false, error: String(error).slice(0, 2_000) };
          }
        };
        const cli = async (...args: string[]) =>
          JSON.parse(
            (await exec(binary, args, { cwd: repository, env, timeout: 15_000 })).stdout,
          ) as unknown;
        const clip = (value: unknown) => {
          const text = typeof value === "string" ? value : JSON.stringify(value);
          return text.length > 300 ? `${text.slice(0, 300)}…` : value;
        };
        const stat = await probe(async () => {
          const node = (await cli(
            "document",
            "stat",
            `${name === "reviewer-note.eml" ? "/private" : "/board"}/${name}`,
            "--json",
          )) as {
            id?: number;
            current_version_id?: string;
            source_metadata?: {
              contract_version?: string;
              fields?: {
                key: string;
                namespace?: string;
                source_field?: string;
                sensitive?: boolean;
                value?: unknown;
              }[];
              warnings?: unknown[];
            };
          };
          const metadata = node.source_metadata;
          return {
            id: node.id,
            current_version_id: node.current_version_id,
            polled_version_id: version[name],
            version_match: node.current_version_id === version[name],
            source_metadata: metadata
              ? {
                  contract_version: metadata.contract_version,
                  fields: (metadata.fields ?? []).slice(0, 80).map((field) => ({
                    key: field.key,
                    namespace: field.namespace,
                    source_field: field.source_field,
                    sensitive: field.sensitive ?? false,
                    value: field.sensitive ? "[sensitive]" : clip(field.value),
                  })),
                  warnings: (metadata.warnings ?? []).slice(0, 40).map(clip),
                }
              : null,
          };
        });
        const jobs = await probe(async () => {
          const listing = (await cli("jobs", "--json")) as { items?: unknown[] };
          return (listing.items ?? [])
            .filter((item) => /source-metadata|document-events/u.test(JSON.stringify(item)))
            .slice(0, 20);
        });
        const log = await probe(async () => {
          const dir = path.join(vault, "logs");
          const files = (await readdir(dir)).filter((file) => file.startsWith("docbank")).sort();
          const lines: string[] = [];
          for (const file of files.slice(-2))
            for (const line of (await readFile(path.join(dir, file), "utf8")).split("\n"))
              if (
                /retry|retrying|source.?metadata|document.?events|level=(warn|error)|"level":"(WARN|ERROR)"/iu.test(
                  line,
                )
              )
                lines.push(line.slice(0, 500));
          return { files, lines: lines.slice(-60) };
        });
        await testInfo.attach(`document-facts-${proof}-${name}-${resource}-readiness.json`, {
          body: JSON.stringify({ name, resource, stat, jobs, log }, null, 2),
          contentType: "application/json",
        });
      };
      const claim = (facts: Facts, kind: string, when: Record<string, unknown>) =>
        facts.claims?.some(
          (item) =>
            item.date_kind === kind &&
            Object.entries(when).every(([key, value]) => item.when[key] === value),
        ) ?? false;
      const edge = (facts: Facts, role: string, name: string) =>
        facts.edges?.some((item) => item.role === role && item.display_name === name) ?? false;
      await expectFacts("board-update.eml", "dates", (facts) =>
        claim(facts, "sent", { state: "exact", at: "2026-03-11T03:40:00Z" }),
      );
      await expectFacts(
        "board-update.eml",
        "people",
        (facts) =>
          edge(facts, "sender", "Mara Okonkwo-Fell") &&
          edge(facts, "recipient", "Ines Albarran") &&
          edge(facts, "copied", "Teodor Vask") &&
          !JSON.stringify(facts).includes("Private Reviewer"),
      );
      await expectFacts("board-minutes.pdf", "dates", (facts) =>
        claim(facts, "created", { state: "floating", local: "2026-03-10T23:30:00" }),
      );
      await expectFacts(
        "board-minutes.pdf",
        "people",
        (facts) =>
          facts.state === "published" &&
          (facts.assertions?.some(
            (item) => item.display_name === "Jory Pellham" && item.action === "assert",
          ) ??
            false) &&
          (facts.unresolved?.some((item) => item.raw_label === "L. Beerwijk") ?? false),
      );
      await expectFacts("board-offsite.ics", "dates", (facts) =>
        claim(facts, "started", { state: "date", date: "2026-03-09" }),
      );

      // Privacy, proved on an email that has a Bcc. Authenticated non-browser
      // reads (the CLI's daemonconn.Ensure client with the runtime record's
      // API key) of the exact version's full and safe dates are bracketed by
      // reads of the retained event head taken while the daemon is stopped.
      // The closure must hold: the same head before and after, full showing
      // exactly the retained events, safe exactly those without sensitive
      // evidence, and the sent event carrying a sensitive blind_copy actor from
      // the same metadata generation. Nothing here carries credentials.
      const privatePath = "/private/reviewer-note.eml";
      const privateNode = JSON.parse(await run("document", "stat", privatePath, "--json")) as {
        current_version_id: string;
      };
      const privateVersion = privateNode.current_version_id;
      type CliDates = {
        content_version_id: string;
        state: string;
        claims: {
          event_id: string;
          evidence_id: string;
          date_kind: string;
          when: { at?: string };
        }[];
      };
      const datesOf = async (full: boolean) =>
        JSON.parse(
          await run("document", "dates", privatePath, "--json", ...(full ? ["--full"] : [])),
        ) as CliDates;
      const sentIn = (dates: CliDates) =>
        dates.claims.find((item) => item.date_kind === "sent" && item.when.at === SENT_AT);
      let waited: CliDates | undefined;
      try {
        await expect
          .poll(
            async () => {
              waited = await datesOf(true);
              return waited.state === "published" && !!sentIn(waited);
            },
            { timeout: 60_000, intervals: [1_000] },
          )
          .toBe(true);
      } catch (error) {
        throw new Error(`${privatePath} full dates never had sent: ${JSON.stringify(waited)}`, {
          cause: error,
        });
      }
      type Head = {
        version_id: string;
        events: {
          event_id: string;
          evidence_id: string;
          date_kind: string;
          utc_key: string;
          safe: boolean;
          actors: { role: string; sensitive: boolean; evidence_id: string }[];
        }[];
      } & Record<string, unknown>;
      const restart = async () => {
        started = true;
        issued = new URL(await run("web", "--no-browser"));
        session = new URLSearchParams(issued.hash.slice(1)).get("web_session") ?? "";
      };
      await stop();
      const before = JSON.parse(await fixture("head", vault, privateVersion)) as Head;
      await restart();
      const full = await datesOf(true);
      const safe = await datesOf(false);
      const fullAgain = await datesOf(true);
      await stop();
      const after = JSON.parse(await fixture("head", vault, privateVersion)) as Head;
      await restart();
      const witness = { version: privateVersion, before, full, safe, fullAgain, after };
      await testInfo.attach("document-facts-branch-privacy-witness.json", {
        body: JSON.stringify(witness, null, 2),
        contentType: "application/json",
      });
      const identities = (items: { event_id: string; evidence_id: string }[]) =>
        items.map((item) => `${item.event_id}/${item.evidence_id}`).sort();
      // Everything in a head except its events: the generation, checksum,
      // inputs digest, epochs, publication time and metadata sources.
      const fence = (head: Head) =>
        JSON.stringify(Object.entries(head).filter(([key]) => key !== "events"));
      const retainedSent = before.events.find(
        (item) => item.date_kind === "sent" && item.utc_key === SENT_AT,
      );
      const closure = {
        exactVersion: [
          before.version_id,
          after.version_id,
          full.content_version_id,
          safe.content_version_id,
          fullAgain.content_version_id,
        ].every((id) => id === privateVersion),
        sameHead:
          fence(before) === fence(after) &&
          JSON.stringify(identities(before.events)) === JSON.stringify(identities(after.events)),
        published: [full, safe, fullAgain].every((item) => item.state === "published"),
        fullIsRetained:
          JSON.stringify(identities(full.claims)) === JSON.stringify(identities(before.events)) &&
          JSON.stringify(identities(fullAgain.claims)) ===
            JSON.stringify(identities(before.events)),
        safeIsRetainedSafe:
          JSON.stringify(identities(safe.claims)) ===
          JSON.stringify(identities(before.events.filter((item) => item.safe))),
        sentRetainedWithBcc:
          !!retainedSent &&
          !retainedSent.safe &&
          retainedSent.actors.some(
            (actor) =>
              actor.role === "blind_copy" &&
              actor.sensitive &&
              actor.evidence_id === retainedSent.evidence_id,
          ),
        sentFullOnly:
          !!retainedSent &&
          sentIn(full)?.event_id === retainedSent.event_id &&
          !safe.claims.some((item) => item.event_id === retainedSent.event_id),
      };
      if (!Object.values(closure).every(Boolean))
        throw new Error(`privacy closure failed: ${JSON.stringify(closure)}`);
      // The browser session's own safe payloads for that version: people with
      // the sender and no Bcc name or address, dates published without sent.
      version["reviewer-note.eml"] = privateVersion;
      await expectFacts(
        "reviewer-note.eml",
        "people",
        (facts) =>
          edge(facts, "sender", "Mara Okonkwo-Fell") &&
          !JSON.stringify(facts).includes("Private Reviewer") &&
          !JSON.stringify(facts).includes(hiddenAddress),
      );
      await expectFacts(
        "reviewer-note.eml",
        "dates",
        (facts) => facts.state === "published" && !claim(facts, "sent", { at: SENT_AT }),
      );
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

    // Exact email time, configured zone, From/To/Cc chips and no Bcc in the facts.
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
      // Safe facts never carry the Bcc recipient. The existing raw email viewer
      // legitimately shows the Bcc header, so check the facts surfaces only.
      for (const surface of [people, dates, liveRow("board-update.eml")])
        await expect(surface.getByText("Private Reviewer")).toHaveCount(0);
      await expect(page.getByRole("img", { name: "Private Reviewer" })).toHaveCount(0);
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

    if (proof === "branch") {
      // The Bcc email's safe surfaces: the sent date is absent and the Bcc
      // recipient's name and address appear nowhere in the inspector's facts
      // or the table, while From and To still show. The raw email viewer is a
      // separate, owner-only view of the message and is not checked here.
      await browse("private");
      await liveRow("reviewer-note.eml").click();
      const dates = inspector.getByRole("region", { name: "Dates" });
      const people = inspector.getByRole("region", { name: "People" });
      await expect(dates.getByText("Times show in America/Toronto.")).toBeVisible();
      await expect(people.getByRole("button", { name: "Mara Okonkwo-Fell" })).toBeVisible();
      await expect(dates.getByText(EMAIL_SENT)).toHaveCount(0);
      for (const surface of [people, dates, liveRow("reviewer-note.eml")])
        for (const text of ["Private Reviewer", hiddenAddress])
          await expect(surface.getByText(text)).toHaveCount(0);
      await expect(page.getByRole("img", { name: "Private Reviewer" })).toHaveCount(0);
      await browse("board");
    }

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
      // A held dates request (an HTTP transport state, not a server
      // derivation state) shows its spinner while people are already shown.
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
      await expect(
        inspector
          .getByRole("region", { name: "People" })
          .getByRole("button", { name: "Mara Okonkwo-Fell" }),
      ).toBeVisible();
      await capture(page, testInfo, "dates-loading-wide", "light");
      release();
      await expect(inspector.getByText(EMAIL_SENT)).toBeVisible();
      await page.unroute(datesOf);

      // One aborted HTTP request (a transport failure, not a failed derivation)
      // leaves the other resource shown; Retry reads only that one.
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
  } finally {
    try {
      await stop();
    } finally {
      await rm(scratch, { recursive: true, force: true });
    }
  }
});
