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
let uiError = failures.length
  ? new Error(`Browser qualification failed: ${failures.join(", ")}`)
  : null;

// This isolated CI proof branch never ships with the delivery branch.
if (process.env.CI && !uiError) {
  const source = "b5124da33b136e33f00b6f5eb74d464c737d12a0";
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
      assert.equal(entry.evidence.tree, tree);
      assert.equal(
        entry.evidence.cleanup?.daemon_running_after_stop,
        false,
        `${tree} daemon cleanup`,
      );
      assert.equal(entry.evidence.scratch_removed, true, `${tree} scratch cleanup`);
      assert.equal(entry.evidence.cleanup_error, undefined, `${tree} cleanup error`);
      const original = ["invalid-range", "date-query", "results"].flatMap((state) => [
        `${state}-desktop-light.png`,
        `${state}-desktop-dark.png`,
        `${state}-narrow-dark.png`,
      ]);
      const added = [
        "sort-options",
        "sort-ascending",
        "sort-descending",
        "saved-create",
        "saved-update",
        "saved-noop",
      ].flatMap((state) => [`${state}-desktop-dark.png`, `${state}-narrow-dark.png`]);
      assert.deepEqual(
        entry.evidence.captures.map(({ file }) => file),
        [...original, ...added],
        `${tree} capture inventory`,
      );
      for (const capture of entry.evidence.captures) {
        for (const field of ["listbox", "drawer", "sort_label", "direction_label"]) {
          assert.ok(
            Object.hasOwn(capture.measurements, field),
            `${tree} ${capture.file} missing ${field}`,
          );
        }
      }
      const { sort, saved } = entry.evidence;
      assert.deepEqual(
        sort.options,
        tree === "branch"
          ? ["Name", "Path", "Modified", "Document date", "Size", "File type"]
          : ["Name", "Path", "Modified", "Size", "File type"],
      );
      assert.equal(sort.ascending.results.status, tree === "branch" ? 200 : 422);
      assert.equal(sort.descending.preview.status, tree === "branch" ? 200 : 422);
      assert.equal(sort.descending.results.status, tree === "branch" ? 200 : 422);
      if (tree === "branch") {
        assert.equal(sort.ascending.preview.status, 200);
        assert.deepEqual(sort.ascending.preview.sort, { field: "document_date", direction: "asc" });
        assert.deepEqual(sort.descending.preview.sort, {
          field: "document_date",
          direction: "desc",
        });
        assert.deepEqual(sort.ascending.results.request_sort, {
          field: "document_date",
          direction: "asc",
        });
        assert.deepEqual(sort.descending.results.request_sort, {
          field: "document_date",
          direction: "desc",
        });
        assert.equal(sort.ascending.results.rows.length, 2);
        assert.deepEqual(sort.descending.results.rows, [...sort.ascending.results.rows].reverse());
      } else {
        assert.equal(sort.ascending.preview, null);
        assert.deepEqual(sort.descending.preview.sort, { field: "name", direction: "desc" });
        assert.equal(sort.ascending.results.rows, null);
        assert.equal(sort.descending.results.rows, null);
      }
      assert.ok(saved.id);
      for (const [step, status, revision] of [
        ["create", 201, 1],
        ["update", 200, 2],
        ["noop", 200, 2],
      ]) {
        assert.equal(saved[step].status, status, `${tree} ${step} status`);
        assert.equal(saved[step].revision, revision, `${tree} ${step} revision`);
        assert.equal(saved[step].listed_revision, revision, `${tree} ${step} listed revision`);
        assert.equal(saved[step].payload_timezone, tree === "branch" ? "UTC" : null);
      }
      assert.equal(saved.create.request.payload_timezone, null);
      assert.equal(saved.noop.request.payload_timezone, null);
      assert.equal(saved.update.request.if_match, '\"1\"');
      assert.equal(saved.noop.request.if_match, '\"2\"');
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
    const cleanup = {
      worktree_removed: !worktree,
      scratch_removed: false,
      checkout_clean: false,
      errors: [],
    };
    try {
      if (worktree) git("worktree", "remove", "--force", baselineTree);
      cleanup.worktree_removed = !git("worktree", "list", "--porcelain").includes(baselineTree);
    } catch (cause) {
      cleanup.errors.push(cause.stack ?? String(cause));
    }
    try {
      await rm(proofScratch, { recursive: true, force: true });
      cleanup.scratch_removed = true;
    } catch (cause) {
      cleanup.errors.push(cause.stack ?? String(cause));
    }
    try {
      cleanup.checkout_clean = git("status", "--porcelain", "--untracked-files=all") === "";
    } catch (cause) {
      cleanup.errors.push(cause.stack ?? String(cause));
    }
    report.cleanup = cleanup;
    if (
      cleanup.errors.length ||
      !cleanup.worktree_removed ||
      !cleanup.scratch_removed ||
      !cleanup.checkout_clean
    ) {
      report.success = false;
      proofError ??= new Error("UI proof cleanup failed");
      report.error ??= proofError.stack;
    }
    await mkdir(results, { recursive: true });
    await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  }
  if (proofError) uiError = proofError;
  else if (
    !report.cleanup.worktree_removed ||
    !report.cleanup.scratch_removed ||
    !report.cleanup.checkout_clean
  )
    uiError = new Error("UI proof cleanup failed");
}

