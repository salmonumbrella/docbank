import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
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
// Before-and-after screenshot proofs a branch adds to this lane. Each entry is
// { spec, env }: the branch run sets env=branch, and the base capture runs the
// same spec, fixtures included, against main's binary with env=base. Each spec
// holds exactly one test, so the stats check counts specs.
const proofs = [{ spec: "document-facts.screenshot.ts", env: "DOCBANK_DOCUMENT_FACTS_PROOF" }];
// Where the base capture comes from: a pull request's base branch, or, for a
// manual workflow_dispatch run (a fork branch before its upstream PR), the
// current upstream main fetched by explicit URL.
const upstream = "https://github.com/kenn-io/docbank.git";
const baseSource = process.env.GITHUB_BASE_REF
  ? { remote: "origin", ref: process.env.GITHUB_BASE_REF }
  : process.env.GITHUB_EVENT_NAME === "workflow_dispatch"
    ? { remote: upstream, ref: "main" }
    : undefined;
const proofEnv = (side) => Object.fromEntries(proofs.map((proof) => [proof.env, side]));
// Runs the named specs against one binary and requires every case to pass.
const qualify = async (mode, binary, specs, extraEnv) => {
  // CI keeps each mode's JSON report for upload; proof screenshots ride in it
  // as base64 attachments.
  const report = process.env.CI
    ? path.join(repository, "frontend/test-results/playwright", `report-export-${mode}.json`)
    : path.join(scratch, `${mode}.json`);
  const env = {
    ...process.env,
    DOCBANK_SCREENSHOT_BINARY: binary,
    PLAYWRIGHT_JSON_OUTPUT_FILE: report,
    ...extraEnv,
  };
  const run = spawnSync(
    process.execPath,
    [
      "frontend/node_modules/@playwright/test/cli.js",
      "test",
      ...specs,
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
    { expected: specs.length, unexpected: 0, flaky: 0, skipped: 0 },
    `${mode} must execute ${specs.length} passing cases`,
  );
  console.log(`${mode}: executed=${specs.length} passed=${specs.length} skipped=0 failed=0`);
};
const base = path.join(scratch, "base");
let baseWorktree = false;
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
    const env = {
      ...process.env,
      CGO_ENABLED: cgo,
      DOCBANK_REPORT_EXPORT_SCREENSHOT_DIR: path.join(
        repository,
        ".superpowers/report-export-browser",
        mode,
      ),
      ...proofEnv("branch"),
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
      await qualify(
        mode,
        binary,
        ["report-export.screenshot.ts", ...proofs.map((proof) => proof.spec)],
        env,
      );
    } catch (error) {
      failures.push(mode);
      console.error(`${mode}: ${error.message}`);
    }
  }
  // The base capture runs the branch's proof specs on the same fixtures
  // against the base's own frontend and binary, so reviewers compare like
  // with like. It runs only in CI with a known base and at least one proof.
  if (process.env.CI && baseSource && proofs.length) {
    try {
      console.log(`Building base ${baseSource.remote} ${baseSource.ref}`);
      git("fetch", "--depth=1", baseSource.remote, baseSource.ref);
      git("worktree", "add", "--detach", base, "FETCH_HEAD");
      baseWorktree = true;
      console.log(`Base commit: ${git("-C", base, "rev-parse", "HEAD")}`);
      const binary = path.join(
        scratch,
        `docbank-base${process.platform === "win32" ? ".exe" : ""}`,
      );
      for (const [command, args] of [
        ["make", ["-C", base, "frontend"]],
        ["go", ["build", "-tags", "fts5", "-o", binary, "./cmd/docbank"]],
      ]) {
        const build = spawnSync(command, args, {
          cwd: base,
          env: { ...process.env, CGO_ENABLED: "1" },
          stdio: "inherit",
          timeout: 600_000,
        });
        if (build.error) throw build.error;
        assert.equal(build.status, 0, `base ${command} failed`);
      }
      await qualify(
        "base",
        binary,
        proofs.map((proof) => proof.spec),
        { CGO_ENABLED: "1", ...proofEnv("base") },
      );
    } catch (error) {
      failures.push("base");
      console.error(`base: ${error.message}`);
    }
  }
} finally {
  if (baseWorktree) spawnSync("git", ["worktree", "remove", "--force", base], { cwd: repository });
  await rm(scratch, { recursive: true, force: true });
}
if (failures.length) {
  throw new Error(`Browser qualification failed: ${failures.join(", ")}`);
}

