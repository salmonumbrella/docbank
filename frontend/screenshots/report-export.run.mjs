import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const git = (...args) => execFileSync("git", args, { cwd: repository, encoding: "utf8" }).trim();
const commit = git("rev-parse", "HEAD");
const dirty = git("status", "--porcelain") !== "";
console.log(`Browser qualification: ${commit} (${dirty ? "dirty" : "clean"} tree)`);
const scratch = await mkdtemp(path.join(tmpdir(), "docbank-browser-build-"));
const failures = [];
// Playwright stops its own run, retries included, before the watchdog kills it,
// so its JSON reporter always writes the result.
const browserWatchdog = 360_000;
const browserGlobalTimeout = browserWatchdog - 30_000;
try {
  for (const [mode, cgo] of [
    ["cgo", "1"],
    ["purego", "0"],
  ]) {
    console.log(`Building CGO_ENABLED=${cgo} (${mode})`);
    const binary = path.join(
      scratch,
      `docbank-${mode}${process.platform === "win32" ? ".exe" : ""}`,
    );
    // CI keeps each mode's JSON report for upload.
    const report = process.env.CI
      ? path.join(repository, "frontend/test-results/playwright", `report-export-${mode}.json`)
      : path.join(scratch, `${mode}.json`);
    const env = {
      ...process.env,
      CGO_ENABLED: cgo,
      DOCBANK_SCREENSHOT_BINARY: binary,
      DOCBANK_REPORT_EXPORT_SCREENSHOT_DIR: path.join(
        repository,
        ".superpowers/report-export-browser",
        mode,
      ),
      PLAYWRIGHT_JSON_OUTPUT_FILE: report,
    };
    try {
      await rm(env.DOCBANK_REPORT_EXPORT_SCREENSHOT_DIR, { recursive: true, force: true });
      const build = spawnSync("go", ["build", "-tags", "fts5", "-o", binary, "./cmd/docbank"], {
        cwd: repository,
        env,
        stdio: "inherit",
        timeout: 600_000,
      });
      if (build.error) throw build.error;
      assert.equal(build.status, 0, `${mode} build failed`);
      const run = spawnSync(
        process.execPath,
        [
          "frontend/node_modules/@playwright/test/cli.js",
          "test",
          "report-export.screenshot.ts",
          "--config",
          "frontend/screenshots/playwright.config.ts",
          "--project",
          "chromium",
          "--reporter",
          "line,json",
          "--global-timeout",
          String(browserGlobalTimeout),
        ],
        { cwd: repository, env, stdio: "inherit", timeout: browserWatchdog },
      );
      if (run.error) throw run.error;
      assert.equal(run.status, 0, `${mode} browser case failed`);
      const result = JSON.parse(await readFile(report, "utf8"));
      assert.equal(result.errors.length, 0, `${mode} runner errors`);
      const { expected, unexpected, flaky, skipped } = result.stats;
      assert.deepEqual(
        { expected, unexpected, flaky, skipped },
        { expected: 1, unexpected: 0, flaky: 0, skipped: 0 },
        `${mode} must execute one passing case`,
      );
      console.log(`${mode}: executed=1 passed=1 skipped=0 failed=0`);
    } catch (error) {
      failures.push(mode);
      console.error(`${mode}: ${error.message}`);
    }
  }
} finally {
  await rm(scratch, { recursive: true, force: true });
}
if (failures.length) {
  throw new Error(`Browser qualification failed: ${failures.join(", ")}`);
}

