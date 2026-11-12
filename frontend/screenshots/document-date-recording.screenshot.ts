import { expect, test, type CDPSession, type Locator, type Response } from "@playwright/test";
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

// Branch-only screen recording of the Document date sort and saved-search
// sequence. It shares the synthetic fixtures and request matchers of
// document-date-query.screenshot.ts but writes no paired captures, so the
// 42-image proof stays separate. The runner encodes the frames.
const exec = promisify(execFile);
const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const output = process.env.DOCBANK_DATE_RECORDING_DIR;
test.skip(!output, "DOCBANK_DATE_RECORDING_DIR enables this proof recording");

const viewport = { width: 1440, height: 960 };
const day = "date:2026-03-10";
const savedName = "Synthetic March 10";
// Recording cadence from screenshot.md, not synchronization: every wait for
// application state below is an assertion.
const resultHold = 2_000;
const stepHold = 1_200;
const glideSteps = 30;
const glideStepDelay = 20;
const aimPause = 250;
// The arrow's tip sits at this offset inside its SVG.
const tip = { x: 1, y: 1 };
const arrow =
  '<svg xmlns="http://www.w3.org/2000/svg" width="22" height="32" viewBox="0 0 22 32">' +
  '<path d="M1 1 L1 25 L7 19.2 L11 29 L15 27.4 L11 17.8 L19 17.8 Z" fill="#000" ' +
  'stroke="#fff" stroke-width="1.6" stroke-linejoin="round"/></svg>';
type Sort = { field: string; direction: "asc" | "desc" };
type Box = { x: number; y: number; width: number; height: number };
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
// Byte-identical to the fixtures of document-date-query.screenshot.ts.
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

// Width and height from a baseline or progressive JPEG start-of-frame marker.
function jpegSize(bytes: Buffer): { width: number; height: number } {
  let offset = 2;
  while (offset + 9 < bytes.length) {
    if (bytes[offset] !== 0xff) throw new Error("Screencast frame is not a JPEG");
    const marker = bytes[offset + 1];
    const length = bytes.readUInt16BE(offset + 2);
    if (marker >= 0xc0 && marker <= 0xc3) {
      return { height: bytes.readUInt16BE(offset + 5), width: bytes.readUInt16BE(offset + 7) };
    }
    offset += 2 + length;
  }
  throw new Error("Screencast frame has no JPEG size");
}

const intersects = (a: Box, b: Box) =>
  a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;
const now = () => Date.now() / 1000;