// A normal browser failure prevents the UI proof, but still leaves an explicit receipt.
if (process.env.CI && failures.length) {
  const output = path.join(
    repository,
    "frontend/test-results/playwright/report-export-ui-proof.json",
  );
  await mkdir(path.dirname(output), { recursive: true });
  await writeFile(
    output,
    `${JSON.stringify({ success: false, error: uiError.stack, normal_failures: failures, ui_proof: "not_run" }, null, 2)}\n`,
  );
}

// Qualification runs in a separate, disposable source checkout, never either UI tree.
if (process.env.CI) {
  const source = "b5124da33b136e33f00b6f5eb74d464c737d12a0";
  const allowed = [
    "internal/formatqualification/qualification.go",
    "internal/formatqualification/qualification_test.go",
    "internal/processing/coverage_test.go",
    "document/testdata/format_coverage.json",
    "document/format_coverage_codec_test.go",
  ];
  const output = path.join(
    repository,
    "frontend/test-results/playwright/report-export-qualification.json",
  );
  const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");
  const report = {
    source_head: source,
    base_head: null,
    proof_head: null,
    proof_parent: null,
    checkout_head: commit,
    checkout_tree: null,
    synthetic_parents: [],
    scratch_head: null,
    scratch_tree: null,
    upstream_main: "403e6d87eb94f8d35eed5b9389936d85d004405b",
    allowed_paths: allowed,
    capture: null,
    commands: [],
    hashes_before: null,
    hashes_after_literals: null,
    hashes_after_first: null,
    hashes_after_second: null,
    patch_base64: null,
    patch_sha256: null,
    second_patch_sha256: null,
    deterministic: false,
    inspection: null,
    cleanup: null,
    success: false,
    error: null,
  };
  const scratch = await mkdtemp(path.join(tmpdir(), "docbank-v23-qualification-"));
  const checkout = path.join(scratch, "source");
  const capturePath = path.join(scratch, "capture.json");
  const temporary = "internal/processing/ci1_identity_capture_test.go";
  const fixtureIDs = [
    "calendar",
    "docx",
    "eml",
    "gif",
    "jpeg",
    "mp3",
    "mp4",
    "pdf",
    "png",
    "pptx",
    "tiff",
    "webp",
    "xlsx",
  ];
  const gitAt = (cwd, ...args) => execFileSync("git", args, { cwd, maxBuffer: 64 * 1024 * 1024 });
  const gitText = (cwd, ...args) =>
    gitAt(cwd, ...args)
      .toString("utf8")
      .trim();
  const names = (bytes) => bytes.toString("utf8").split("\0").filter(Boolean);
  const hashes = async () =>
    Object.fromEntries(
      await Promise.all(
        allowed.map(async (file) => [file, sha256(await readFile(path.join(checkout, file)))]),
      ),
    );
  const inspect = () => ({
    changed: names(gitAt(checkout, "diff", "--name-only", "-z", "HEAD", "--")),
    untracked: names(gitAt(checkout, "ls-files", "--others", "--exclude-standard", "-z")),
  });
  const guard = (allowTemporary = false) => {
    const state = inspect();
    report.inspection = state;
    assert.deepEqual(
      state.changed.filter((file) => !allowed.includes(file)),
      [],
      "non-allowlisted tracked change",
    );
    assert.deepEqual(
      state.untracked.filter((file) => !(allowTemporary && file === temporary)),
      [],
      "unexpected untracked files",
    );
    return state;
  };
  const run = (name, pkg, tests, extra = []) => {
    const args = [
      "test",
      "-tags",
      "fts5",
      "-count=1",
      "-json",
      pkg,
      "-run",
      `^(${tests.join("|")})$`,
      ...extra,
    ];
    const result = spawnSync("go", args, {
      cwd: checkout,
      env: { ...process.env, DOCBANK_CI1_CAPTURE: capturePath },
      encoding: "utf8",
      maxBuffer: 64 * 1024 * 1024,
      timeout: 600_000,
    });
    const entry = {
      name,
      args,
      status: result.status,
      signal: result.signal,
      error: result.error?.message ?? null,
      stdout: result.stdout ?? "",
      stderr: result.stderr ?? "",
      passed: [],
      verified: false,
    };
    report.commands.push(entry);
    for (const line of entry.stdout.split("\n")) {
      if (!line) continue;
      try {
        const event = JSON.parse(line);
        if (event.Action === "pass" && event.Test) entry.passed.push(event.Test);
      } catch {
        /* Preserve non-JSON diagnostics in the complete log. */
      }
    }
    if (result.error) throw result.error;
    assert.equal(result.status, 0, `${name} failed`);
    for (const test of tests)
      assert.ok(entry.passed.includes(test), `${name} did not execute ${test}`);
    entry.verified = true;
    return entry;
  };
  let attached = false;
  let qualificationError;
  try {
    const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
    report.base_head = event.pull_request.base.sha;
    report.proof_head = event.pull_request.head.sha;
    assert.equal(report.base_head, source);
    assert.match(report.proof_head, /^[0-9a-f]{40}$/);
    execFileSync("git", ["fetch", "--no-tags", "--depth=1", "origin", source, report.proof_head], {
      cwd: repository,
      stdio: "inherit",
    });
    const parents = (head) =>
      gitText(repository, "cat-file", "-p", head)
        .split("\n")
        .filter((line) => line.startsWith("parent "))
        .map((line) => line.slice(7));
    assert.deepEqual(parents(report.proof_head), [source]);
    report.proof_parent = source;
    report.synthetic_parents = parents(commit);
    if (commit !== report.proof_head)
      assert.deepEqual(
        report.synthetic_parents,
        [source, report.proof_head],
        "synthetic checkout parents",
      );
    report.checkout_tree = gitText(repository, "rev-parse", "HEAD^{tree}");
    assert.equal(
      report.checkout_tree,
      gitText(repository, "rev-parse", `${report.proof_head}^{tree}`),
    );
    assert.deepEqual(
      names(gitAt(repository, "diff", "--name-only", "-z", source, report.proof_head, "--")),
      [
        "frontend/screenshots/document-date-query.screenshot.ts",
        "frontend/screenshots/report-export.run.mjs",
      ],
    );
    assert.equal(gitText(repository, "status", "--porcelain", "--untracked-files=all"), "");
    gitAt(repository, "worktree", "add", "--detach", checkout, source);
    attached = true;
    report.scratch_head = gitText(checkout, "rev-parse", "HEAD");
    report.scratch_tree = gitText(checkout, "rev-parse", "HEAD^{tree}");
    assert.equal(report.scratch_head, source);
    assert.equal(report.scratch_tree, "a0a81d7c6544497e760c42f07c32e387583adeb2");
    guard();
    report.hashes_before = await hashes();
    await writeFile(
      path.join(checkout, temporary),
      `package processing

import (
  "encoding/json"
  "os"
  "testing"
  "go.kenn.io/docbank/internal/emailmime"
)

func TestCI1IdentityCapture(t *testing.T) {
  recipe := emailmime.Recipe()
  values := map[string]string{"implementation": sourceMetadataImplementationID(sourceMetadataExtractorDescriptor, recipe), "current": fingerprintSourceMetadataExtractor(sourceMetadataExtractorDescriptor, recipe)}
  for _, version := range []string{"go1.27.0", "go1.27.1"} {
    recipe.GoVersion = version
    values[version] = fingerprintSourceMetadataExtractor(sourceMetadataExtractorDescriptor, recipe)
    if sourceMetadataImplementationID(sourceMetadataExtractorDescriptor, recipe) != values["implementation"] { t.Fatal("implementation depends on toolchain") }
  }
  data, err := json.Marshal(values); if err != nil { t.Fatal(err) }
  if err := os.WriteFile(os.Getenv("DOCBANK_CI1_CAPTURE"), data, 0600); err != nil { t.Fatal(err) }
}
`,
    );
    guard(true);
    run("capture identity", "./internal/processing/", ["TestCI1IdentityCapture"]);
    report.capture = JSON.parse(await readFile(capturePath, "utf8"));
    for (const key of ["implementation", "current", "go1.27.0", "go1.27.1"])
      assert.match(report.capture[key], /^[0-9a-f]{64}$/);
    await rm(path.join(checkout, temporary));
    guard();
    const replaceOnce = async (file, pattern, value) => {
      const full = path.join(checkout, file);
      const text = await readFile(full, "utf8");
      const matches = [...text.matchAll(pattern)];
      assert.equal(matches.length, 1, `${file}: expected one literal`);
      await writeFile(
        full,
        text.replace(pattern, (_, prefix, suffix) => prefix + value + suffix),
      );
    };
    await replaceOnce(
      allowed[0],
      /(sourceMetadataExtractorID = ")[0-9a-f]{64}("\n)/g,
      report.capture.implementation,
    );
    await replaceOnce(
      allowed[1],
      /(const testExtractorFingerprint = ")[0-9a-f]{64}("\n)/g,
      report.capture.implementation,
    );
    await replaceOnce(
      allowed[4],
      /(const qualifiedMetadataFingerprint = ")[0-9a-f]{64}("\n)/g,
      report.capture.implementation,
    );
    for (const version of ["go1.27.0", "go1.27.1"]) {
      await replaceOnce(
        allowed[2],
        new RegExp(`(\\{"${version.replaceAll(".", "\\.")}", ")[0-9a-f]{64}("\\},)`, "g"),
        report.capture[version],
      );
    }
    guard();
    report.hashes_after_literals = await hashes();
    const fixtures = run("registered metadata fixtures", "./internal/processing/", [
      "TestSourceMetadataQualificationsExecuteRegisteredFixtures",
    ]);
    fixtures.verified = false;
    for (const id of fixtureIDs)
      assert.ok(
        fixtures.passed.includes(`TestSourceMetadataQualificationsExecuteRegisteredFixtures/${id}`),
        `fixture ${id} did not pass`,
      );
    fixtures.verified = true;
    run("original metadata fixture owners", "./internal/processing/", [
      "TestExtractSourceMetadataFromSyntheticFormats",
      "TestExtractSourceMetadataFromSyntheticDOCX",
      "TestExtractSourceMetadataFromSyntheticPPTX",
      "TestExtractSourceMetadataFromSyntheticXLSX",
      "TestExtractSourceMetadataReadsVisualContainerFacts",
      "TestExtractSourceMetadataReadsMP4CreationTime",
      "TestExtractSourceMetadataUsesAuthoritativePDFInfo",
      "TestExtractSourceMetadataReadsTIFFPhotoFacts",
      "TestExtractID3TextEncodingsAndFrameBoundary",
    ]);
    run("metadata and coverage regressions", "./internal/processing/", [
      "TestMetadataQualificationSurvivesToolchainChanges",
      "TestMetadataQualificationRejectsParserChanges",
      "TestSourceMetadataExtractorFingerprintIncludesEmailRecipe",
      "TestFormatCoverageComposesQualifiedOwnersWithoutInflatingClaims",
      "TestFormatCoverageUsesExecutedSyntheticProviderQualification",
      "TestFormatCoverageRequiresTheActiveMetadataExtractorQualification",
      "TestLookupFormatDistinguishesCatalogPendingAndUnknown",
    ]);
    run("qualification registry", "./internal/formatqualification/", [
      "TestLookupRequiresTheExactQualifiedTuple",
      "TestQualificationsReturnsACopy",
    ]);
    run("coverage contract", "./document/", [
      "TestFormatCoverageCanonicalRoundTripAndRejections",
      "TestFormatCoverageValidationRequiresCompleteRegisteredClaims",
      "TestFormatCoverageValidationBindsProviderEvidenceToItsDescriptor",
      "TestFormatCoverageValidationKeepsOrderedProviderAlternatives",
      "TestDecodeFormatCoverageReturnsDeepCanonicalCopy",
    ]);
    run("format API lookup", "./internal/api/", [
      "TestFormatCapabilitiesFiltersAndLooksUp",
      "TestFormatCapabilitiesLookupUsesFullSnapshotBeforeFamilyFilter",
      "TestFormatCapabilitiesSnapshotIsFrozenAndDeepCopied",
    ]);
    guard();
    let firstPatch;
    for (let iteration = 1; iteration <= 2; iteration++) {
      run(
        `coverage update ${iteration}`,
        "./internal/processing/",
        ["TestFormatCoverageMatchesPinnedFixture"],
        ["-args", "-update"],
      );
      guard();
      const patch = gitAt(
        checkout,
        "diff",
        "--binary",
        "--no-ext-diff",
        "--no-textconv",
        "HEAD",
        "--",
      );
      if (iteration === 1) {
        firstPatch = patch;
        report.hashes_after_first = await hashes();
        report.patch_base64 = patch.toString("base64");
        report.patch_sha256 = sha256(patch);
      } else {
        report.hashes_after_second = await hashes();
        report.second_patch_sha256 = sha256(patch);
        report.deterministic =
          patch.equals(firstPatch) && report.patch_sha256 === report.second_patch_sha256;
        assert.deepEqual(
          report.hashes_after_second,
          report.hashes_after_first,
          "output bytes changed",
        );
        assert.ok(report.deterministic, "binary patch changed");
      }
    }
    assert.deepEqual(
      [...guard().changed].sort(),
      [...allowed].sort(),
      "expected five refreshed paths",
    );
    report.success = true;
  } catch (error) {
    qualificationError = error;
    report.error = error.stack ?? String(error);
  } finally {
    const cleanup = {
      temporary_removed: false,
      inspection: null,
      worktree_removed: !attached,
      scratch_removed: false,
      errors: [],
    };
    try {
      attached ||= gitText(repository, "worktree", "list", "--porcelain")
        .split("\n")
        .includes(`worktree ${checkout}`);
      cleanup.worktree_removed = !attached;
    } catch (error) {
      cleanup.errors.push(String(error));
    }
    if (attached) {
      try {
        await rm(path.join(checkout, temporary), { force: true });
        cleanup.temporary_removed = true;
      } catch (error) {
        cleanup.errors.push(String(error));
      }
      try {
        cleanup.inspection = guard();
      } catch (error) {
        cleanup.errors.push(String(error));
      }
      try {
        gitAt(repository, "worktree", "remove", "--force", checkout);
        cleanup.worktree_removed = !gitText(repository, "worktree", "list", "--porcelain").includes(
          checkout,
        );
      } catch (error) {
        cleanup.errors.push(String(error));
      }
    } else cleanup.temporary_removed = true;
    try {
      await rm(scratch, { recursive: true, force: true });
      cleanup.scratch_removed = true;
    } catch (error) {
      cleanup.errors.push(String(error));
    }
    report.cleanup = cleanup;
    report.named_status = Object.fromEntries(
      [
        "capture identity",
        "registered metadata fixtures",
        "original metadata fixture owners",
        "metadata and coverage regressions",
        "qualification registry",
        "coverage contract",
        "format API lookup",
        "coverage update 1",
        "coverage update 2",
      ].map((name) => {
        const command = report.commands.find((entry) => entry.name === name);
        return [name, command ? (command.verified ? "passed" : "failed") : "not_run"];
      }),
    );
    if (
      cleanup.errors.length ||
      !cleanup.worktree_removed ||
      !cleanup.scratch_removed ||
      !cleanup.temporary_removed
    ) {
      report.success = false;
      qualificationError ??= new Error("qualification cleanup failed");
      report.error ??= qualificationError.stack;
    }
    await mkdir(path.dirname(output), { recursive: true });
    await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  }
  if (qualificationError && !uiError) uiError = qualificationError;
}
if (uiError) throw uiError;
