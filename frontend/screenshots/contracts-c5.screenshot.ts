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

test("contract form, stale edit and inspector exact-version link", async ({ page }) => {
  const workspace = await mkdtemp(path.join(tmpdir(), "docbank-contracts-c5-"));
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
    const signed = path.join(files, "lease-signed.pdf");
    await writeFile(signed, syntheticPDF("Synthetic signed storage lease"), { mode: 0o600 });
    await run("document", "add", signed, "--dest", "/agreements");
    await run("party", "create", "Example Holdings Inc", "--kind", "organization", "--own");
    await run("party", "create", "Northwind Storage Ltd", "--kind", "organization");
    const lease = JSON.parse(
      await run(
        "contract", "create", "Northwind Storage - unit lease",
        "--type", "lease", "--status", "active",
        "--starts", day(-60), "--expires", day(300),
        "--own", "Example Holdings Inc", "--counterparty", "Northwind Storage Ltd",
        "--json",
      ),
    ) as { contract_id: string };

    await page.goto(await run("web", "--no-browser"));
    await page.getByRole("button", { name: "Contracts", exact: true }).click();
    const main = page.getByRole("main", { name: "Contracts" });

    // Create: own entity chosen, counterparty created in place, one atomic save.
    await main.getByRole("button", { name: "New contract" }).first().click();
    await expect(main.getByRole("heading", { name: "New contract" })).toBeVisible();
    await main.getByLabel("Title").fill("Harbor archive scanning");
    const adder = (block: string) =>
      main.getByRole("button", { name: `Find or create a party: Add ${block}` });
    const query = page.getByRole("combobox", { name: "Find or create a party" });
    await adder("own entity").click();
    await query.fill("Example");
    await page.getByRole("option", { name: "Example Holdings Inc" }).dispatchEvent("mousedown");
    await adder("counterparty").click();
    await query.fill("Harbor Scanning Co");
    await page.getByRole("option", { name: 'Create party "Harbor Scanning Co"' }).dispatchEvent("mousedown");
    await expect(main.getByRole("group", { name: "New party" })).toBeVisible();
    await shot(page, "contracts-c5-create-party.png");
    await main.getByRole("button", { name: "Create party" }).click();
    await expect(main.getByRole("button", { name: "Remove Harbor Scanning Co" })).toBeVisible();
    await shot(page, "contracts-c5-form.png");
    await main.getByRole("button", { name: "Create contract" }).click();
    await expect(main.getByRole("heading", { name: "Harbor archive scanning" })).toBeVisible();
    await expect(main.getByText("Contract saved.")).toBeVisible();
    await shot(page, "contracts-c5-created.png");

    // Stale edit: the contract changes in the CLI after the form opened.
    await main.getByRole("button", { name: "All contracts" }).first().click();
    await main.getByRole("button", { name: "Show contract Northwind Storage - unit lease" }).click();
    await main.getByRole("button", { name: "Edit", exact: true }).click();
    await main.getByLabel("Title").fill("Northwind Storage - unit 14 lease");
    await run("contract", "edit", lease.contract_id, "--summary", "Changed in the CLI.");
    await main.getByRole("button", { name: "Save changes" }).click();
    await expect(
      main.getByText(
        "This contract changed. It was saved elsewhere after you opened it; nothing was saved.",
      ),
    ).toBeVisible();
    await expect(main.getByLabel("Title")).toHaveValue("Northwind Storage - unit 14 lease");
    await shot(page, "contracts-c5-stale.png");
    await main.getByRole("button", { name: "Load latest" }).click();
    await expect(main.getByLabel("Summary")).toHaveValue("Changed in the CLI.");

    // Inspector: link the version the inspector shows and find it on the contract.
    const shown = JSON.parse(await run("document", "show", "/agreements/lease-signed.pdf", "--json")) as {
      current_version_id: string;
    };
    await page.getByRole("button", { name: "All files", exact: true }).click();
    await page.getByRole("cell", { name: "agreements", exact: true }).dblclick();
    await page.getByRole("row").filter({ hasText: "lease-signed.pdf" }).click();
    const inspector = page.getByRole("complementary", { name: "Document inspector" });
    const section = inspector.getByRole("region", { name: "Contracts" });
    await expect(section.getByText("Not linked to a contract")).toBeVisible();
    await section.getByRole("button", { name: "Link to contract" }).click();
    await section.getByRole("button", { name: "Find a contract" }).click();
    await page.getByRole("combobox", { name: "Find a contract" }).fill("north");
    await page.getByRole("option", { name: /^Northwind Storage - unit lease/ }).dispatchEvent("mousedown");
    await section.getByRole("combobox", { name: /^Role/ }).click();
    await page.getByRole("option", { name: "Signed" }).click();
    await shot(page, "contracts-c5-inspector-link.png");
    await section.getByRole("button", { name: "Link", exact: true }).click();
    await expect(section.getByText("Linked as Signed to Northwind Storage - unit lease.")).toBeVisible();
    await expect(section.getByRole("listitem", { name: "Northwind Storage - unit lease" })).toBeVisible();
    await shot(page, "contracts-c5-inspector-linked.png");
    const linked = JSON.parse(await run("contract", "show", lease.contract_id, "--json")) as {
      documents: { content_version_id: string; role: string }[];
    };
    expect(linked.documents).toEqual([
      expect.objectContaining({ role: "signed", content_version_id: shown.current_version_id }),
    ]);

    // Mobile: the form stacks to one column without horizontal overflow.
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole("button", { name: "Contracts", exact: true }).click();
    await main.getByRole("button", { name: "New contract" }).first().click();
    await expect(main.getByRole("heading", { name: "New contract" })).toBeVisible();
    expect(await main.evaluate((element) => element.scrollWidth - element.clientWidth)).toBe(0);
    await shot(page, "contracts-c5-mobile-form.png");
  } finally {
    await run("daemon", "stop").catch(() => "");
    const status = JSON.parse(await run("daemon", "status", "--json")) as { running?: boolean };
    if (status.running !== false)
      throw new Error("Synthetic contracts daemon did not stop; workspace retained.");
    await rm(workspace, { recursive: true, force: true });
  }
});