// This isolated CI proof branch never ships with the delivery branch.
if (process.env.CI) {
  const source = "1d03fd83396723bdfc70d736a011c92d5ce39e5a";
  const runner = "frontend/screenshots/report-export.run.mjs";
  const allowed = [
    "internal/api/openapi.yaml",
    "internal/apiclient/client.gen.go",
    "frontend/src/generated/docbank.ts",
    "frontend/src/generated/photoSourceExtensions.ts",
    "frontend/src/generated/documentDateKinds.ts",
  ];
  const output = path.join(
    repository,
    "frontend/test-results/playwright/report-export-api-generation.json",
  );
  const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");
  const gitBytes = (...args) =>
    execFileSync("git", args, { cwd: repository, maxBuffer: 64 * 1024 * 1024 });
  const names = (bytes) => bytes.toString("utf8").split("\0").filter(Boolean);
  const hashes = async () =>
    Object.fromEntries(
      await Promise.all(
        allowed.map(async (file) => [file, sha256(await readFile(path.join(repository, file)))]),
      ),
    );
  const report = {
    source_head: source,
    base_head: source,
    proof_head: null,
    checkout_head: commit,
    proof_parent: null,
    allowed_paths: allowed,
    generated_paths: [],
    patch_base64: null,
    patch_sha256: null,
    hashes_before: null,
    hashes_after_first: null,
    hashes_after_second: null,
    generations: [],
    deterministic: false,
    success: false,
    error: null,
  };
  let proofError;
  try {
    const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
    report.proof_head = event.pull_request.head.sha;
    assert.equal(event.pull_request.base.sha, source, "PR base must equal source");
    assert.match(report.proof_head, /^[0-9a-f]{40}$/);
    // Checkout is shallow and may be GitHub's synthetic PR merge commit.
    execFileSync("git", ["fetch", "--no-tags", "--depth=1", "origin", source, report.proof_head], {
      cwd: repository,
      stdio: "inherit",
    });
    const parents = git("cat-file", "-p", report.proof_head)
      .split("\n")
      .filter((line) => line.startsWith("parent "))
      .map((line) => line.slice(7));
    assert.deepEqual(parents, [source], "proof parent must equal source/base");
    report.proof_parent = parents[0];
    assert.equal(
      git("rev-parse", "HEAD^{tree}"),
      git("rev-parse", `${report.proof_head}^{tree}`),
      "checkout tree must equal proof tree",
    );
    assert.deepEqual(
      names(gitBytes("diff", "--name-only", "-z", source, report.proof_head, "--")),
      [runner],
    );
    assert.equal(
      git("status", "--porcelain", "--untracked-files=all"),
      "",
      "proof must start clean",
    );
    report.hashes_before = await hashes();
    let firstPatch;
    for (let iteration = 1; iteration <= 2; iteration++) {
      const result = spawnSync("make", ["generate-api"], {
        cwd: repository,
        env: process.env,
        stdio: "inherit",
      });
      report.generations.push({
        iteration,
        status: result.status,
        signal: result.signal,
        error: result.error?.message ?? null,
      });
      const changed = names(gitBytes("diff", "--name-only", "-z", "HEAD", "--"));
      const untracked = names(gitBytes("ls-files", "--others", "--exclude-standard", "-z"));
      const patch = gitBytes("diff", "--binary", "--no-ext-diff", "--no-textconv", "HEAD", "--");
      report.generations[iteration - 1].changed_paths = changed;
      report.generations[iteration - 1].untracked_paths = untracked;
      if (iteration === 1) {
        firstPatch = patch;
        report.generated_paths = changed;
        report.patch_base64 = patch.toString("base64");
        report.patch_sha256 = sha256(patch);
        report.hashes_after_first = await hashes();
      } else {
        report.hashes_after_second = await hashes();
        report.deterministic = patch.equals(firstPatch);
      }
      assert.deepEqual(untracked, [], "generator created untracked files");
      assert.deepEqual(
        changed.filter((file) => !allowed.includes(file)),
        [],
        "generator changed non-allowlisted files",
      );
      if (result.error) throw result.error;
      assert.equal(result.status, 0, `generation ${iteration} failed`);
      if (iteration === 2) assert.ok(report.deterministic, "generation output changed on second run");
    }
    report.success = true;
  } catch (error) {
    report.error = error.stack ?? String(error);
    proofError = error;
  } finally {
    await mkdir(path.dirname(output), { recursive: true });
    await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  }
  if (proofError) throw proofError;
}
