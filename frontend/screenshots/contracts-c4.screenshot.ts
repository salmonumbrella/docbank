import { expect, test, type Page } from "@playwright/test";
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";

const exec = promisify(execFile);
const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const screenshots = process.env.DOCBANK_SCREENSHOT_DIR!;

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

async function shot(page: Page, name: string) {
  // Finished transitions, as in the other harness captures, so a nav
  // highlight is never photographed mid-fade.
  await page.screenshot({ path: path.join(screenshots, name), animations: "disabled" });
}

// tableGeometry measures where each visible header's and first row's content
// starts, and the gap between consecutive rows' titles.
async function tableGeometry(table: ReturnType<Page["getByRole"]>) {
  return table.evaluate((element) => {
    const visible = (cell: Element) => getComputedStyle(cell).display !== "none";
    const contentLeft = (cell: Element) => {
      const range = document.createRange();
      range.selectNodeContents(cell);
      return range.getBoundingClientRect().left;
    };
    const headers = [...element.querySelectorAll("thead th")].filter(visible);
    const first = [...element.querySelectorAll("tbody tr:first-child > *")].filter(visible);
    const titles = [...element.querySelectorAll("tbody .title-button")].map((button) =>
      button.getBoundingClientRect(),
    );
    const rows = [...element.querySelectorAll("tbody tr")].map((row) => {
      const cell = row.firstElementChild!;
      return Number.parseFloat(getComputedStyle(cell).borderBottomWidth);
    });
    return {
      columns: headers.map((header, index) => ({
        name: header.textContent?.trim(),
        headerLeft: contentLeft(header),
        cellLeft: contentLeft(first[index]!),
      })),
      titleGaps: titles.slice(1).map((box, index) => box.top - titles[index]!.bottom),
      rowDividers: rows,
    };
  });
}