test("records the document-date sort and saved-search sequence", async ({ page }) => {
  test.setTimeout(300_000);
  if (process.env.DOCBANK_DATE_QUERY_TREE !== "branch") {
    throw new Error("The recording runs only on the branch tree");
  }
  const binary = process.env.DOCBANK_SCREENSHOT_BINARY;
  if (!binary || !path.isAbsolute(binary)) {
    throw new Error("DOCBANK_SCREENSHOT_BINARY must name an absolute binary");
  }
  await access(binary);
  const scratch = await mkdtemp(path.join(tmpdir(), "docbank-date-recording-"));
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
  const recording = {
    tree: "branch",
    viewport,
    device_pixel_ratio: 0,
    theme: "dark",
    format: "jpeg",
    quality: 90,
    frame_size: null as { width: number; height: number } | null,
    pointer: { kind: "macos-arrow-svg", tip, svg: arrow },
    started_at: 0,
    ended_at: 0,
    dark_check: null as unknown,
    frames: [] as { file: string; timestamp: number; bytes: number; sha256: string }[],
    captions: [] as {
      state: string;
      text: string;
      hold: "result" | "step";
      start: number;
      end: number;
      box: Box;
    }[],
    actions: [] as {
      state: string;
      target: string;
      x: number;
      y: number;
      from: { x: number; y: number };
      glide_start: number;
      glide_end: number;
      steps: number;
      tip: { x: number; y: number };
      clicked_at: number;
    }[],
    facts: {
      fixtures: [] as unknown[],
      states: [] as unknown[],
      sort: {} as Record<string, unknown>,
      saved: {} as Record<string, unknown>,
      run: null as unknown,
    },
    // Each flag is true only after that step actually succeeded.
    cdp_cleanup: {
      stop_attempted: false,
      stopped: false,
      detach_attempted: false,
      detached: false,
      errors: [] as string[],
    },
    frames_drained: false,
    frame_error: undefined as string | undefined,
    daemon_started: false,
    cleanup: null as { daemon_pid: number | null; daemon_running_after_stop: false } | null,
    scratch_removed: false,
    // Set when the daemon was not verified stopped: its vault stays for the
    // runner's recovery stop (DOCBANK_HOME=<path>), and the run still fails.
    retained_vault: null as string | null,
    error: undefined as string | undefined,
    cleanup_errors: [] as string[],
  };
  let session = "";
  let started = false;
  let daemonStopAttempted = false;
  let failure: unknown;
  let cdp: CDPSession | undefined;
  let accepting = false;
  let frameError: unknown;
  const frameWrites: Promise<void>[] = [];
  const redact = (value: unknown) =>
    session ? inspect(value).replaceAll(session, "[redacted]") : inspect(value);
  // Each cleanup runs at most once; a later attempt never repeats or
  // replaces the first outcome.
  const stop = async () => {
    if (!started || daemonStopAttempted) return;
    daemonStopAttempted = true;
    const before = JSON.parse(await run("daemon", "status", "--json"));
    if (before.running) await run("daemon", "stop");
    const after = JSON.parse(await run("daemon", "status", "--json"));
    if (after.running !== false) {
      throw new Error(`Synthetic daemon ${before.pid} is still running`);
    }
    recording.cleanup = { daemon_pid: before.pid ?? null, daemon_running_after_stop: false };
  };
  // Stopping and detaching are separate attempts, each recorded truthfully.
  const stopScreencast = async () => {
    const screencast = cdp;
    const receipt = recording.cdp_cleanup;
    if (!screencast || receipt.stop_attempted) return;
    receipt.stop_attempted = true;
    try {
      await screencast.send("Page.stopScreencast");
      receipt.stopped = true;
    } catch (cause) {
      receipt.errors.push(`stopScreencast: ${redact(cause)}`);
    }
    accepting = false;
    receipt.detach_attempted = true;
    try {
      await screencast.detach();
      receipt.detached = true;
    } catch (cause) {
      receipt.errors.push(`detach: ${redact(cause)}`);
    }
    if (!receipt.stopped || !receipt.detached) {
      throw new Error(`Screencast cleanup failed: ${receipt.errors.join("; ")}`);
    }
  };

  const editor = page.getByRole("dialog", { name: "Filters and sort" });
  const expression = editor.getByLabel("Query syntax", { exact: true });
  const checking = editor.getByRole("status", { name: "Checking query" });
  const valid = editor.getByRole("status", { name: "Query valid" });
  const sortBy = editor.getByRole("combobox", { name: /^Sort by: / });
  const direction = editor.getByRole("combobox", { name: /^Direction: / });
  const listbox = editor.getByRole("listbox");
  const drawer = page.getByRole("dialog", { name: "Searches" });
  const definitionEditor = drawer.getByRole("region", { name: "Definition editor" });
  const queryJSON = drawer.getByRole("textbox", { name: "Complete query JSON" });
  const searchbox = page.getByRole("banner").getByRole("searchbox", { name: "Search documents" });
  const results = page.getByRole("region", { name: "Frozen query results" });
  const rows = page.locator("table tr[data-snapshot-node]");
  const captionBox = page.locator("#recording-caption");
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
    recording.facts.states.push(result);
    return result;
  };

  // Recording overlays: aria-hidden and pointer-events:none, so hit testing,
  // roles and the app ignore them. Hidden until the recording starts.
  // The arrow starts over the empty part of the results table.
  const pointer = { x: 720, y: 700 };
  const startRecording = async () => {
    const theme = await page.evaluate(() => {
      // The first opaque page background, body before html.
      const painted = [document.body, document.documentElement]
        .map((element) => getComputedStyle(element).backgroundColor)
        .map((color) => color.match(/[\d.]+/g)!.map(Number))
        .find((channels) => channels.length === 3 || channels[3] === 1);
      return {
        class_dark: document.documentElement.classList.contains("dark"),
        stored: localStorage.getItem("docbank-theme"),
        background: painted?.slice(0, 3) ?? null,
      };
    });
    recording.dark_check = theme;
    expect(theme.class_dark).toBe(true);
    expect(theme.stored).toBe("dark");
    expect(theme.background).not.toBeNull();
    for (const channel of theme.background!) expect(channel).toBeLessThan(80);
    await page.mouse.move(pointer.x, pointer.y);
    await page.evaluate(() => {
      document.getElementById("recording-pointer")!.style.display = "block";
    });
    const screencast = await page.context().newCDPSession(page);
    cdp = screencast;
    screencast.on("Page.screencastFrame", (frame) => {
      if (!accepting) return;
      const bytes = Buffer.from(frame.data, "base64");
      const index = recording.frames.length;
      const file = `frames/${String(index).padStart(6, "0")}.jpg`;
      try {
        if (typeof frame.metadata.timestamp !== "number") {
          throw new Error(`Screencast frame ${index} has no timestamp`);
        }
        const size = jpegSize(bytes);
        recording.frame_size ??= size;
        if (size.width !== recording.frame_size.width || size.height !== recording.frame_size.height)
          throw new Error(`Screencast frame ${index} changed size`);
        recording.frames.push({
          file,
          timestamp: frame.metadata.timestamp,
          bytes: bytes.length,
          sha256: sha256(bytes),
        });
        frameWrites.push(
          writeFile(path.join(output!, file), bytes).catch((cause) => {
            frameError ??= cause;
          }),
        );
      } catch (cause) {
        frameError ??= cause;
      }
      // An unacknowledged frame stalls the screencast, so a failed
      // acknowledgement while recording fails the recording.
      void screencast
        .send("Page.screencastFrameAck", { sessionId: frame.sessionId })
        .catch((cause) => {
          if (accepting) frameError ??= cause;
        });
    });
    accepting = true;
    recording.started_at = now();
    await screencast.send("Page.startScreencast", {
      format: "jpeg",
      quality: recording.quality,
      maxWidth: viewport.width * recording.device_pixel_ratio,
      maxHeight: viewport.height * recording.device_pixel_ratio,
      everyNthFrame: 1,
    });
  };
  const caption = async (state: string, text: string, hold: "result" | "step") => {
    const banner = (await page.getByRole("banner").boundingBox())!;
    await page.evaluate(
      ([value, top]) => {
        const element = document.getElementById("recording-caption")!;
        element.textContent = value;
        element.style.top = `${top}px`;
        element.style.display = "block";
      },
      [text, banner.y + banner.height + 16] as const,
    );
    const box = (await captionBox.boundingBox())!;
    // The caption never covers the state it names.
    for (const subject of [editor, listbox, drawer, results, searchbox]) {
      if ((await subject.count()) === 0 || !(await subject.first().isVisible())) continue;
      const covered = await subject.first().boundingBox();
      if (covered && intersects(box, covered)) {
        throw new Error(`Caption "${text}" covers ${subject}`);
      }
    }
    // The first caption is on screen before the first frame.
    if (!cdp) await startRecording();
    const start = recording.captions.length === 0 ? recording.started_at : now();
    const previous = recording.captions.at(-1);
    if (previous) previous.end = start;
    recording.captions.push({ state, text, hold, start, end: 0, box });
    await expect.poll(() => recording.frames.length).toBeGreaterThan(0);
    await page.waitForTimeout(hold === "result" ? resultHold : stepHold);
  };
  const glide = async (x: number, y: number) => {
    const from = { ...pointer };
    for (let step = 1; step <= glideSteps; step++) {
      const t = step / glideSteps;
      const eased = t < 0.5 ? 2 * t * t : 1 - (-2 * t + 2) ** 2 / 2;
      await page.mouse.move(from.x + (x - from.x) * eased, from.y + (y - from.y) * eased);
      await page.waitForTimeout(glideStepDelay);
    }
    pointer.x = x;
    pointer.y = y;
    return from;
  };
  const centre = async (target: Locator) => {
    await expect(target).toBeVisible();
    await target.scrollIntoViewIfNeeded();
    const box = (await target.boundingBox())!;
    return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
  };
  // The arrow glides to the target's centre, the tip must be on the element
  // there, and the click lands at the tip.
  const glideClick = async (state: string, target: Locator, label: string) => {
    const { x, y } = await centre(target);
    const glideStart = now();
    const from = await glide(x, y);
    const glideEnd = now();
    const hit = await target.evaluate(
      (element, [px, py]) => {
        const at = document.elementFromPoint(px, py);
        return at !== null && (element === at || element.contains(at));
      },
      [x, y] as const,
    );
    expect(hit, `${label} is under the pointer`).toBe(true);
    const at = await page.evaluate(() => {
      const box = document.getElementById("recording-pointer")!.getBoundingClientRect();
      return { left: box.left, top: box.top };
    });
    const tipAt = { x: at.left + tip.x, y: at.top + tip.y };
    expect(Math.abs(tipAt.x - x)).toBeLessThan(0.5);
    expect(Math.abs(tipAt.y - y)).toBeLessThan(0.5);
    await page.waitForTimeout(aimPause);
    const clickedAt = now();
    await page.mouse.click(x, y);
    recording.actions.push({
      state,
      target: label,
      x,
      y,
      from,
      glide_start: glideStart,
      glide_end: glideEnd,
      steps: glideSteps,
      tip: tipAt,
      clicked_at: clickedAt,
    });
  };
  const settledPreview = async () => {
    await expect(checking).toHaveCount(0);
    await expect(valid).toBeAttached();
    await expect(editor.getByRole("alert")).toHaveCount(0);
  };
  // The JSON editor's line holding needle lies inside its visible area.
  const lineVisible = (needle: string) =>
    queryJSON.evaluate((element, value) => {
      const area = element as HTMLTextAreaElement;
      const style = getComputedStyle(area);
      const size = Number.parseFloat(style.fontSize);
      const parsed = Number.parseFloat(style.lineHeight);
      const line = Number.isFinite(parsed) ? parsed : size * 1.2;
      const index = area.value.split("\n").findIndex((text) => text.includes(value));
      const top = Number.parseFloat(style.paddingTop) + index * line - area.scrollTop;
      return {
        index,
        line_height: line,
        scroll_top: area.scrollTop,
        client_height: area.clientHeight,
        visible: index >= 0 && top >= 0 && top + line <= area.clientHeight,
      };
    }, needle);
  const atBottom = () =>
    queryJSON.evaluate((element) => {
      const area = element as HTMLTextAreaElement;
      return area.scrollTop + area.clientHeight >= area.scrollHeight - 1;
    });
  // Scroll the JSON editor to its end with the wheel, as a person would. A
  // wheel at the end would scroll the drawer instead, so it is skipped there.
  const scrollJSON = async () => {
    const { x, y } = await centre(queryJSON);
    await glide(x, y);
    if (!(await atBottom())) await page.mouse.wheel(0, 2_000);
    await expect.poll(atBottom).toBe(true);
    await expect(queryJSON).toBeInViewport({ ratio: 1 });
  };
  const showsLines = async (...needles: string[]) => {
    for (const needle of needles) {
      const line = await lineVisible(needle);
      expect(line.visible, `JSON line ${needle} is visible`).toBe(true);
    }
  };

  try {
    await mkdir(path.join(output!, "frames"), { recursive: true, mode: 0o700 });
    await mkdir(vault, { mode: 0o700 });
    const sources: string[] = [];
    for (const fixture of fixtures) {
      const source = path.join(scratch, fixture.name);
      await writeFile(source, fixture.bytes, { mode: 0o600 });
      sources.push(source);
    }
    started = true; // add may start the daemon even if the command fails.
    recording.daemon_started = true;
    await run("add", ...sources, "--dest", "/");
    const nodes: { name: string; node_id: number; matches: boolean }[] = [];
    for (const fixture of fixtures) {
      const node = JSON.parse(await run("stat", `/${fixture.name}`, "--json"));
      expect(node.blob_hash).toBe(sha256(fixture.bytes));
      if (fixture.name.endsWith(".eml")) expect(node.mime_type).toBe("message/rfc822");
      // Readiness and the asserted primary come from one response.
      const utcKey = "instant" in fixture ? `${fixture.instant}.000000000Z` : undefined;
      let read: { state: string; claims: DateClaim[] } = { state: "", claims: [] };
      await expect
        .poll(
          async () => {
            read = await dates(fixture.name);
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
      nodes.push({ name: fixture.name, node_id: node.id, matches: fixture.matches });
      recording.facts.fixtures.push({
        name: fixture.name,
        node_id: node.id,
        sha256: node.blob_hash,
        primary: read.claims.find((claim) => claim.primary) ?? null,
      });
    }
    const nodeID = (name: string) => nodes.find((node) => node.name === name)!.node_id;
    const ascending = [nodeID("evening-offset.eml"), nodeID("morning-utc.eml")];

    // The app reads the stored theme before it renders, so it is dark from
    // its first paint; the recording starts only after that is checked.
    await page.addInitScript(
      ({ arrowSVG }) => {
        localStorage.setItem("docbank-theme", "dark");
        document.addEventListener("DOMContentLoaded", () => {
          const pointer = document.createElement("div");
          pointer.id = "recording-pointer";
          pointer.setAttribute("aria-hidden", "true");
          pointer.innerHTML = arrowSVG;
          pointer.style.cssText =
            "position:fixed;left:0;top:0;width:22px;height:32px;display:none;" +
            "pointer-events:none;z-index:2147483647;" +
            "filter:drop-shadow(0 1px 1.5px rgba(0,0,0,.45))";
          const caption = document.createElement("div");
          caption.id = "recording-caption";
          caption.setAttribute("aria-hidden", "true");
          caption.style.cssText =
            "position:fixed;left:24px;top:72px;max-width:400px;display:none;" +
            "pointer-events:none;z-index:2147483646;box-sizing:border-box;" +
            "padding:12px 18px;border-radius:10px;background:rgba(17,17,20,.94);" +
            "border:1px solid rgba(255,255,255,.2);color:#fff;" +
            "font:600 24px/1.25 system-ui,-apple-system,sans-serif";
          document.body.append(pointer, caption);
          window.addEventListener(
            "mousemove",
            (event) => {
              pointer.style.left = `${event.clientX - 1}px`;
              pointer.style.top = `${event.clientY - 1}px`;
            },
            { capture: true, passive: true },
          );
        });
      },
      { arrowSVG: arrow },
    );
    const issued = new URL(await run("web", "--no-browser"));
    session = new URLSearchParams(issued.hash.slice(1)).get("web_session") ?? "";
    if (!session) throw new Error("Synthetic web session missing");
    try {
      await page.goto(issued.toString());
    } catch (cause) {
      const detail = String(cause).replaceAll(session, "[redacted]");
      throw new Error("Could not open the synthetic browser session", { cause: new Error(detail) });
    }
    recording.device_pixel_ratio = await page.evaluate(() => devicePixelRatio);
    expect(recording.device_pixel_ratio).toBe(2);
    expect(page.viewportSize()).toEqual(viewport);

    // Unrecorded setup: the proof's existing flow to the first date results.
    await page.getByRole("button", { name: "Imports", exact: true }).click();
    const imports = page.getByRole("main", { name: "Imports" });
    await imports
      .getByRole("button", { name: /Browse import/ })
      .first()
      .click();
    await imports.getByRole("button", { name: "Search this import" }).click();
    await expect(valid).toBeAttached();
    await page.keyboard.press("Escape");
    await expect(editor).toBeHidden();
    await page.getByRole("combobox", { name: /^Search syntax/ }).click();
    await page.getByRole("option", { name: "Query syntax", exact: true }).click();
    await page.getByRole("button", { name: "Advanced", exact: true }).click();
    await expect(expression).toBeVisible();
    const previewing = posted(getParseQueryUrl(), day);
    await expression.fill(day);
    const preview = await record("setup-preview", await previewing);
    expect(preview.status).toBe(200);
    expect(Object.hasOwn((preview.body as QueryPreview).query, "timezone")).toBe(false);
    await settledPreview();
    const first = posted(getCreateWorkspaceQueryUrl(), day);
    await editor.getByRole("button", { name: "Search", exact: true }).click();
    const firstResults = await record("setup-results", await first);
    expect(firstResults.status).toBe(200);
    expect((firstResults.body as SnapshotPage).timezone).toBe("UTC");
    expect((firstResults.body as SnapshotPage).timezone_defaulted).toBe(true);
    await expect(editor).toBeHidden();
    await expect(rows).toHaveCount(ascending.length);
    await expect(searchbox).toHaveValue(day);
    await expect(page.getByText("2 results", { exact: true })).toBeVisible();

    // Searches from the reopened dialog. A successful search clears the
    // dialog's definition flag, so the banner holds the operative query.
    const search = async (state: string, sort: Sort) => {
      const searching = posted(getCreateWorkspaceQueryUrl(), day, sort);
      await glideClick(state, editor.getByRole("button", { name: "Search", exact: true }), "Search");
      const response = await searching;
      const submitted = response.request().postDataJSON();
      expect(submitted.query.sort).toEqual(sort);
      expect(Object.hasOwn(submitted.query, "timezone")).toBe(false);
      const { status, body } = await record(state, response);
      expect(status).toBe(200);
      const snapshot = body as SnapshotPage;
      expect(snapshot.query.text).toBe(day);
      expect(snapshot.query.sort).toEqual(sort);
      expect(snapshot.timezone).toBe("UTC");
      expect(snapshot.timezone_defaulted).toBe(true);
      const order = snapshot.rows.map((row) => row.node_id);
      expect(order).toEqual(sort.direction === "asc" ? ascending : ascending.toReversed());
      await expect(editor).toBeHidden();
      await expect(results).toHaveAttribute("data-snapshot-id", snapshot.snapshot_id);
      await expect(rows).toHaveCount(order.length);
      expect(
        await rows.evaluateAll((elements) =>
          elements.map((row) => Number(row.getAttribute("data-snapshot-node"))),
        ),
      ).toEqual(order);
      return { status, request_sort: submitted.query.sort as Sort, rows: order };
    };
    const reopen = async (state: string) => {
      await glideClick(state, page.getByRole("button", { name: "Advanced", exact: true }), "Advanced");
      await expect(editor).toBeVisible();
      await expect(searchbox).toHaveValue(day);
      await settledPreview();
    };
    const choose = async (state: string, combobox: Locator, label: string, sort: Sort) => {
      await glideClick(state, combobox, (await combobox.getAttribute("aria-label")) ?? "combobox");
      await expect(listbox).toBeVisible();
      const previewing = posted(getParseQueryUrl(), day, sort);
      return {
        listed: async (captionText: string) => {
          await caption(`${state}-options`, captionText, "step");
          await glideClick(state, listbox.getByRole("option", { name: label, exact: true }), label);
          const { status, body } = await record(`${state}-preview`, await previewing);
          expect(status).toBe(200);
          expect((body as QueryPreview).query.sort).toEqual(sort);
          expect(Object.hasOwn((body as QueryPreview).query, "timezone")).toBe(false);
          await expect(listbox).toBeHidden();
          await settledPreview();
          return { status, sort: (body as QueryPreview).query.sort as Sort };
        },
      };
    };

    await test.step("first results", async () => {
      await caption("results", "date:2026-03-10 finds 2 results", "result");
    });

    await test.step("sort by document date, ascending", async () => {
      const sort: Sort = { field: "document_date", direction: "asc" };
      await reopen("sort-ascending");
      await expect(sortBy).toHaveAttribute("aria-label", "Sort by: Name");
      await expect(direction).toHaveAttribute("aria-label", "Direction: Ascending");
      await caption("filters-name", "Filters and sort: Sort by Name, Ascending", "step");
      const chosen = await choose("sort-ascending", sortBy, "Document date", sort);
      await expect(listbox.getByRole("option")).toHaveText([
        "Name",
        "Path",
        "Modified",
        "Document date",
        "Size",
        "File type",
      ]);
      const preview = await chosen.listed("Sort by lists Document date");
      await expect(sortBy).toHaveAttribute("aria-label", "Sort by: Document date");
      await caption("sort-ascending", "Sort by Document date, Ascending", "step");
      const submitted = await search("sort-ascending-results", sort);
      recording.facts.sort.ascending = { preview, results: submitted };
      await caption(
        "sort-ascending-results",
        "Ascending: evening-offset.eml, then morning-utc.eml",
        "result",
      );
    });

    await test.step("sort by document date, descending", async () => {
      const sort: Sort = { field: "document_date", direction: "desc" };
      await reopen("sort-descending");
      await expect(sortBy).toHaveAttribute("aria-label", "Sort by: Document date");
      await expect(direction).toHaveAttribute("aria-label", "Direction: Ascending");
      await caption("filters-ascending", "Filters and sort: Document date, Ascending", "step");
      const chosen = await choose("sort-descending", direction, "Descending", sort);
      const preview = await chosen.listed("Direction lists Ascending and Descending");
      await expect(direction).toHaveAttribute("aria-label", "Direction: Descending");
      await caption("sort-descending", "Sort by Document date, Descending", "step");
      const submitted = await search("sort-descending-results", sort);
      recording.facts.sort.descending = { preview, results: submitted };
      await caption(
        "sort-descending-results",
        "Descending: morning-utc.eml, then evening-offset.eml",
        "result",
      );
    });

    // Saves through the drawer's own create/update functions, which check each
    // receipt; a rejected receipt shows an alert and disables both save buttons.
    let stored: { id: string; created_at: string; payload: Record<string, unknown> };
    const listed = () =>
      page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === getListSavedQueriesUrl() &&
          response.request().method() === "GET",
      );
    const editorJSON = async () => JSON.parse(await queryJSON.inputValue());
    const replaceJSON = async (state: string, value: unknown) => {
      await glideClick(state, queryJSON, "Complete query JSON");
      await page.keyboard.press("ControlOrMeta+A");
      await page.keyboard.insertText(JSON.stringify(value, null, 2));
      await scrollJSON();
    };
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
      await glideClick(
        state,
        definitionEditor.getByRole("button", { name: button, exact: true }),
        button,
      );
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
      await expect(drawer.getByText("Loading…")).toHaveCount(0);
      await expect(drawer.getByRole("alert")).toHaveCount(0);
      await expect(definitionEditor.getByRole("heading", { level: 3 })).toHaveText(
        `Edit ${savedName}`,
      );
      await expect(
        definitionEditor.getByRole("button", { name: "Save changes", exact: true }),
      ).toBeEnabled();
      await scrollJSON();
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
      await reopen("saved-create");
      await expect(direction).toHaveAttribute("aria-label", "Direction: Descending");
      await caption("filters-descending", "Filters and sort: Document date, Descending", "step");
      await glideClick("saved-create", editor.getByRole("button", { name: "Save…", exact: true }), "Save…");
      await expect(editor).toBeHidden();
      await expect(drawer).toBeVisible();
      await expect(drawer.getByText("Loading…")).toHaveCount(0);
      await expect(drawer.getByText(/^No searches yet\./)).toBeVisible();
      await expect(definitionEditor.getByRole("heading", { level: 3 })).toHaveText("New search");
      await caption("new-search", "Searches: New search", "step");
      await glideClick(
        "saved-create",
        definitionEditor.getByText("Edit as JSON", { exact: true }),
        "Edit as JSON",
      );
      await expect(queryJSON).toBeVisible();
      const draft = await editorJSON();
      expect(draft.text).toBe(day);
      expect(draft.sort).toEqual({ field: "document_date", direction: "desc" });
      expect(Object.hasOwn(draft, "timezone")).toBe(false);
      await scrollJSON();
      await showsLines('"document_date"', '"desc"');
      await caption("draft-json", 'JSON: "document_date", "desc", no "timezone" line', "step");
      await glideClick(
        "saved-create",
        definitionEditor.getByRole("textbox", { name: "Definition name" }),
        "Definition name",
      );
      await page.keyboard.type(savedName, { delay: 60 });
      await expect(definitionEditor.getByRole("textbox", { name: "Definition name" })).toHaveValue(
        savedName,
      );
      await caption("named", `Name: ${savedName}`, "step");
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
      // The server pins its default zone into a date query saved without one.
      expect(body.payload.timezone).toBe("UTC");
      expect((await editorJSON()).timezone).toBe("UTC");
      await showsLines('"timezone": "UTC"');
      stored = body;
      recording.facts.saved.id = body.id;
      recording.facts.saved.create = summary;
      await caption(
        "saved-create",
        `Saved ${savedName}. The JSON now shows "timezone": "UTC"`,
        "result",
      );
    });

    await test.step("change the saved search", async () => {
      const value = await editorJSON();
      value.sort.direction = "asc";
      await replaceJSON("saved-update", value);
      await showsLines('"direction": "asc"');
      await caption("edited", 'JSON edited: "direction": "asc"', "step");
      const { sent, body, summary } = await mutation(
        "saved-update",
        "Save changes",
        getUpdateSavedQueryUrl(stored.id),
        "PATCH",
        200,
        2,
      );
      expect(summary.request.if_match).toBe('"1"');
      expect(sent.payload.sort).toEqual({ field: "document_date", direction: "asc" });
      expect(sent.payload.timezone).toBe("UTC");
      expect(body.id).toBe(stored.id);
      expect(body.created_at).toBe(stored.created_at);
      expect(body.payload.sort).toEqual({ field: "document_date", direction: "asc" });
      expect(body.payload.timezone).toBe("UTC");
      await showsLines('"direction": "asc"', '"timezone": "UTC"');
      stored = body;
      recording.facts.saved.update = summary;
      await caption("saved-update", `Saved ${savedName} with "direction": "asc"`, "result");
    });

    await test.step("save again without the zone", async () => {
      const value = await editorJSON();
      expect(value.timezone).toBe("UTC");
      delete value.timezone;
      await replaceJSON("saved-noop", value);
      expect((await queryJSON.inputValue()).includes('"timezone"')).toBe(false);
      await caption("zone-removed", 'The "timezone" line is removed', "step");
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
      // The server re-pins the same zone, so the definition and revision stay.
      expect(body.id).toBe(stored.id);
      expect(body.created_at).toBe(stored.created_at);
      expect(body.payload).toEqual(stored.payload);
      expect((await editorJSON()).timezone).toBe("UTC");
      await showsLines('"timezone": "UTC"');
      recording.facts.saved.noop = summary;
      await caption("saved-noop", `Saved ${savedName}. "timezone": "UTC" is back`, "result");
    });

    await test.step("run the saved search", async () => {
      const sort: Sort = { field: "document_date", direction: "asc" };
      const searching = posted(getCreateWorkspaceQueryUrl(), day, sort);
      await glideClick(
        "saved-run",
        drawer.getByRole("button", { name: `Run ${savedName}`, exact: true }),
        `Run ${savedName}`,
      );
      const response = await searching;
      const submitted = response.request().postDataJSON();
      // The saved definition carries its pinned zone, so it is not defaulted.
      expect(submitted.query.timezone).toBe("UTC");
      const { status, body } = await record("saved-run", response);
      expect(status).toBe(200);
      const snapshot = body as SnapshotPage;
      expect(snapshot.query.text).toBe(day);
      expect(snapshot.query.sort).toEqual(sort);
      expect(snapshot.timezone).toBe("UTC");
      expect(snapshot.timezone_defaulted).toBe(false);
      const order = snapshot.rows.map((row) => row.node_id);
      expect(order).toEqual(ascending);
      await expect(drawer).toBeHidden();
      await expect(results).toHaveAttribute("data-snapshot-id", snapshot.snapshot_id);
      expect(
        await rows.evaluateAll((elements) =>
          elements.map((row) => Number(row.getAttribute("data-snapshot-node"))),
        ),
      ).toEqual(order);
      recording.facts.run = {
        status,
        request_sort: submitted.query.sort,
        request_timezone: submitted.query.timezone,
        timezone: snapshot.timezone,
        timezone_defaulted: snapshot.timezone_defaulted,
        rows: order,
      };
      await caption(
        "saved-run",
        "Saved search results: evening-offset.eml, then morning-utc.eml",
        "result",
      );
    });

    recording.ended_at = now();
    recording.captions.at(-1)!.end = recording.ended_at;
    await stopScreencast();
    await Promise.all(frameWrites);
    recording.frames_drained = true;
    if (frameError !== undefined) throw frameError;
    const stamps = recording.frames.map((frame) => frame.timestamp);
    expect(stamps.length).toBeGreaterThan(recording.captions.length);
    expect(stamps.every((stamp, index) => index === 0 || stamp >= stamps[index - 1])).toBe(true);
    expect(stamps[0]).toBeGreaterThanOrEqual(recording.started_at - 1);
    expect(stamps.at(-1)!).toBeLessThanOrEqual(recording.ended_at + 1);
    await stop();
  } catch (error) {
    failure = error;
    recording.error = redact(error);
  }
  // Every cleanup is attempted independently, even after a screencast
  // failure; each error is kept and none of them can turn a failure into a
  // success.
  const attempt = async (label: string, action: () => Promise<void>) => {
    try {
      await action();
    } catch (cause) {
      recording.cleanup_errors.push(`${label}: ${redact(cause)}`);
    }
  };
  await attempt("screencast", stopScreencast);
  await attempt("frames", async () => {
    await Promise.allSettled(frameWrites);
    recording.frames_drained = true;
    if (frameError !== undefined) {
      recording.frame_error = redact(frameError);
      if (failure === undefined) throw frameError;
    }
  });
  await attempt("daemon", stop);
  await attempt("scratch", async () => {
    if (started && recording.cleanup === null) {
      recording.retained_vault = vault;
      throw new Error(`Kept ${vault} because its daemon was not verified stopped`);
    }
    await rm(scratch, { recursive: true, force: true });
    recording.scratch_removed = true;
  });
  const cleanupFailure =
    recording.cleanup_errors.length > 0
      ? new Error(`Date recording cleanup failed: ${recording.cleanup_errors.join("; ")}`)
      : undefined;
  if (failure === undefined && cleanupFailure) recording.error = redact(cleanupFailure);
  let writeError: unknown;
  try {
    await writeFile(
      path.join(output!, "recording.json"),
      `${JSON.stringify(recording, null, 2)}\n`,
    );
  } catch (cause) {
    writeError = cause;
  }
  if (failure !== undefined) {
    // The first failure stays the test's error; a lost receipt is reported beside it.
    if (writeError !== undefined) {
      test.info().annotations.push({
        type: "recording-json-write-error",
        description: redact(writeError),
      });
    }
    throw failure;
  }
  if (writeError !== undefined) {
    throw new Error(`Could not write recording.json: ${redact(writeError)}`);
  }
  if (cleanupFailure) throw cleanupFailure;
});