// Isolated proof only: this runner is never merged into the delivery branch.
if (process.env.CI) {
  const source = "d3fd1bc9f1f0463be361dc047b93c7a4c32b80af";
  const runner = "frontend/screenshots/report-export.run.mjs";
  const allowed = [
    "internal/api/openapi.yaml",
    "internal/apiclient/client.gen.go",
    "frontend/src/generated/docbank.ts",
    "frontend/src/generated/photoSourceExtensions.ts",
  ];
  const output = path.join(
    repository,
    "frontend/test-results/playwright/report-export-api-generation.json",
  );
  const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
  const gitAt = (cwd, ...args) => execFileSync("git", args, { cwd, maxBuffer: 64 * 1024 * 1024 });
  const gitText = (cwd, ...args) =>
    gitAt(cwd, ...args)
      .toString("utf8")
      .trim();
  const names = (bytes) => bytes.toString("utf8").split("\0").filter(Boolean);
  const hashes = async (dir) =>
    Object.fromEntries(
      await Promise.all(
        allowed.map(async (file) => [file, hash(await readFile(path.join(dir, file)))]),
      ),
    );
  const report = {
    source_head: source,
    base_head: null,
    proof_head: null,
    checkout_head: commit,
    proof_parent: null,
    source_tree: null,
    proof_tree: null,
    checkout_tree: null,
    checkout_parents: [],
    allowed_paths: allowed,
    hashes_before: null,
    hashes_before_second: null,
    hashes_after_first: null,
    hashes_after_second: null,
    generated_paths: [],
    patch_base64: null,
    patch_sha256: null,
    generations: [
      {
        iteration: 1,
        status: null,
        signal: null,
        error: "not run",
        changed_paths: [],
        untracked_paths: [],
      },
      {
        iteration: 2,
        status: null,
        signal: null,
        error: "not run",
        changed_paths: [],
        untracked_paths: [],
      },
    ],
    deterministic: false,
    success: false,
    error: null,
    cleanup_errors: [],
  };
  let root;
  const worktrees = [];
  let proofError;
  try {
    assert.equal(process.env.GITHUB_EVENT_NAME, "pull_request", "pull request event required");
    const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
    const pr = event.pull_request;
    report.base_head = pr.base.sha;
    report.proof_head = pr.head.sha;
    assert.equal(pr.base.ref, "ci/timeline-e-generation2-base");
    assert.equal(pr.head.ref, "ci/timeline-e-generation2-proof");
    assert.equal(pr.base.repo.full_name, "salmonumbrella/docbank");
    assert.equal(pr.head.repo.full_name, "salmonumbrella/docbank");
    assert.equal(report.base_head, source, "PR base must be the exact source");
    assert.match(report.proof_head, /^[0-9a-f]{40}$/);
    // The checkout may be a shallow synthetic merge, not the PR head commit.
    execFileSync("git", ["fetch", "--no-tags", "--depth=1", "origin", source, report.proof_head], {
      cwd: repository,
      stdio: "inherit",
    });
    const parents = gitText(repository, "cat-file", "-p", report.proof_head)
      .split("\n")
      .filter((line) => line.startsWith("parent "))
      .map((line) => line.slice(7));
    assert.deepEqual(parents, [source], "proof must have the source as its sole parent");
    report.proof_parent = parents[0];
    report.source_tree = gitText(repository, "rev-parse", `${source}^{tree}`);
    report.proof_tree = gitText(repository, "rev-parse", `${report.proof_head}^{tree}`);
    report.checkout_tree = gitText(repository, "rev-parse", "HEAD^{tree}");
    assert.equal(report.checkout_tree, report.proof_tree, "checkout tree must equal proof tree");
    report.checkout_parents = gitText(repository, "cat-file", "-p", commit)
      .split("\n")
      .filter((line) => line.startsWith("parent "))
      .map((line) => line.slice(7));
    if (commit !== report.proof_head) {
      assert.deepEqual(
        report.checkout_parents,
        [source, report.proof_head],
        "checkout must be the PR merge",
      );
    }
    assert.equal(
      report.source_tree,
      "c11c63b55070acedf4bb8c8af4f2ce795eb26216",
      "source tree mismatch",
    );
    assert.deepEqual(
      names(gitAt(repository, "diff", "--name-only", "-z", source, report.proof_head, "--")),
      [runner],
    );
    assert.equal(
      gitText(repository, "status", "--porcelain", "--untracked-files=all"),
      "",
      "checkout must be clean",
    );
    root = await mkdtemp(path.join(tmpdir(), "docbank-e-generation-"));
    let firstPatch;
    let before;
    for (let iteration = 1; iteration <= 2; iteration++) {
      const dir = path.join(root, `source-${iteration}`);
      // Register before adding, so finally attempts cleanup even if add fails midway.
      worktrees.push(dir);
      gitAt(repository, "worktree", "add", "--detach", dir, source);
      assert.equal(gitText(dir, "rev-parse", "HEAD"), source);
      assert.equal(gitText(dir, "rev-parse", "HEAD^{tree}"), report.source_tree);
      assert.equal(gitText(dir, "status", "--porcelain", "--untracked-files=all"), "");
      // The existing CI step installed these locked dependencies; no tracked source is shared.
      await symlink(
        path.join(repository, "frontend/node_modules"),
        path.join(dir, "frontend/node_modules"),
        "dir",
      );
      const initial = await hashes(dir);
      if (iteration === 1) {
        before = initial;
        report.hashes_before = initial;
      } else {
        report.hashes_before_second = initial;
        assert.deepEqual(initial, before, "second source snapshot differs");
      }
      const result = spawnSync("make", ["generate-api"], {
        cwd: dir,
        env: process.env,
        stdio: "inherit",
      });
      const run = {
        iteration,
        status: result.status,
        signal: result.signal,
        error: result.error?.message ?? null,
      };
      report.generations[iteration - 1] = run;
      run.changed_paths = names(gitAt(dir, "diff", "--name-only", "-z", "HEAD", "--"));
      run.untracked_paths = names(gitAt(dir, "ls-files", "--others", "--exclude-standard", "-z"));
      const patch = gitAt(
        dir,
        "diff",
        "--binary",
        "--full-index",
        "--no-ext-diff",
        "--no-textconv",
        "HEAD",
        "--",
      );
      if (iteration === 1) {
        firstPatch = patch;
        report.generated_paths = run.changed_paths;
        report.patch_base64 = patch.toString("base64");
        report.patch_sha256 = hash(patch);
        report.hashes_after_first = await hashes(dir);
      } else {
        report.hashes_after_second = await hashes(dir);
        report.deterministic =
          patch.equals(firstPatch) &&
          JSON.stringify(report.hashes_after_first) === JSON.stringify(report.hashes_after_second);
      }
      assert.deepEqual(run.untracked_paths, [], "generator created untracked files");
      assert.deepEqual(
        run.changed_paths.filter((file) => !allowed.includes(file)),
        [],
        "unexpected generated paths",
      );
      for (const required of allowed.slice(0, 3)) {
        assert.ok(
          run.changed_paths.includes(required),
          `missing expected generated change: ${required}`,
        );
      }
      if (result.error) throw result.error;
      assert.equal(result.signal, null, `generation ${iteration} was signalled`);
      assert.equal(result.status, 0, `generation ${iteration} failed`);
      if (iteration === 2) assert.ok(report.deterministic, "generation outputs differ");
    }
    report.success = true;
  } catch (error) {
    report.error = error.stack ?? String(error);
    proofError = error;
  } finally {
    for (const dir of worktrees.reverse()) {
      const removed = spawnSync("git", ["worktree", "remove", "--force", dir], {
        cwd: repository,
        encoding: "utf8",
      });
      if (removed.error || removed.status !== 0) {
        report.cleanup_errors.push(
          removed.error?.message ?? removed.stderr ?? `status ${removed.status}`,
        );
      }
    }
    if (root) {
      try {
        await rm(root, { recursive: true, force: true });
      } catch (error) {
        report.cleanup_errors.push(error.message);
      }
    }
    if (report.cleanup_errors.length) {
      report.success = false;
      report.error ??= `Cleanup failed: ${report.cleanup_errors.join("; ")}`;
      proofError ??= new Error(report.error);
    }
    await mkdir(path.dirname(output), { recursive: true });
    await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  }
  if (proofError) throw proofError;
}
