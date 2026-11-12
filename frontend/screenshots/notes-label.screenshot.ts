import { expect, test, type BrowserContext } from "@playwright/test";
import { execFile } from "node:child_process";
import { access, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { promisify } from "node:util";

const exec = promisify(execFile);

test("the real notes worker uses the authored lane title", async ({ browser }) => {
  test.setTimeout(180_000);
  const repository = process.env.DOCBANK_PROOF_REPOSITORY!;
  const binary = process.env.DOCBANK_SCREENSHOT_BINARY!;
  const output = process.env.DOCBANK_SCREENSHOT_DIR!;
  const side = process.env.DOCBANK_PROOF_SIDE;
  if (side !== "before" && side !== "after") throw new Error("Explicit proof side required");
  const scale = Number(process.env.DOCBANK_SCREENSHOT_SCALE);
  expect(scale).toBe(2);
  const title = side === "before" ? "derive notes" : "Notes";
  const viewport = { width: 1440, height: 960 };
  await mkdir(output, { recursive: true });
  const workspace = await mkdtemp(path.join(tmpdir(), `docbank-notes-${side}-`));
  const env = {
    PATH: process.env.PATH,
    HOME: path.join(workspace, "home"),
    XDG_CONFIG_HOME: path.join(workspace, "config"),
    XDG_CACHE_HOME: path.join(workspace, "cache"),
    XDG_DATA_HOME: path.join(workspace, "data"),
    TMPDIR: workspace,
    DOCBANK_HOME: path.join(workspace, "vault"),
    DOCBANK_LOCK_DIR: path.join(workspace, "locks"),
    DOCBANK_TELEMETRY_ENABLED: "0",
  };
  const run = async (...args: string[]) => {
    try {
      return (await exec(binary, args, { cwd: repository, env, timeout: 60_000 })).stdout.trim();
    } catch {
      // Never put the authenticated web URL or runtime record in an artifact.
      throw new Error(`Synthetic CLI command failed: ${args.join(" ")}`);
    }
  };
  type Job = { name: string; lane: string; status: string; busy: boolean; controllable?: boolean };
  const receipt = {
    side, title, source: process.env.DOCBANK_PROOF_SOURCE, viewport, scale,
    worker: undefined as Job | undefined,
    captures: [] as { theme: string; crop: { x: number; y: number; width: number; height: number }; titleLeft: number; stateLeft: number }[],
    daemonStopped: false, workspaceRemoved: false,
  };
  let context: BrowserContext | undefined;
  try {
    await mkdir(env.HOME, { recursive: true });
    context = await browser.newContext({ viewport, deviceScaleFactor: scale, colorScheme: "dark" });
    // web creates a new empty synthetic vault and its ordinary daemon workers.
    const url = await run("web", "--no-browser");
    await expect.poll(async () => {
      const jobs = JSON.parse(await run("jobs", "--json")) as { items: Job[] };
      receipt.worker = jobs.items.find((job) => job.name === "derive:notes");
      return receipt.worker?.busy;
    }).toBe(false);
    expect(receipt.worker?.lane).toBe("derive:notes");
    expect(receipt.worker?.status).toBe("running");
    expect(receipt.worker?.controllable ?? false).toBe(false);
    const page = await context.newPage();
    await page.goto(url).catch(() => { throw new Error("Synthetic web navigation failed"); });
    await page.getByRole("button", { name: "System", exact: true }).click();
    await page.getByRole("tab", { name: "Running now", exact: true }).click();
    const drawer = page.getByRole("dialog", { name: "System", exact: true });
    const row = drawer.getByRole("table", { name: "All workers" }).getByRole("row").filter({
      has: page.getByRole("rowheader", { name: title, exact: true }),
    });
    await expect(row).toHaveCount(1);
    await row.scrollIntoViewIfNeeded();
    await expect(row).toBeInViewport();
    await expect(row.getByText("Idle", { exact: true })).toBeVisible();
    // Notes is read-only: no pause/resume control exists for this worker.
    await expect(row.getByRole("button")).toHaveCount(0);
    await expect(drawer.getByRole("heading", { name: "System", exact: true })).toBeInViewport();
    expect(await page.evaluate(() => window.devicePixelRatio)).toBe(scale);
    for (const theme of ["dark", "light"] as const) {
      await page.emulateMedia({ colorScheme: theme });
      await expect(row.getByRole("rowheader", { name: title, exact: true })).toBeVisible();
      const bounds = await drawer.boundingBox();
      const titleBounds = await row.getByRole("rowheader").boundingBox();
      const stateBounds = await row.getByText("Idle", { exact: true }).boundingBox();
      if (!bounds || !titleBounds || !stateBounds) throw new Error("Visible drawer geometry required");
      const crop = { x: Math.floor(bounds.x), y: Math.floor(bounds.y), width: Math.ceil(bounds.width), height: Math.ceil(bounds.height) };
      receipt.captures.push({ theme, crop, titleLeft: titleBounds.x, stateLeft: stateBounds.x });
      await page.screenshot({ path: path.join(output, `notes-label-${side}-${theme}.png`), clip: crop, animations: "disabled" });
    }
  } finally {
    try {
      await context?.close();
    } finally {
      try {
        await run("daemon", "stop");
        const status = JSON.parse(await run("daemon", "status", "--json")) as { running: boolean };
        expect(status.running).toBe(false);
        receipt.daemonStopped = true;
      } finally {
        try {
          await rm(workspace, { recursive: true });
          await expect(access(workspace)).rejects.toMatchObject({ code: "ENOENT" });
          receipt.workspaceRemoved = true;
        } finally {
          await writeFile(path.join(output, `${side}-receipt.json`), JSON.stringify(receipt, null, 2) + "\n");
        }
      }
    }
  }
});
