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
  const source = "15610d9f2c401a9ff8ef8b21d6704eaa04ed8412";
  const baseline = "19e8ca3042316dcc47981b5b04cd1c0fc19f6d24";
  const spec = "document-date-query.screenshot.ts";
  const allowed = [`frontend/screenshots/${spec}`, "frontend/screenshots/report-export.run.mjs"];
  const results = path.join(repository, "frontend/test-results/playwright");
  const output = path.join(results, "report-export-ui-proof.json");
  const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");
  const gitBytes = (...args) =>
    execFileSync("git", args, { cwd: repository, maxBuffer: 64 * 1024 * 1024 });
  const names = (bytes) => bytes.toString("utf8").split("\0").filter(Boolean);
  const report = {
    source_head: source,
    base_head: source,
    baseline_head: baseline,
    proof_head: null,
    proof_parent: null,
    checkout_head: commit,
    checkout_tree: null,
    allowed_paths: allowed,
    changed_paths: null,
    host_device_scale_factor: 2,
    normal_cases: {},
    trees: {},
    pairing: null,
    cleanup: null,
    success: false,
    error: null,
  };
  const proofScratch = await mkdtemp(path.join(tmpdir(), "docbank-date-query-proof-"));
  const baselineTree = path.join(proofScratch, "baseline");
  const spawn = (command, args, options) => {
    const result = spawnSync(command, args, { stdio: "inherit", timeout: 600_000, ...options });
    const outcome = { status: result.status, signal: result.signal, error: result.error?.message };
    if (result.error) throw result.error;
    assert.equal(result.status, 0, `${command} ${args.join(" ")} failed`);
    return outcome;
  };
  let worktree = false;
  let proofError;
  try {
    // The unchanged normal cases qualify first: exactly one per SQLite mode.
    for (const mode of ["cgo", "purego"]) {
      const result = JSON.parse(
        await readFile(path.join(results, `report-export-${mode}.json`), "utf8"),
      );
      const { expected, unexpected, flaky, skipped } = result.stats;
      report.normal_cases[mode] = {
        expected,
        unexpected,
        flaky,
        skipped,
        errors: result.errors.length,
      };
      assert.deepEqual(report.normal_cases[mode], {
        expected: 1,
        unexpected: 0,
        flaky: 0,
        skipped: 0,
        errors: 0,
      });
    }
    const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
    report.proof_head = event.pull_request.head.sha;
    assert.equal(event.pull_request.base.sha, source, "PR base must equal source");
    assert.match(report.proof_head, /^[0-9a-f]{40}$/);
    // Checkout is shallow and may be GitHub's synthetic PR merge commit.
    execFileSync(
      "git",
      ["fetch", "--no-tags", "--depth=1", "origin", source, report.proof_head, baseline],
      { cwd: repository, stdio: "inherit" },
    );
    const parents = git("cat-file", "-p", report.proof_head)
      .split("\n")
      .filter((line) => line.startsWith("parent "))
      .map((line) => line.slice(7));
    assert.deepEqual(parents, [source], "proof parent must equal source/base");
    report.proof_parent = parents[0];
    report.checkout_tree = git("rev-parse", "HEAD^{tree}");
    assert.equal(
      report.checkout_tree,
      git("rev-parse", `${report.proof_head}^{tree}`),
      "checkout tree must equal proof tree",
    );
    report.changed_paths = names(
      gitBytes("diff", "--name-only", "-z", source, report.proof_head, "--"),
    );
    assert.deepEqual(report.changed_paths, allowed);
    assert.equal(
      git("status", "--porcelain", "--untracked-files=all"),
      "",
      "proof must start clean",
    );
    assert.equal(git("rev-parse", `${baseline}^{commit}`), baseline);

    git("worktree", "add", "--detach", baselineTree, baseline);
    worktree = true;
    for (const [tree, checkout, head] of [
      ["baseline", baselineTree, baseline],
      ["branch", repository, source],
    ]) {
      const binary = path.join(proofScratch, `docbank-${tree}`);
      const captures = path.join(proofScratch, `${tree}-captures`);
      const json = path.join(proofScratch, `${tree}.json`);
      const entry = {
        head,
        checkout_head: null,
        builds: [],
        binary_sha256: null,
        playwright: null,
      };
      report.trees[tree] = entry;
      entry.checkout_head = execFileSync("git", ["rev-parse", "HEAD"], {
        cwd: checkout,
        encoding: "utf8",
      }).trim();
      // The branch head is the proof tree; its source equals the frozen delivery.
      assert.equal(entry.checkout_head, tree === "baseline" ? baseline : commit);
      // The branch frontend was built by make's prerequisite; the baseline uses its own recipe.
      if (tree === "baseline") entry.builds.push(spawn("make", ["frontend"], { cwd: checkout }));
      const env = {
        ...process.env,
        CGO_ENABLED: "1",
        DOCBANK_SCREENSHOT_BINARY: binary,
        DOCBANK_SCREENSHOT_SCALE: "2",
        DOCBANK_DATE_QUERY_CAPTURE_DIR: captures,
        DOCBANK_DATE_QUERY_TREE: tree,
        PLAYWRIGHT_JSON_OUTPUT_FILE: json,
      };
      entry.builds.push(
        spawn("go", ["build", "-tags", "fts5", "-o", binary, "./cmd/docbank"], {
          cwd: checkout,
          env,
        }),
      );
      entry.binary_sha256 = sha256(await readFile(binary));
      const run = spawnSync(
        process.execPath,
        [
          "frontend/node_modules/@playwright/test/cli.js",
          "test",
          spec,
          "--config",
          "frontend/screenshots/playwright.config.ts",
          "--project",
          "chromium",
          "--reporter",
          "line,json",
          "--retries",
          "0",
          "--global-timeout",
          String(browserGlobalTimeout),
        ],
        { cwd: repository, env, stdio: "inherit", timeout: browserWatchdog },
      );
      entry.playwright = { status: run.status, signal: run.signal, error: run.error?.message };
      try {
        const result = JSON.parse(await readFile(json, "utf8"));
        entry.playwright.stats = result.stats;
        entry.playwright.errors = result.errors;
      } catch (cause) {
        entry.playwright.report_error = String(cause);
      }
      try {
        entry.evidence = JSON.parse(await readFile(path.join(captures, "evidence.json"), "utf8"));
        for (const capture of entry.evidence.captures) {
          const bytes = await readFile(path.join(captures, capture.file));
          assert.equal(bytes.subarray(1, 4).toString(), "PNG", `${capture.file} is not a PNG`);
          Object.assign(capture, {
            bytes: bytes.length,
            sha256: sha256(bytes),
            png_width: bytes.readUInt32BE(16),
            png_height: bytes.readUInt32BE(20),
            base64: bytes.toString("base64"),
          });
        }
      } catch (cause) {
        entry.evidence_error = cause.stack ?? String(cause);
      }
      if (run.error) throw run.error;
      assert.equal(run.status, 0, `${tree} date query capture failed`);
      const { expected, unexpected, flaky, skipped } = entry.playwright.stats;
      assert.deepEqual(
        { expected, unexpected, flaky, skipped },
        { expected: 1, unexpected: 0, flaky: 0, skipped: 0 },
        `${tree} must execute one passing capture`,
      );
      assert.equal(entry.evidence_error, undefined, `${tree} evidence is incomplete`);
      assert.equal(entry.evidence.captures.length, 9, `${tree} must capture nine images`);
    }
    // Before and after share every state, theme, viewport and image size.
    const shape = (tree) =>
      report.trees[tree].evidence.captures.map(({ file, png_width, png_height }) => ({
        file,
        png_width,
        png_height,
      }));
    report.pairing = { baseline: shape("baseline"), branch: shape("branch") };
    assert.deepEqual(report.pairing.branch, report.pairing.baseline, "captures must pair");
    for (const capture of report.pairing.branch) {
      const desktop = capture.file.includes("-desktop-");
      assert.deepEqual(
        [capture.png_width, capture.png_height],
        desktop ? [2880, 1920] : [780, 1688],
        `${capture.file} must use the host scale`,
      );
    }
    report.success = true;
  } catch (error) {
    report.error = error.stack ?? String(error);
    proofError = error;
  } finally {
    const cleanup = { worktree_removed: !worktree, scratch_removed: false, error: null };
    try {
      if (worktree) {
        git("worktree", "remove", "--force", baselineTree);
        cleanup.worktree_removed = !git("worktree", "list", "--porcelain").includes(baselineTree);
      }
      await rm(proofScratch, { recursive: true, force: true });
      cleanup.scratch_removed = true;
      cleanup.checkout_clean = git("status", "--porcelain", "--untracked-files=all") === "";
    } catch (cause) {
      cleanup.error = cause.stack ?? String(cause);
    }
    report.cleanup = cleanup;
    await mkdir(results, { recursive: true });
    await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  }
  if (proofError) throw proofError;
  assert.ok(report.cleanup.worktree_removed && report.cleanup.scratch_removed, "cleanup failed");
  assert.ok(report.cleanup.checkout_clean, "proof left the checkout dirty");
}