test("contracts register, detail and exact linked version", async ({ page }) => {
  const workspace = await mkdtemp(path.join(tmpdir(), "docbank-contracts-"));
  const vault = path.join(workspace, "vault");
  const run = async (...args: string[]) =>
    (
      await exec(path.join(repository, "bin", "docbank"), args, {
        cwd: repository,
        env: { ...process.env, DOCBANK_HOME: vault },
        timeout: 60_000,
      })
    ).stdout.trim();
  try {
    await mkdir(screenshots, { recursive: true, mode: 0o700 });
    const files = path.join(workspace, "files");
    await mkdir(files, { mode: 0o700 });
    const draft = path.join(files, "lease-draft.txt");
    const signed = path.join(files, "lease-signed.pdf");
    const audit = path.join(files, "lease-audit-trail.txt");
    await writeFile(draft, "Synthetic draft 1: monthly fee 120 units.\n", { mode: 0o600 });
    await writeFile(signed, syntheticPDF("Synthetic signed storage lease"), { mode: 0o600 });
    await writeFile(audit, "Synthetic audit trail for the storage lease.\n", { mode: 0o600 });
    await run("document", "add", draft, signed, audit, "--dest", "/agreements");

    await run("party", "create", "Example Holdings Inc", "--kind", "organization", "--own");
    await run(
      "party",
      "create",
      "Northwind Storage Ltd",
      "--kind",
      "organization",
      "--country",
      "CA",
    );
    await run("party", "create", "Riverbend Analytics LLC", "--kind", "organization");
    const create = async (...args: string[]) =>
      JSON.parse(await run("contract", "create", ...args, "--json")) as { contract_id: string };
    const lease = await create(
      "Northwind Storage - unit lease",
      "--type",
      "lease",
      "--status",
      "active",
      "--starts",
      day(-60),
      "--expires",
      day(20),
      "--signed",
      day(-62),
      "--own",
      "Example Holdings Inc",
      "--counterparty",
      "Northwind Storage Ltd",
      "--summary",
      "Storage unit 14 for archive boxes.",
      "--consideration",
      "Monthly fee of 120 units, invoiced quarterly.",
      "--termination",
      "Either party with 60 days written notice.",
    );
    await create(
      "Riverbend analytics services",
      "--type",
      "service",
      "--status",
      "pending_signature",
      "--expires",
      day(200),
      "--own",
      "Example Holdings Inc",
      "--counterparty",
      "Riverbend Analytics LLC",
    );
    await create(
      "Northwind mutual NDA",
      "--type",
      "nda",
      "--status",
      "active",
      "--starts",
      day(-400),
      "--expires",
      day(-10),
      "--own",
      "Example Holdings Inc",
      "--counterparty",
      "Northwind Storage Ltd",
    );
    await create(
      "Archive scanning licence",
      "--type",
      "licence",
      "--status",
      "draft",
      "--counterparty",
      "Northwind Storage Ltd",
      "--counterparty",
      "Riverbend Analytics LLC",
    );
    const id = lease.contract_id;
    await run("contract", "link", id, "/agreements/lease-draft.txt", "--role", "draft");
    await run("contract", "link", id, "/agreements/lease-signed.pdf", "--role", "signed");
    await run("contract", "link", id, "/agreements/lease-audit-trail.txt", "--role", "audit_trail");
    // A later draft leaves the link on its old version; trash makes a link unavailable.
    await writeFile(draft, "Synthetic draft 2: monthly fee 140 units.\n", { mode: 0o600 });
    await run("document", "put", draft, "/agreements/lease-draft.txt");
    await run("document", "remove", "/agreements/lease-audit-trail.txt");

    await page.goto(await run("web", "--no-browser"));
    await page.getByRole("button", { name: "Contracts", exact: true }).click();
    const main = page.getByRole("main", { name: "Contracts" });
    const table = main.getByRole("table", { name: "Contracts" });
    await expect(table.getByRole("row")).toHaveCount(5);
    await expect(table.getByText("Expired")).toBeVisible();
    await expect(table.getByText("Northwind Storage Ltd, Riverbend Analytics LLC")).toBeVisible();
    expect(new URL(page.url()).pathname).toBe("/contracts");
    expect(page.url()).not.toContain("web_session");
    // Direct entry marks Contracts, not All files, as the current page, and
    // All files settles to the idle look of an ordinary item.
    const nav = (name: string) => page.getByRole("button", { name, exact: true });
    await expect(nav("Contracts")).toHaveAttribute("aria-current", "page");
    await expect(nav("All files")).not.toHaveAttribute("aria-current", /.+/);
    const background = (name: string) =>
      nav(name).evaluate((element) => getComputedStyle(element).backgroundColor);
    await expect.poll(() => background("All files")).toBe(await background("Imports"));
    const desktop = await tableGeometry(table);
    console.log(`contracts desktop geometry ${JSON.stringify(desktop)}`);
    await test.info().attach("contracts-desktop-geometry", {
      body: JSON.stringify(desktop, null, 2),
      contentType: "application/json",
    });
    for (const column of desktop.columns)
      expect(Math.abs(column.headerLeft - column.cellLeft), column.name).toBeLessThanOrEqual(2);
    for (const divider of desktop.rowDividers) expect(divider).toBeGreaterThanOrEqual(1);
    await shot(page, "contracts-table.png");

    await main.getByRole("combobox", { name: /^Expires/ }).click();
    await page.getByRole("option", { name: "Within 30 days" }).click();
    await expect(table.getByRole("row")).toHaveCount(2);
    await expect(main.getByText(/in any status/)).toBeVisible();
    await shot(page, "contracts-window.png");
    await main.getByRole("button", { name: "Clear filters" }).first().click();
    await expect(table.getByRole("row")).toHaveCount(5);

    await main
      .getByRole("button", { name: "Show contract Northwind Storage - unit lease" })
      .click();
    await expect(
      main.getByRole("heading", { name: "Northwind Storage - unit lease" }),
    ).toBeVisible();
    await expect(main.getByText("lease-signed.pdf")).toBeVisible();
    await expect(main.getByText("Older version")).toBeVisible();
    await expect(main.getByText("Unavailable")).toBeVisible();
    await expect(main.getByRole("button", { name: /^Open Audit trail/ })).toBeDisabled();
    await shot(page, "contracts-detail.png");

    await main.getByRole("button", { name: "Open Draft lease-draft.txt" }).click();
    const reader = page.getByRole("dialog", { name: "Read lease-draft.txt" });
    await expect(reader.getByText("Synthetic draft 1: monthly fee 120 units.")).toBeVisible();
    await expect(reader.getByText("Synthetic draft 2")).toHaveCount(0);
    await shot(page, "contracts-reader-older.png");
    await page.keyboard.press("Escape");
    await expect(reader).toHaveCount(0);

    await main.getByRole("button", { name: "Open Signed lease-signed.pdf" }).click();
    await expect(page.getByRole("dialog", { name: "Read lease-signed.pdf" })).toBeVisible();
    await page.waitForTimeout(1500);
    await shot(page, "contracts-reader-signed.png");
    await page.keyboard.press("Escape");

    await page.setViewportSize({ width: 390, height: 844 });
    await main.getByRole("button", { name: "All contracts" }).first().click();
    await expect(table).toBeVisible();
    await expect(table.getByRole("columnheader", { name: "Type" })).toBeHidden();
    await shot(page, "contracts-mobile.png");
    // Mobile evidence: nothing in the page, filters or table extends past the viewport.
    const bounds = await main.evaluate((element) => {
      const measure = (cell: Element) => {
        const box = cell.getBoundingClientRect();
        const style = getComputedStyle(cell);
        return {
          text: cell.textContent?.trim(),
          left: box.left,
          right: box.right,
          scrollWidth: cell.scrollWidth,
          clientWidth: cell.clientWidth,
          paddingLeft: style.paddingLeft,
          paddingRight: style.paddingRight,
          whiteSpace: style.whiteSpace,
        };
      };
      const table = element.querySelector("table")!;
      return {
        viewport: window.innerWidth,
        main: { scrollWidth: element.scrollWidth, clientWidth: element.clientWidth },
        table: {
          right: table.getBoundingClientRect().right,
          borderCollapse: getComputedStyle(table).borderCollapse,
          borderSpacing: getComputedStyle(table).borderSpacing,
        },
        columns: [...table.querySelectorAll("thead th")].map(measure),
        cells: [...table.querySelectorAll("tbody th, tbody td")]
          .filter((cell) => getComputedStyle(cell).display !== "none")
          .map(measure),
        // Each status is a local span inside the kit chip's label box; the
        // label clips overflowing content, so both boxes are recorded.
        chips: [...table.querySelectorAll(".status-label")].map((span) => {
          const label = span.parentElement!;
          const box = span.getBoundingClientRect();
          const clip = label.getBoundingClientRect();
          const style = getComputedStyle(span);
          const clipStyle = getComputedStyle(label);
          return {
            text: span.textContent?.trim(),
            width: box.width,
            height: box.height,
            left: box.left,
            right: box.right,
            top: box.top,
            bottom: box.bottom,
            whiteSpace: style.whiteSpace,
            label: {
              left: clip.left,
              right: clip.right,
              top: clip.top,
              bottom: clip.bottom,
              scrollWidth: label.scrollWidth,
              clientWidth: label.clientWidth,
              scrollHeight: label.scrollHeight,
              clientHeight: label.clientHeight,
              overflow: clipStyle.overflow,
              textOverflow: clipStyle.textOverflow,
            },
          };
        }),
      };
    });
    console.log(`contracts mobile bounds ${JSON.stringify(bounds)}`);
    await test.info().attach("contracts-mobile-bounds", {
      body: JSON.stringify(bounds, null, 2),
      contentType: "application/json",
    });
    const width = page.viewportSize()!.width;
    const right = async (locator: ReturnType<typeof page.getByRole>) =>
      (await locator.boundingBox())!.x + (await locator.boundingBox())!.width;
    expect(await main.evaluate((element) => element.scrollWidth - element.clientWidth)).toBe(0);
    expect(await right(main.getByRole("combobox", { name: /^Expires/ }))).toBeLessThanOrEqual(
      width,
    );
    expect(await right(table)).toBeLessThanOrEqual(width);
    for (const name of ["Title", "Counterparty", "Status", "Expires"])
      expect(await right(table.getByRole("columnheader", { name }))).toBeLessThanOrEqual(width);
    // Fit must not come from hiding content: every visible cell and status
    // label shows its whole text inside the viewport, wrapping instead of clipping.
    for (const cell of [...bounds.columns, ...bounds.cells]) {
      expect(cell.scrollWidth, cell.text).toBeLessThanOrEqual(cell.clientWidth + 1);
      expect(cell.right, cell.text).toBeLessThanOrEqual(width);
    }
    const pending = bounds.chips.find((chip) => chip.text === "Pending signature");
    expect(pending).toBeDefined();
    expect(bounds.chips).toHaveLength(bounds.cells.length / 4);
    for (const chip of bounds.chips) {
      // Nothing overflows the clipping label, so no ellipsis or clip can show,
      // and the whole status box lies inside it and the viewport.
      expect(chip.label.scrollWidth, chip.text).toBeLessThanOrEqual(chip.label.clientWidth + 1);
      expect(chip.label.scrollHeight, chip.text).toBeLessThanOrEqual(chip.label.clientHeight + 1);
      expect(chip.left, chip.text).toBeGreaterThanOrEqual(chip.label.left - 1);
      expect(chip.right, chip.text).toBeLessThanOrEqual(chip.label.right + 1);
      expect(chip.top, chip.text).toBeGreaterThanOrEqual(chip.label.top - 1);
      expect(chip.bottom, chip.text).toBeLessThanOrEqual(chip.label.bottom + 1);
      expect(chip.label.right, chip.text).toBeLessThanOrEqual(width);
      expect(chip.height, chip.text).toBeLessThan(40);
    }
    await expect(table.getByText("Pending signature", { exact: true })).toBeVisible();
    // Rows read as separate records: aligned columns, a divider on every row
    // and clear space between one row's title and the next.
    const mobile = await tableGeometry(table);
    console.log(`contracts mobile geometry ${JSON.stringify(mobile)}`);
    await test.info().attach("contracts-mobile-geometry", {
      body: JSON.stringify(mobile, null, 2),
      contentType: "application/json",
    });
    expect(mobile.columns.map((column) => column.name)).toEqual([
      "Title",
      "Counterparty",
      "Status",
      "Expires",
    ]);
    for (const column of mobile.columns)
      expect(Math.abs(column.headerLeft - column.cellLeft), column.name).toBeLessThanOrEqual(2);
    for (const divider of mobile.rowDividers) expect(divider).toBeGreaterThanOrEqual(1);
    for (const gap of mobile.titleGaps) expect(gap).toBeGreaterThanOrEqual(12);
    await main
      .getByRole("button", { name: "Show contract Northwind Storage - unit lease" })
      .click();
    await expect(main.getByText("Older version")).toBeVisible();
    await shot(page, "contracts-mobile-detail.png");
  } finally {
    // Stop the synthetic daemon even when seeding failed, then delete its home.
    await run("daemon", "stop").catch(() => "");
    const status = JSON.parse(await run("daemon", "status", "--json")) as { running?: boolean };
    if (status.running !== false)
      throw new Error("Synthetic contracts daemon did not stop; workspace retained.");
    await rm(workspace, { recursive: true, force: true });
  }
});
