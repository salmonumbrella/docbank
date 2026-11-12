import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  access,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
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
      // P07 role search: matched-role chips and the People facet (#1453).
      DOCBANK_ROLE_SEARCH_PROOF: "branch",
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
          "role-search.screenshot.ts",
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
      assert.equal(run.status, 0, `${mode} browser cases failed`);
      const result = JSON.parse(await readFile(report, "utf8"));
      assert.equal(result.errors.length, 0, `${mode} runner errors`);
      const { expected, unexpected, flaky, skipped } = result.stats;
      assert.deepEqual(
        { expected, unexpected, flaky, skipped },
        { expected: 2, unexpected: 0, flaky: 0, skipped: 0 },
        `${mode} must execute two passing cases`,
      );
      console.log(`${mode}: executed=2 passed=2 skipped=0 failed=0`);
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

// Temporary, isolated CI proof; this runner change is not a delivery change.
if (process.env.CI) {
  const source = "467bb4af89232b1c0fec91d62080419342d764e5";
  const sourceTree = "5143b24f2ab7a5a1b72a15812ef26594a0b7d1fd";
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
  const names = (bytes) => bytes.toString("utf8").split("\0").filter(Boolean);
  const run = (cwd, command, args, options = {}) => {
    const result = spawnSync(command, args, { cwd, maxBuffer: 64 * 1024 * 1024, ...options });
    const record = {
      cwd,
      command,
      args,
      status: result.status,
      signal: result.signal,
      error: result.error?.message ?? null,
      stdout: result.stdout?.toString("utf8") ?? null,
      stderr: result.stderr?.toString("utf8") ?? null,
    };
    return { result, record };
  };
  const checked = (cwd, command, args) => {
    const { result, record } = run(cwd, command, args);
    if (result.error || result.status !== 0) throw new Error(JSON.stringify(record));
    return result.stdout;
  };
  const gitAt = (cwd, ...args) => checked(cwd, "git", args).toString("utf8").trim();
  const parents = (rev) =>
    gitAt(repository, "cat-file", "-p", rev)
      .split("\n")
      .filter((line) => line.startsWith("parent "))
      .map((line) => line.slice(7));
  const hashes = async (cwd) =>
    Object.fromEntries(
      await Promise.all(
        allowed.map(async (file) => [file, sha256(await readFile(path.join(cwd, file)))]),
      ),
    );
  const report = {
    source,
    source_tree: sourceTree,
    base: null,
    proof: null,
    checkout: commit,
    checkout_tree: null,
    checkout_parents: null,
    proof_tree: null,
    proof_parents: null,
    event: null,
    allowed_paths: allowed,
    dependency: null,
    generations: [],
    deterministic: false,
    cleanup: [],
    success: false,
    error: null,
  };
  let proofError;
  let proofScratch;
  const worktrees = [];
  try {
    const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
    report.event = {
      name: process.env.GITHUB_EVENT_NAME,
      sha: process.env.GITHUB_SHA,
      base_ref: event.pull_request.base.ref,
      base_sha: event.pull_request.base.sha,
      head_ref: event.pull_request.head.ref,
      head_sha: event.pull_request.head.sha,
      base_repo: event.pull_request.base.repo.full_name,
      head_repo: event.pull_request.head.repo.full_name,
    };
    report.base = event.pull_request.base.sha;
    report.proof = event.pull_request.head.sha;
    assert.equal(report.event.name, "pull_request");
    assert.equal(report.event.base_ref, "ci/timeline-d-generation-base");
    assert.equal(report.event.head_ref, "ci/timeline-d-generation-proof-3");
    assert.equal(report.event.base_repo, "salmonumbrella/docbank");
    assert.equal(report.event.head_repo, "salmonumbrella/docbank");
    assert.equal(report.base, source);
    assert.match(report.proof, /^[0-9a-f]{40}$/);
    const fetch = run(repository, "git", [
      "fetch",
      "--no-tags",
      "--depth=1",
      "origin",
      source,
      report.proof,
    ]);
    report.fetch = fetch.record;
    if (fetch.result.error || fetch.result.status !== 0) throw new Error("fetch failed");
    assert.equal(gitAt(repository, "rev-parse", `${source}^{tree}`), sourceTree);
    report.proof_parents = parents(report.proof);
    assert.deepEqual(report.proof_parents, [source]);
    report.proof_tree = gitAt(repository, "rev-parse", `${report.proof}^{tree}`);
    report.checkout_tree = gitAt(repository, "rev-parse", "HEAD^{tree}");
    report.checkout_parents = parents("HEAD");
    assert.equal(report.checkout, report.event.sha);
    if (report.checkout !== report.proof) {
      assert.deepEqual(
        report.checkout_parents,
        [source, report.proof],
        "checkout must be the PR merge",
      );
    }
    assert.equal(report.checkout_tree, report.proof_tree);
    assert.deepEqual(
      names(checked(repository, "git", ["diff", "--name-only", "-z", source, report.proof, "--"])),
      [runner],
    );
    assert.equal(gitAt(repository, "status", "--porcelain", "--untracked-files=all"), "");
    const modules = path.join(repository, "frontend/node_modules");
    await access(path.join(modules, ".bin", "orval"));
    report.dependency = {
      path: modules,
      check:
        "installed Orval executable exists; entries linked inside each scratch node_modules directory",
    };
    proofScratch = await mkdtemp(path.join(tmpdir(), "docbank-api-generation-"));
    for (let iteration = 1; iteration <= 2; iteration++) {
      const dir = path.join(proofScratch, `source-${iteration}`);
      const entry = { iteration, path: dir, error: null };
      report.generations.push(entry);
      try {
        worktrees.push(dir);
        const add = run(repository, "git", ["worktree", "add", "--detach", dir, source]);
        entry.checkout_command = add.record;
        if (add.result.error || add.result.status !== 0) throw new Error("worktree add failed");
        entry.head = gitAt(dir, "rev-parse", "HEAD");
        entry.tree = gitAt(dir, "rev-parse", "HEAD^{tree}");
        entry.status_before = gitAt(dir, "status", "--porcelain", "--untracked-files=all");
        assert.equal(entry.head, source);
        assert.equal(entry.tree, sourceTree);
        assert.equal(entry.status_before, "");
        entry.hashes_before = await hashes(dir);
        const scratchModules = path.join(dir, "frontend/node_modules");
        await mkdir(scratchModules);
        for (const name of await readdir(modules)) {
          await symlink(path.join(modules, name), path.join(scratchModules, name));
        }
        entry.status_after_dependency = gitAt(
          dir,
          "status",
          "--porcelain",
          "--untracked-files=all",
        );
        assert.equal(
          entry.status_after_dependency,
          "",
          "dependency link must not modify tracked or untracked inputs",
        );
        const generation = run(dir, "make", ["generate-api"], {
          env: process.env,
          timeout: 600_000,
        });
        entry.generation_command = generation.record;
        entry.changed_paths = names(
          checked(dir, "git", ["diff", "--name-only", "-z", "HEAD", "--"]),
        );
        entry.untracked_paths = names(
          checked(dir, "git", ["ls-files", "--others", "--exclude-standard", "-z"]),
        );
        const patch = checked(dir, "git", [
          "diff",
          "--binary",
          "--full-index",
          "--no-ext-diff",
          "--no-textconv",
          "HEAD",
          "--",
        ]);
        entry.patch_base64 = patch.toString("base64");
        entry.patch_sha256 = sha256(patch);
        entry.hashes_after = await hashes(dir);
        assert.deepEqual(entry.untracked_paths, []);
        assert.deepEqual(
          entry.changed_paths.filter((file) => !allowed.includes(file)),
          [],
        );
        if (generation.result.error || generation.result.status !== 0)
          throw new Error("make generate-api failed");
      } catch (error) {
        entry.error = error.stack ?? String(error);
      }
    }
    const [first, second] = report.generations;
    report.deterministic =
      first.patch_base64 === second.patch_base64 &&
      JSON.stringify(first.hashes_before) === JSON.stringify(second.hashes_before) &&
      JSON.stringify(first.hashes_after) === JSON.stringify(second.hashes_after) &&
      JSON.stringify(first.changed_paths) === JSON.stringify(second.changed_paths);
    assert.ok(!first.error && !second.error, "both independent generations must succeed");
    assert.ok(report.deterministic, "independent generation outputs must match");
  } catch (error) {
    proofError = error;
    report.error = error.stack ?? String(error);
  } finally {
    for (const dir of worktrees) {
      const removal = run(repository, "git", ["worktree", "remove", "--force", dir]);
      report.cleanup.push(removal.record);
      if (removal.result.error || removal.result.status !== 0) {
        proofError ??= new Error(`worktree cleanup failed: ${dir}`);
        report.error ??= proofError.stack;
      }
    }
    if (proofScratch) {
      try {
        await rm(proofScratch, { recursive: true, force: true });
        await access(proofScratch).then(
          () => {
            throw new Error("scratch directory still exists");
          },
          (error) => {
            if (error.code !== "ENOENT") throw error;
          },
        );
        report.cleanup.push({ command: "rm", path: proofScratch, success: true });
      } catch (error) {
        report.cleanup.push({
          command: "rm",
          path: proofScratch,
          success: false,
          error: String(error),
        });
        proofError ??= error;
        report.error ??= error.stack ?? String(error);
      }
    }
    report.success = !proofError;
    await mkdir(path.dirname(output), { recursive: true });
    await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  }
  if (proofError) throw proofError;
}
