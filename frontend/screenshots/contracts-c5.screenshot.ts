import { expect, test, type Browser, type Locator, type Page } from "@playwright/test";
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";

// C5 media: a real daemon seeded through the CLI with synthetic records,
// never a mocked API. Before captures run the C4 base binary on the same
// seed and viewport; After and the recording run this branch's binary.
const exec = promisify(execFile);
const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const screenshots = process.env.DOCBANK_SCREENSHOT_DIR!;
const branchBinary = path.join(repository, "bin", "docbank");
const baseBinary = process.env.DOCBANK_C5_BASE_BIN;
const scale = Number(process.env.DOCBANK_SCREENSHOT_SCALE ?? 1);
const desktop = { width: 1440, height: 960 };
const phone = { width: 390, height: 844 };

// syntheticPDF writes a one-page PDF with correct cross-reference offsets.
function syntheticPDF(text: string): Buffer {
  const stream = `BT /F1 18 Tf 72 720 Td (${text}) Tj ET`;
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
    `<< /Length ${stream.length} >>\nstream\n${stream}\nendstream`,
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
  ];
  let body = "%PDF-1.4\n";
  const offsets: number[] = [];
  objects.forEach((object, index) => {
    offsets.push(body.length);
    body += `${index + 1} 0 obj\n${object}\nendobj\n`;
  });
  const xref = body.length;
  body += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n`;
  for (const offset of offsets) body += `${String(offset).padStart(10, "0")} 00000 n \n`;
  body += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
  return Buffer.from(body, "latin1");
}

function day(offset: number): string {
  const value = new Date();
  value.setUTCDate(value.getUTCDate() + offset);
  return value.toISOString().slice(0, 10);
}

interface Vault {
  run: (...args: string[]) => Promise<string>;
  close: () => Promise<void>;
  leaseID: string;
  olderVersion: string;
  currentVersion: string;
}

// openVault seeds one synthetic vault: two parties, two contracts and a
// signed lease PDF with an older and a current version.
async function openVault(binary: string): Promise<Vault> {
  const workspace = await mkdtemp(path.join(tmpdir(), "docbank-contracts-c5-"));
  const home = path.join(workspace, "vault");
  const run = async (...args: string[]) =>
    (
      await exec(binary, args, {
        cwd: repository,
        env: { ...process.env, DOCBANK_HOME: home },
        timeout: 60_000,
      })
    ).stdout.trim();
  const close = async () => {
    await run("daemon", "stop").catch(() => "");
    const status = JSON.parse(await run("daemon", "status", "--json")) as { running?: boolean };
    if (status.running !== false)
      throw new Error("Synthetic contracts daemon did not stop; workspace retained.");
    await rm(workspace, { recursive: true, force: true });
  };
  try {
    const files = path.join(workspace, "files");
    await mkdir(files, { mode: 0o700 });
    const signed = path.join(files, "lease-signed.pdf");
    await writeFile(signed, syntheticPDF("Synthetic storage lease, draft for signature"), {
      mode: 0o600,
    });
    await run("document", "add", signed, "--dest", "/agreements");
    const version = async () =>
      (
        JSON.parse(await run("document", "stat", "/agreements/lease-signed.pdf", "--json")) as {
          current_version_id: string;
        }
      ).current_version_id;
    const olderVersion = await version();
    await writeFile(signed, syntheticPDF("Synthetic storage lease, signed"), { mode: 0o600 });
    await run("document", "put", signed, "/agreements/lease-signed.pdf");
    const currentVersion = await version();
    await run("party", "create", "Example Holdings Inc", "--kind", "organization", "--own");
    await run("party", "create", "Northwind Storage Ltd", "--kind", "organization");
    await run("party", "create", "Riverbend Analytics LLC", "--kind", "organization");
    const create = async (...args: string[]) =>
      (JSON.parse(await run("contract", "create", ...args, "--json")) as { contract_id: string })
        .contract_id;
    const leaseID = await create(
      "Northwind Storage - unit lease",
      ...["--type", "lease", "--status", "active", "--starts", day(-60), "--expires", day(300)],
      ...["--own", "Example Holdings Inc", "--counterparty", "Northwind Storage Ltd"],
      ...["--summary", "Storage unit 14 for archive boxes."],
    );
    await create(
      "Riverbend analytics services",
      ...["--type", "service", "--status", "pending_signature", "--expires", day(200)],
      ...["--own", "Example Holdings Inc", "--counterparty", "Riverbend Analytics LLC"],
    );
    return { run, close, leaseID, olderVersion, currentVersion };
  } catch (cause) {
    await close().catch(() => undefined);
    throw cause;
  }
}

async function shot(page: Page, name: string) {
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({
    path: path.join(screenshots, name),
    animations: "disabled",
    style: "#capture-pointer, #capture-step { visibility: hidden }",
  });
}

async function report(name: string, value: unknown) {
  console.log(`${name} ${JSON.stringify(value)}`);
  await writeFile(path.join(screenshots, `${name}.json`), JSON.stringify(value, null, 2));
}

// formGeometry measures every form field, the party blocks and the date rows
// so the visual report quotes rendered alignment instead of judging by eye.
async function formGeometry(main: Locator) {
  return main.evaluate((element) => {
    const box = (node: Element) => {
      const rect = node.getBoundingClientRect();
      return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom };
    };
    const fields = [...element.querySelectorAll(".contract-form .tool-field")].map((field) => ({
      label: field.querySelector("label, .tool-label")?.textContent?.trim() ?? "",
      ...box(field),
    }));
    const sections = [...element.querySelectorAll(".contract-form > .tool-section")].map(
      (section) => ({ heading: section.querySelector("h3")?.textContent?.trim(), ...box(section) }),
    );
    return {
      viewport: window.innerWidth,
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
      fields,
      sections,
      sectionGaps: sections.slice(1).map((section, index) => section.top - sections[index]!.bottom),
    };
  });
}

test.describe.configure({ mode: "serial" });

// views captures the existing views C5 changes, for Before and After.
async function views(browser: Browser, binary: string, label: "before" | "after") {
  const vault = await openVault(binary);
  const context = await browser.newContext({
    viewport: desktop,
    deviceScaleFactor: scale,
    colorScheme: "dark",
  });
  const page = await context.newPage();
  try {
    await page.goto(await vault.run("web", "--no-browser"));
    await page.getByRole("button", { name: "Contracts", exact: true }).click();
    const main = page.getByRole("main", { name: "Contracts" });
    const table = main.getByRole("table", { name: "Contracts" });
    await expect(table.getByRole("row")).toHaveCount(3);
    await shot(page, `${label}-register-desktop-dark.png`);
    await main
      .getByRole("button", { name: "Show contract Northwind Storage - unit lease" })
      .click();
    await expect(main.getByRole("heading", { name: "Northwind Storage - unit lease" })).toBeVisible();
    await shot(page, `${label}-detail-desktop-dark.png`);
    await page.getByRole("button", { name: "All files", exact: true }).click();
    await page.getByRole("cell", { name: "agreements", exact: true }).dblclick();
    await page.getByRole("cell", { name: "lease-signed.pdf", exact: true }).click();
    const inspector = page.getByRole("complementary", { name: "Document inspector" });
    await expect(inspector.getByRole("heading", { name: "History" })).toBeVisible();
    if (label === "after")
      await expect(inspector.getByText("Not linked to a contract")).toBeVisible();
    await shot(page, `${label}-inspector-desktop-dark.png`);
    await page.getByRole("button", { name: "Contracts", exact: true }).click();
    await expect(table.getByRole("row")).toHaveCount(3);
    await page.setViewportSize(phone);
    await expect(table).toBeVisible();
    await shot(page, `${label}-register-mobile-dark.png`);
    await main
      .getByRole("button", { name: "Show contract Northwind Storage - unit lease" })
      .click();
    await expect(main.getByRole("heading", { name: "Northwind Storage - unit lease" })).toBeVisible();
    expect(await main.evaluate((element) => element.scrollWidth - element.clientWidth)).toBe(0);
    await shot(page, `${label}-detail-mobile-dark.png`);
  } finally {
    await context.close();
    await vault.close();
  }
}

test("before: C4 base views", async ({ browser }) => {
  test.skip(!baseBinary, "DOCBANK_C5_BASE_BIN names the C4 base build");
  await mkdir(screenshots, { recursive: true, mode: 0o700 });
  await views(browser, baseBinary!, "before");
});

test("after: C5 views", async ({ browser }) => {
  await mkdir(screenshots, { recursive: true, mode: 0o700 });
  await views(browser, branchBinary, "after");
});

test("recording: create, stale edit and inspector link", async ({ browser }) => {
  test.setTimeout(300_000);
  await mkdir(screenshots, { recursive: true, mode: 0o700 });
  const vault = await openVault(branchBinary);
  const size = { width: 1280, height: 800 };
  const context = await browser.newContext({
    viewport: size,
    deviceScaleFactor: scale,
    colorScheme: "dark",
    recordVideo: { dir: path.join(screenshots, "video"), size },
  });
  // A macOS-style arrow whose tip sits exactly on the pointer position, and
  // one large caption per screen state.
  await context.addInitScript(() => {
    document.addEventListener("DOMContentLoaded", () => {
      const pointer = document.createElement("div");
      pointer.id = "capture-pointer";
      pointer.innerHTML =
        '<svg width="22" height="32" viewBox="0 0 22 32" xmlns="http://www.w3.org/2000/svg"><path d="M1 1 L1 25 L7 19.5 L11 29 L15 27.3 L11 18 L19 18 Z" fill="#000" stroke="#fff" stroke-width="1.6" stroke-linejoin="round"/></svg>';
      pointer.style.cssText =
        "position:fixed;left:640px;top:400px;width:22px;height:32px;pointer-events:none;z-index:2147483647;margin:-1px 0 0 -1px;filter:drop-shadow(0 1px 2px #0008)";
      const caption = document.createElement("div");
      caption.id = "capture-step";
      caption.style.cssText =
        "position:fixed;top:14px;left:50%;transform:translateX(-50%);padding:12px 26px;border-radius:10px;background:#0b0d12ee;color:#fff;font:600 26px/1.25 system-ui,sans-serif;pointer-events:none;z-index:2147483646;white-space:nowrap;box-shadow:0 6px 24px #0009;border:1px solid #ffffff26";
      document.body.append(pointer, caption);
      document.addEventListener("mousemove", (event) => {
        pointer.style.left = `${event.clientX}px`;
        pointer.style.top = `${event.clientY}px`;
      });
    });
  });
  const page = await context.newPage();
  const opened = Date.now();
  let position = { x: 640, y: 400 };
  const caption = async (text: string) => {
    await page.locator("#capture-step").evaluate((element, value) => {
      element.textContent = value;
    }, text);
  };
  const pause = (ms = 2000) => page.waitForTimeout(ms);
  // point glides the arrow tip to the target's centre, then clicks it.
  const point = async (locator: Locator, click = true) => {
    await locator.scrollIntoViewIfNeeded();
    const box = await locator.boundingBox();
    if (!box) throw new Error("Missing recording target");
    const target = { x: box.x + box.width / 2, y: box.y + box.height / 2 };
    for (let i = 1; i <= 24; i++) {
      await page.mouse.move(
        position.x + ((target.x - position.x) * i) / 24,
        position.y + ((target.y - position.y) * i) / 24,
      );
      await page.waitForTimeout(25);
    }
    position = target;
    await page.waitForTimeout(350);
    if (click) await locator.click();
  };
  const type = async (locator: Locator, text: string) => {
    await point(locator);
    await locator.press("ControlOrMeta+a");
    await locator.pressSequentially(text, { delay: 45 });
  };
  try {
    const url = new URL(await vault.run("web", "--no-browser"));
    await page.goto(url.href);
    await page.getByRole("button", { name: "Contracts", exact: true }).click();
    const main = page.getByRole("main", { name: "Contracts" });
    await expect(main.getByRole("table", { name: "Contracts" }).getByRole("row")).toHaveCount(3);
    await page.mouse.move(position.x, position.y);
    // Frames before the loaded register are trimmed, so no blank page shows.
    await writeFile(
      path.join(screenshots, "video-start.txt"),
      ((Date.now() - opened) / 1000 + 0.3).toFixed(2),
    );
    await caption("Contracts: start a new contract");
    await pause(1200);

    await point(main.getByRole("button", { name: "New contract" }).first());
    await expect(main.getByRole("heading", { name: "New contract" })).toBeVisible();
    await caption("Fill in the new contract");
    await type(main.getByLabel("Title"), "Harbor archive scanning");
    const adder = (block: string) =>
      main.getByRole("button", { name: `Find or create a party: Add ${block}` });
    const query = page.getByRole("combobox", { name: "Find or create a party" });
    await point(adder("own entity"));
    await query.pressSequentially("Example", { delay: 45 });
    await point(page.getByRole("option", { name: "Example Holdings Inc" }));
    await expect(main.getByRole("button", { name: "Remove Example Holdings Inc" })).toBeVisible();
    await caption("Create a counterparty in place");
    await point(adder("counterparty"));
    await query.pressSequentially("Harbor Scanning Co", { delay: 45 });
    await point(page.getByRole("option", { name: 'Create party "Harbor Scanning Co"' }));
    await expect(main.getByRole("group", { name: "New party" })).toBeVisible();
    await pause(1200);
    await shot(page, "after-create-party-desktop-dark.png");
    await point(main.getByRole("button", { name: "Create party" }));
    await expect(main.getByRole("button", { name: "Remove Harbor Scanning Co" })).toBeVisible();
    const desktopForm = await formGeometry(main);
    await report("form-desktop-geometry", desktopForm);
    await shot(page, "after-form-desktop-dark.png");
    await pause(1200);
    await point(main.getByRole("button", { name: "Create contract" }));
    await expect(main.getByRole("heading", { name: "Harbor archive scanning" })).toBeVisible();
    await expect(main.getByText("Contract saved.")).toBeVisible();
    await caption("Saved: contract and both parties");
    await shot(page, "after-created-desktop-dark.png");
    await pause();

    await caption("Edit a contract that changes elsewhere");
    await point(main.getByRole("button", { name: "All contracts" }).first());
    await point(main.getByRole("button", { name: "Show contract Northwind Storage - unit lease" }));
    await point(main.getByRole("button", { name: "Edit", exact: true }));
    await type(main.getByLabel("Title"), "Northwind Storage - unit 14 lease");
    await vault.run("contract", "edit", vault.leaseID, "--summary", "Changed in the CLI.");
    await point(main.getByRole("button", { name: "Save changes" }));
    await expect(
      main.getByText(
        "This contract changed. It was saved elsewhere after you opened it; nothing was saved.",
      ),
    ).toBeVisible();
    await expect(main.getByLabel("Title")).toHaveValue("Northwind Storage - unit 14 lease");
    await caption("Changed elsewhere: nothing was saved");
    await shot(page, "after-stale-desktop-dark.png");
    await pause();
    await point(main.getByRole("button", { name: "Load latest" }));
    await expect(main.getByLabel("Summary")).toHaveValue("Changed in the CLI.");
    await expect(main.getByLabel("Title")).toHaveValue("Northwind Storage - unit lease");
    await caption("Load latest shows the saved contract");
    await pause();

    await caption("Link the shown version from the inspector");
    await point(main.getByRole("button", { name: "Cancel" }).first());
    await point(page.getByRole("button", { name: "All files", exact: true }));
    const folder = page.getByRole("cell", { name: "agreements", exact: true });
    await point(folder, false);
    await folder.dblclick();
    await point(page.getByRole("cell", { name: "lease-signed.pdf", exact: true }));
    const inspector = page.getByRole("complementary", { name: "Document inspector" });
    const section = inspector.getByRole("region", { name: "Contracts" });
    await expect(section.getByText("Not linked to a contract")).toBeVisible();
    await point(section.getByRole("button", { name: "Link to contract" }));
    await point(section.getByRole("button", { name: "Find a contract" }));
    await page.getByRole("combobox", { name: "Find a contract" }).pressSequentially("north", {
      delay: 60,
    });
    await point(page.getByRole("option", { name: /^Northwind Storage - unit lease/ }));
    await point(section.getByRole("combobox", { name: /^Role/ }));
    await point(page.getByRole("option", { name: "Signed", exact: true }));
    await shot(page, "after-inspector-link-desktop-dark.png");
    await point(section.getByRole("button", { name: "Link", exact: true }));
    await expect(
      section.getByText("Linked as Signed to Northwind Storage - unit lease."),
    ).toBeVisible();
    await expect(
      section.getByRole("listitem", { name: "Northwind Storage - unit lease" }),
    ).toBeVisible();
    await caption("Linked: this exact version is on the contract");
    // The version the inspector displays is the one the contract records.
    await inspector.getByRole("group", { name: "Details" }).getByText("Details").click();
    const shown = inspector.getByText(vault.currentVersion, { exact: true });
    await expect(shown).toBeVisible();
    const linked = JSON.parse(await vault.run("contract", "show", vault.leaseID, "--json")) as {
      documents: { content_version_id: string; role: string }[];
    };
    expect(linked.documents).toEqual([
      expect.objectContaining({ role: "signed", content_version_id: vault.currentVersion }),
    ]);
    expect(vault.currentVersion).not.toBe(vault.olderVersion);
    const sectionGeometry = await section.evaluate((element) => {
      const box = element.getBoundingClientRect();
      const header = element.querySelector("header")!.getBoundingClientRect();
      const button = element.querySelector("header button")!.getBoundingClientRect();
      const inspectorBox = element.closest("aside, [role=complementary]")!.getBoundingClientRect();
      return {
        section: { left: box.left, right: box.right },
        headerRight: header.right,
        buttonRight: button.right,
        inspector: { left: inspectorBox.left, right: inspectorBox.right },
      };
    });
    await report("inspector-section-geometry", sectionGeometry);
    expect(Math.abs(sectionGeometry.buttonRight - sectionGeometry.headerRight)).toBeLessThanOrEqual(2);
    await shot(page, "after-inspector-linked-desktop-dark.png");
    await pause();

    await caption("The link appears on the contract");
    await point(page.getByRole("button", { name: "Contracts", exact: true }));
    await point(main.getByRole("button", { name: "Show contract Northwind Storage - unit lease" }));
    await expect(main.getByText("lease-signed.pdf")).toBeVisible();
    await shot(page, "after-detail-linked-desktop-dark.png");
    await pause(2500);
  } finally {
    await context.close();
    await vault.close();
  }
});

test("mobile form: one column within 390px", async ({ browser }) => {
  await mkdir(screenshots, { recursive: true, mode: 0o700 });
  const vault = await openVault(branchBinary);
  const context = await browser.newContext({
    viewport: phone,
    deviceScaleFactor: scale,
    colorScheme: "dark",
  });
  const page = await context.newPage();
  try {
    // A phone has no room for the sidebar, so the page is entered directly.
    const url = new URL(await vault.run("web", "--no-browser"));
    url.pathname = "/contracts";
    await page.goto(url.href);
    const main = page.getByRole("main", { name: "Contracts" });
    await main
      .getByRole("button", { name: "Show contract Northwind Storage - unit lease" })
      .click();
    await main.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(main.getByRole("heading", { name: /^Edit / })).toBeVisible();
    const geometry = await formGeometry(main);
    await report("form-mobile-geometry", geometry);
    expect(geometry.scrollWidth - geometry.clientWidth).toBe(0);
    const lefts = new Set(geometry.fields.map((field) => Math.round(field.left)));
    expect([...lefts], "every field shares one left edge").toHaveLength(1);
    for (const field of geometry.fields)
      expect(field.right, field.label).toBeLessThanOrEqual(phone.width);
    await shot(page, "after-form-mobile-dark.png");
    await main.getByLabel("Summary").scrollIntoViewIfNeeded();
    await shot(page, "after-form-mobile-terms-dark.png");
  } finally {
    await context.close();
    await vault.close();
  }
});
