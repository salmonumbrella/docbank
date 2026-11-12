import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
// Replace both placeholders only after the source owner releases the final clean head.
const source = "6ae76b1f3f825d090b448e9bb2d653582f6a2577";
const sourceTree = "350c2e298583ee7f3571da564cd393b7e631d80e";
const git = (...args) => execFileSync("git", args, { cwd: repository, encoding: "utf8" }).trim();
const commit = git("rev-parse", "HEAD");
const dirty = git("status", "--porcelain") !== "";
console.log(`Browser qualification: ${commit} (${dirty ? "dirty" : "clean"} tree)`);
// Schema evidence has its own lifecycle and report, independent of browser results.
let schemaError = null;
if (process.env.CI) {
  const report = { source, source_tree: sourceTree, base: null, proof: null, proof_parent: null,
    checkout: commit, checkout_tree: null, checkout_parents: null, proof_tree: null,
    changed_paths: null, commands: [], steps: {}, captures: [], marker: null, patch: null,
    cleanup: {}, success: false, error: null };
  const results = path.join(repository, "frontend/test-results/playwright");
  const out = path.join(results, "report-export-schema-contract.json");
  const hash = (b) => createHash("sha256").update(b).digest("hex");
  const ownerTests = ["TestStorageSchemaVersionContract", "TestStorageSchemaContractRecords", "TestStorageSchemaTokenDigest", "TestStorageSchemaCatalogChanges", "TestStorageSchemaContractChanges", "TestStorageSchemaNonUniqueIndexChanges"];
  for (const mode of ["cgo", "purego"]) {
    for (const pass of [1, 2]) report.steps[`capture ${mode} ${pass}`] = "not_run";
    for (const test of ownerTests) report.steps[`${test} ${mode}`] = "not_run";
  }
  let root = null, worktree = null, captureTest = null, markerPath = null;
  const command = (name, executable, args, options = {}) => {
    const r = spawnSync(executable, args, { cwd: repository, env: process.env, encoding: "utf8",
      timeout: 600_000, maxBuffer: 64 * 1024 * 1024, ...options });
    report.commands.push({ name, executable, argv: args, cwd: options.cwd ?? repository,
      status: r.status, signal: r.signal, error: r.error?.message ?? null,
      stdout: r.stdout ?? "", stderr: r.stderr ?? "" });
    if (Object.hasOwn(report.steps, name)) report.steps[name] = r.error || r.status !== 0 ? "failed" : "command_passed";
    if (r.error) throw r.error;
    assert.equal(r.status, 0, `${name} failed`);
    return r.stdout;
  };
  const gitAt = (cwd, ...args) => command(`git ${args[0]}`, "git", args, { cwd }).trim();
  try {
    assert.match(source, /^[0-9a-f]{40}$/, "replace final source placeholder");
    assert.match(sourceTree, /^[0-9a-f]{40}$/, "replace final tree placeholder");
    const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
    report.base = event.pull_request.base.sha; report.proof = event.pull_request.head.sha;
    assert.equal(report.base, source);
    assert.match(report.proof, /^[0-9a-f]{40}$/);
    command("fetch exact pins", "git", ["fetch", "--no-tags", "--depth=1", "origin", source, report.proof]);
    const parents = (id) => gitAt(repository, "cat-file", "-p", id).split("\n")
      .filter((line) => line.startsWith("parent ")).map((line) => line.slice(7));
    assert.equal(gitAt(repository, "rev-parse", `${source}^{tree}`), sourceTree);
    assert.deepEqual(parents(report.proof), [source]); report.proof_parent = source;
    report.checkout_parents = parents(commit);
    if (commit !== report.proof) assert.deepEqual(report.checkout_parents, [source, report.proof]);
    report.checkout_tree = gitAt(repository, "rev-parse", "HEAD^{tree}");
    report.proof_tree = gitAt(repository, "rev-parse", `${report.proof}^{tree}`);
    assert.equal(report.checkout_tree, report.proof_tree);
    report.changed_paths = gitAt(repository, "diff", "--name-only", source, report.proof, "--").split("\n").filter(Boolean);
    assert.deepEqual(report.changed_paths, ["frontend/screenshots/document-date-recording.screenshot.ts", "frontend/screenshots/report-export.run.mjs"]);
    assert.equal(gitAt(repository, "status", "--porcelain", "--untracked-files=all"), "");
    root = await mkdtemp(path.join(tmpdir(), "docbank-schema-contract-"));
    worktree = path.join(root, "source");
    command("create detached source", "git", ["worktree", "add", "--detach", worktree, source]);
    assert.equal(gitAt(worktree, "rev-parse", "HEAD"), source);
    assert.equal(gitAt(worktree, "rev-parse", "HEAD^{tree}"), sourceTree);
    assert.equal(gitAt(worktree, "status", "--porcelain", "--untracked-files=all"), "");
    const captureTarget = path.join(worktree, "internal/store/schema_contract_recording_capture_test.go");
    const capture = `package store
import ("encoding/json"; "os"; "slices"; "strings"; "testing")
func TestRecordingSchemaOwnerCapture(t *testing.T) {
 type record struct { Driver string \`json:"driver"\`; Version int \`json:"version"\`; Name string \`json:"name"\`; Contents string \`json:"contents"\` }
 var result []record
 for _, d := range v090UpgradeDrivers() {
  db := schemaContractDB(t, d.driver, schemaSQL)
  digest := schemaCatalogDigest(t, db)
  indexes := schemaIndexDigests(t, db)
  var lines []string
  for name, hash := range indexes { lines = append(lines, name+" "+hash+"\\n") }
  slices.Sort(lines)
  result = append(result, record{d.name, currentStorageSchemaVersion, schemaContractFile(schemaContract{Version: currentStorageSchemaVersion, Digest: digest}, "document-date-search"), strings.Join(lines, "")})
 }
 data, err := json.Marshal(result); if err != nil { t.Fatal(err) }
 if err := os.WriteFile(os.Getenv("DOCBANK_SCHEMA_CAPTURE"), data, 0600); err != nil { t.Fatal(err) }
}
`;
    await writeFile(captureTarget, capture, { flag: "wx" });
    captureTest = captureTarget;
    let canonical = null;
    for (const [mode, cgo, drivers] of [["cgo", "1", ["mattn/go-sqlite3", "modernc.org/sqlite"]], ["purego", "0", ["modernc.org/sqlite"]]]) {
      for (let pass = 1; pass <= 2; pass++) {
        const file = path.join(root, `${mode}-${pass}.json`);
        command(`capture ${mode} ${pass}`, "go", ["test", "-tags", "fts5", "./internal/store/", "-run", "^TestRecordingSchemaOwnerCapture$", "-count=1", "-v"],
          { cwd: worktree, env: { ...process.env, CGO_ENABLED: cgo, DOCBANK_SCHEMA_CAPTURE: file } });
        const records = JSON.parse(await readFile(file, "utf8"));
        assert.deepEqual(records.map((r) => r.driver).sort(), drivers.sort());
        for (const record of records) {
          assert.equal(record.version, 49, "provisional version must remain current");
          assert.match(record.name, /^000049-[0-9a-f]{64}-document-date-search\.schema$/);
          const value = { name: record.name, contents: record.contents };
          if (canonical === null) canonical = value; else assert.deepEqual(value, canonical);
        }
        report.captures.push({ mode, pass, records });
        report.steps[`capture ${mode} ${pass}`] = "passed";
      }
    }
    assert.ok(canonical);
    await rm(captureTest); captureTest = null;
    const relative = `internal/store/testdata/schema-contracts/${canonical.name}`;
    const markerTarget = path.join(worktree, relative);
    await mkdir(path.dirname(markerTarget), { recursive: true });
    await writeFile(markerTarget, canonical.contents, { flag: "wx" });
    markerPath = markerTarget;
    const bytes = await readFile(markerPath);
    report.marker = { path: relative, bytes: bytes.length, sha256: hash(bytes), base64: bytes.toString("base64"), text: bytes.toString("utf8") };
    for (const [mode, cgo, drivers] of [["cgo", "1", ["mattn/go-sqlite3", "modernc.org/sqlite"]], ["purego", "0", ["modernc.org/sqlite"]]]) {
      for (const test of ownerTests) {
        const log = command(`${test} ${mode}`, "go", ["test", "-tags", "fts5", "./internal/store/", "-run", `^${test}$`, "-count=1", "-v"],
          { cwd: worktree, env: { ...process.env, CGO_ENABLED: cgo } });
        assert.ok(log.includes(`--- PASS: ${test} (`), `${test} must execute and pass`);
        if (!["TestStorageSchemaContractRecords", "TestStorageSchemaTokenDigest"].includes(test)) {
          for (const driver of drivers) assert.ok(log.includes(`--- PASS: ${test}/${driver} (`), `${test}/${driver} must execute and pass`);
        }
        report.steps[`${test} ${mode}`] = "passed";
      }
    }
    const index = path.join(root, "temporary-index");
    const indexEnv = { ...process.env, GIT_INDEX_FILE: index };
    command("temporary index baseline", "git", ["read-tree", "HEAD"], { cwd: worktree, env: indexEnv });
    command("temporary index marker", "git", ["add", "--", relative], { cwd: worktree, env: indexEnv });
    const changed = command("patch paths", "git", ["diff", "--cached", "--name-only", "--", relative], { cwd: worktree, env: indexEnv }).trim();
    assert.equal(changed, relative);
    const patchText = command("binary patch", "git", ["diff", "--cached", "--binary", "--full-index", "--", relative], { cwd: worktree, env: indexEnv });
    const patch = Buffer.from(patchText, "utf8");
    const repeatedPatch = Buffer.from(command("binary patch repeat", "git", ["diff", "--cached", "--binary", "--full-index", "--", relative], { cwd: worktree, env: indexEnv }), "utf8");
    assert.ok(patch.length > 0);
    assert.ok(patch.equals(repeatedPatch), "binary patch must be deterministic");
    report.patch = { paths: [relative], bytes: patch.length, sha256: hash(patch), repeat_sha256: hash(repeatedPatch), base64: patch.toString("base64") };
    report.success = true;
  } catch (error) { schemaError = error; report.error = error.stack ?? String(error); }
  finally {
    const cleanup = { capture_removed: false, marker_removed: false, source_clean: false, worktree_removed: false, scratch_removed: false, errors: [] };
    try { if (captureTest) await rm(captureTest, { force: true }); cleanup.capture_removed = true; } catch (e) { cleanup.errors.push(String(e)); }
    try { if (markerPath) await rm(markerPath, { force: true }); cleanup.marker_removed = true; } catch (e) { cleanup.errors.push(String(e)); }
    if (worktree) {
      try { cleanup.source_clean = gitAt(worktree, "status", "--porcelain", "--untracked-files=all") === ""; } catch (e) { cleanup.errors.push(String(e)); }
      try { command("remove detached source", "git", ["worktree", "remove", "--force", worktree]); cleanup.worktree_removed = true; } catch (e) { cleanup.errors.push(String(e)); }
    } else { cleanup.source_clean = true; cleanup.worktree_removed = true; }
    try { if (root && cleanup.worktree_removed) await rm(root, { recursive: true, force: true }); cleanup.scratch_removed = !root || cleanup.worktree_removed; } catch (e) { cleanup.errors.push(String(e)); }
    report.cleanup = cleanup;
    if (!cleanup.capture_removed || !cleanup.marker_removed || !cleanup.source_clean || !cleanup.worktree_removed || !cleanup.scratch_removed || cleanup.errors.length) {
      schemaError ??= new Error("schema scratch cleanup failed"); report.success = false; report.error ??= schemaError.stack;
    }
    try {
      await mkdir(results, { recursive: true });
      await writeFile(out, `${JSON.stringify(report, null, 2)}\n`);
    } catch (error) {
      schemaError ??= error;
      console.error(`Schema report could not be written: ${error.stack ?? error}`);
    }
  }
}
const scratch = await mkdtemp(path.join(tmpdir(), "docbank-browser-build-"));
const failures = [];
const normalCleanup = { scratch_removed: false, error: null };
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
      await rm(report, { force: true });
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
  try {
    await rm(scratch, { recursive: true, force: true });
    normalCleanup.scratch_removed = true;
  } catch (error) {
    normalCleanup.error = String(error);
    failures.push(`normal scratch cleanup: ${error.message}`);
  }
}
let proofError = failures.length ? new Error(`Browser qualification failed: ${failures.join(", ")}`) : null;

// A separate branch-only recording proof; prior paired captures remain separate.
if (process.env.CI) {
  // Placeholder: owner replaces this pin after the final source is released.
  const spec = "document-date-recording.screenshot.ts";
  const allowed = [`frontend/screenshots/${spec}`, "frontend/screenshots/report-export.run.mjs"];
  const results = path.join(repository, "frontend/test-results/playwright");
  const output = path.join(results, "report-export-date-recording.json");
  const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");
  const gitBytes = (...args) => execFileSync("git", args, { cwd: repository, maxBuffer: 64 * 1024 * 1024 });
  const names = (bytes) => bytes.toString("utf8").split("\0").filter(Boolean);
  const report = {
    source_head: source, source_tree: sourceTree, base_head: null, proof_head: null,
    proof_parent: null, checkout_head: commit, checkout_tree: null, checkout_parents: null,
    allowed_paths: allowed, changed_paths: null, historical_images: {
      source: "b5124da33b136e33f00b6f5eb74d464c737d12a0",
      proof: "80e979837cc2ebe124df98c622aa684ab88a5cc9",
      checkout: "529bdfc340c1e9285859764126cd3f1a16e47d44",
      tree: "7aa428e4714cc41a6ff8d58b0928b3698aa5627a",
      baseline: "19e8ca3042316dcc47981b5b04cd1c0fc19f6d24", captures: 42, recaptured: false,
    },
    normal_cases: {}, normal_cleanup: normalCleanup, commands: [], playwright: null, receipt: null, frame_validation: null,
    encoder: null, probe: null, mp4: null, cleanup: null, success: false, error: null,
  };
  let scratchPath = null;
  let recordingError;
  const command = (name, executable, args, options = {}) => {
    const result = spawnSync(executable, args, {
      cwd: repository, env: process.env, encoding: "utf8", maxBuffer: 64 * 1024 * 1024,
      timeout: 600_000, ...options,
    });
    const entry = { name, executable, argv: args, status: result.status, signal: result.signal,
      error: result.error?.message ?? null, stdout: result.stdout ?? "", stderr: result.stderr ?? "" };
    report.commands.push(entry);
    return { result, entry };
  };
  const requireCommand = ({ result, entry }) => {
    if (result.error) throw result.error;
    assert.equal(result.status, 0, `${entry.name} failed`);
  };
  const captionStates = ["results", "filters-name", "sort-ascending-options", "sort-ascending",
    "sort-ascending-results", "filters-ascending", "sort-descending-options", "sort-descending",
    "sort-descending-results", "filters-descending", "new-search", "draft-json", "named",
    "saved-create", "edited", "saved-update", "zone-removed", "saved-noop", "saved-run"];
  const captionTexts = ["date:2026-03-10 finds 2 results", "Filters and sort: Sort by Name, Ascending",
    "Sort by lists Document date", "Sort by Document date, Ascending",
    "Ascending: evening-offset.eml, then morning-utc.eml", "Filters and sort: Document date, Ascending",
    "Direction lists Ascending and Descending", "Sort by Document date, Descending",
    "Descending: morning-utc.eml, then evening-offset.eml", "Filters and sort: Document date, Descending",
    "Searches: New search", 'JSON: "document_date", "desc", no "timezone" line', "Name: Synthetic March 10",
    'Saved Synthetic March 10. The JSON now shows "timezone": "UTC"', 'JSON edited: "direction": "asc"',
    'Saved Synthetic March 10 with "direction": "asc"', 'The "timezone" line is removed',
    'Saved Synthetic March 10. "timezone": "UTC" is back',
    "Saved search results: evening-offset.eml, then morning-utc.eml"];
  const resultStates = new Set(["results", "sort-ascending-results", "sort-descending-results", "saved-create", "saved-update", "saved-noop", "saved-run"]);
  const actionTargets = ["Advanced", "Sort by: Name", "Document date", "Search", "Advanced",
    "Direction: Ascending", "Descending", "Search", "Advanced", "Save…", "Edit as JSON",
    "Definition name", "Save as new", "Complete query JSON", "Save changes", "Complete query JSON",
    "Save changes", "Run Synthetic March 10"];
  try {
    for (const mode of ["cgo", "purego"]) {
      try {
        const normal = JSON.parse(await readFile(path.join(results, `report-export-${mode}.json`), "utf8"));
        report.normal_cases[mode] = { stats: normal.stats, errors: normal.errors };
      } catch (error) { report.normal_cases[mode] = { error: String(error) }; }
    }
    const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
    report.base_head = event.pull_request.base.sha;
    report.proof_head = event.pull_request.head.sha;
    assert.equal(report.base_head, source, "PR base must equal source");
    assert.match(report.proof_head, /^[0-9a-f]{40}$/);
    const fetch = command("fetch pins", "git", ["fetch", "--no-tags", "--depth=1", "origin", source, report.proof_head]);
    requireCommand(fetch);
    const parents = (head) => git("cat-file", "-p", head).split("\n")
      .filter((line) => line.startsWith("parent ")).map((line) => line.slice(7));
    assert.equal(git("rev-parse", `${source}^{tree}`), sourceTree, "source tree");
    assert.deepEqual(parents(report.proof_head), [source], "proof parent");
    report.proof_parent = source;
    report.checkout_parents = parents(commit);
    if (commit !== report.proof_head) assert.deepEqual(report.checkout_parents, [source, report.proof_head], "synthetic parents");
    report.checkout_tree = git("rev-parse", "HEAD^{tree}");
    assert.equal(report.checkout_tree, git("rev-parse", `${report.proof_head}^{tree}`), "checkout tree");
    report.changed_paths = names(gitBytes("diff", "--name-only", "-z", source, report.proof_head, "--"));
    assert.deepEqual(report.changed_paths, allowed, "proof paths");
    assert.equal(git("status", "--porcelain", "--untracked-files=all"), "", "proof starts clean");

    scratchPath = await mkdtemp(path.join(tmpdir(), "docbank-date-recording-proof-"));
    const captures = path.join(scratchPath, "recording");
    const binary = path.join(scratchPath, "docbank");
    const json = path.join(scratchPath, "playwright.json");
    await mkdir(captures, { recursive: true });
    const env = { ...process.env, TMPDIR: scratchPath, CGO_ENABLED: "1", DOCBANK_SCREENSHOT_BINARY: binary,
      DOCBANK_SCREENSHOT_SCALE: "2", DOCBANK_DATE_RECORDING_DIR: captures,
      DOCBANK_DATE_QUERY_TREE: "branch", PLAYWRIGHT_JSON_OUTPUT_FILE: json };
    requireCommand(command("build recording binary", "go", ["build", "-tags", "fts5", "-o", binary, "./cmd/docbank"], { env }));
    const playwright = command("record browser", process.execPath, [
      "frontend/node_modules/@playwright/test/cli.js", "test", spec,
      "--config", "frontend/screenshots/playwright.config.ts", "--project", "chromium",
      "--reporter", "line,json", "--retries", "0", "--global-timeout", String(browserGlobalTimeout),
    ], { env, timeout: browserWatchdog });
    try { report.playwright = JSON.parse(await readFile(json, "utf8")); }
    catch (error) { report.playwright = { report_error: String(error) }; }
    try { report.receipt = JSON.parse(await readFile(path.join(captures, "recording.json"), "utf8")); }
    catch (error) { report.receipt = { receipt_error: String(error) }; }
    requireCommand(playwright);
    assert.deepEqual(report.playwright.errors, []);
    const { expected, unexpected, flaky, skipped } = report.playwright.stats;
    assert.deepEqual({ expected, unexpected, flaky, skipped }, { expected: 1, unexpected: 0, flaky: 0, skipped: 0 });
    const rec = report.receipt;
    assert.equal(rec.tree, "branch");
    assert.deepEqual(rec.viewport, { width: 1440, height: 960 });
    assert.equal(rec.device_pixel_ratio, 2);
    assert.equal(rec.theme, "dark");
    assert.equal(rec.format, "jpeg");
    assert.deepEqual(rec.frame_size, { width: 2880, height: 1920 });
    assert.equal(rec.dark_check.class_dark, true);
    assert.equal(rec.dark_check.stored, "dark");
    assert.ok(rec.dark_check.background.length === 3 && rec.dark_check.background.every((channel) => channel < 80));
    assert.equal(rec.pointer.kind, "macos-arrow-svg");
    assert.deepEqual(rec.captions.map(({ state }) => state), captionStates);
    assert.deepEqual(rec.captions.map(({ text }) => text), captionTexts);
    for (const [index, caption] of rec.captions.entries()) {
      assert.equal(caption.hold, resultStates.has(caption.state) ? "result" : "step");
      assert.ok(caption.text && Number.isFinite(caption.start) && Number.isFinite(caption.end));
      assert.ok(caption.end - caption.start >= (caption.hold === "result" ? 2 : 1.2), `${caption.state} hold`);
      if (index) assert.equal(caption.start, rec.captions[index - 1].end, "caption intervals contiguous");
    }
    assert.deepEqual(rec.actions.map(({ target }) => target), actionTargets);
    for (const action of rec.actions) {
      assert.equal(action.steps, 30);
      assert.ok(action.glide_start <= action.glide_end && action.glide_end <= action.clicked_at);
      assert.ok(Math.abs(action.tip.x - action.x) < 0.5 && Math.abs(action.tip.y - action.y) < 0.5);
    }
    const asc = { field: "document_date", direction: "asc" };
    const desc = { field: "document_date", direction: "desc" };
    const fixtures = Object.fromEntries(rec.facts.fixtures.map((item) => [item.name, item.node_id]));
    const rows = [fixtures["evening-offset.eml"], fixtures["morning-utc.eml"]];
    assert.ok(rows.every(Number.isInteger) && rows[0] !== rows[1]);
    for (const [direction, sort, order] of [["ascending", asc, rows], ["descending", desc, [...rows].reverse()]]) {
      assert.equal(rec.facts.sort[direction].preview.status, 200);
      assert.deepEqual(rec.facts.sort[direction].preview.sort, sort);
      assert.equal(rec.facts.sort[direction].results.status, 200);
      assert.deepEqual(rec.facts.sort[direction].results.request_sort, sort);
      assert.deepEqual(rec.facts.sort[direction].results.rows, order);
    }
    assert.ok(rec.facts.saved.id);
    for (const [step, status, revision] of [["create", 201, 1], ["update", 200, 2], ["noop", 200, 2]]) {
      const item = rec.facts.saved[step];
      assert.equal(item.status, status); assert.equal(item.revision, revision);
      assert.equal(item.listed_revision, revision); assert.equal(item.etag, `"${revision}"`);
      assert.equal(item.payload_timezone, "UTC");
    }
    assert.deepEqual(rec.facts.saved.create.request.sort, desc);
    assert.deepEqual(rec.facts.saved.update.request.sort, asc);
    assert.deepEqual(rec.facts.saved.noop.request.sort, asc);
    assert.equal(rec.facts.saved.create.request.method, "POST");
    assert.equal(rec.facts.saved.update.request.method, "PATCH");
    assert.equal(rec.facts.saved.noop.request.method, "PATCH");
    assert.equal(rec.facts.saved.create.request.payload_timezone, null);
    assert.equal(rec.facts.saved.update.request.if_match, '"1"');
    assert.equal(rec.facts.saved.noop.request.if_match, '"2"');
    assert.equal(rec.facts.saved.noop.request.payload_timezone, null);
    assert.equal(rec.facts.run.status, 200);
    assert.deepEqual(rec.facts.run.request_sort, asc);
    assert.equal(rec.facts.run.request_timezone, "UTC");
    assert.equal(rec.facts.run.timezone, "UTC");
    assert.equal(rec.facts.run.timezone_defaulted, false);
    assert.deepEqual(rec.facts.run.rows, rows);
    assert.equal(rec.cleanup?.daemon_running_after_stop, false);
    assert.equal(rec.daemon_started, true);
    assert.equal(rec.frames_drained, true);
    assert.equal(rec.frame_error, undefined);
    assert.equal(rec.scratch_removed, true);
    assert.equal(rec.retained_vault, null);
    assert.equal(rec.error, undefined);
    assert.deepEqual(rec.cleanup_errors, []);
    assert.deepEqual(rec.cdp_cleanup, { stop_attempted: true, stopped: true,
      detach_attempted: true, detached: true, errors: [] });
    assert.ok(rec.frames.length > captionStates.length);
    assert.ok(Number.isFinite(rec.ended_at) && Number.isFinite(rec.started_at) && rec.ended_at > rec.started_at);
    const selected = [];
    for (const [index, frame] of rec.frames.entries()) {
      assert.equal(frame.file, `frames/${String(index).padStart(6, "0")}.jpg`);
      assert.ok(Number.isFinite(frame.timestamp));
      if (index) assert.ok(frame.timestamp >= rec.frames[index - 1].timestamp, "frame timestamp order");
      const bytes = await readFile(path.join(captures, frame.file));
      assert.equal(bytes.length, frame.bytes); assert.equal(sha256(bytes), frame.sha256);
      if (frame.timestamp <= rec.ended_at) selected.push(frame);
    }
    assert.ok(selected.length > 0 && selected[0].timestamp >= rec.started_at - 1);
    const duration = rec.ended_at - selected[0].timestamp;
    assert.ok(duration > 0);
    const concat = path.join(captures, "frames.ffconcat");
    const lines = ["ffconcat version 1.0"];
    for (const [index, frame] of selected.entries()) {
      lines.push(`file '${frame.file}'`);
      const next = index + 1 < selected.length ? selected[index + 1].timestamp : rec.ended_at;
      lines.push(`duration ${(next - frame.timestamp).toFixed(6)}`);
    }
    lines.push(`file '${selected.at(-1).file}'`);
    await writeFile(concat, `${lines.join("\n")}\n`);
    report.frame_validation = { total: rec.frames.length, encoded: selected.length,
      excluded_after_end: rec.frames.length - selected.length, duration, concat };
    const mp4 = path.join(scratchPath, "branch-recording.mp4");
    const encoder = command("encode H264", "ffmpeg", ["-y", "-f", "concat", "-safe", "0",
      "-i", concat, "-t", duration.toFixed(6), "-c:v", "libx264", "-pix_fmt", "yuv420p", "-r", "30", "-movflags", "+faststart", mp4]);
    report.encoder = encoder.entry;
    requireCommand(encoder);
    const bytes = await readFile(mp4);
    report.mp4 = { bytes: bytes.length, sha256: sha256(bytes), base64: bytes.toString("base64"), metadata: null };
    const probe = command("verify MP4", "ffprobe", ["-v", "error", "-show_versions", "-select_streams", "v:0", "-show_entries",
      "program_version:library_version:stream=codec_name,width,height,pix_fmt:format=duration", "-of", "json", mp4]);
    report.probe = probe.entry;
    requireCommand(probe);
    const metadata = JSON.parse(probe.result.stdout);
    assert.equal(metadata.streams.length, 1);
    assert.deepEqual(metadata.streams[0], { codec_name: "h264", width: 2880, height: 1920, pix_fmt: "yuv420p" });
    assert.ok(Math.abs(Number(metadata.format.duration) - duration) <= 0.5, "encoded duration");
    report.mp4.metadata = metadata;
    report.success = !proofError;
  } catch (error) {
    recordingError = error;
    report.error = error.stack ?? String(error);
  } finally {
    const cleanup = { scratch_path: scratchPath, scratch_removed: !scratchPath, checkout_clean: false,
      residual_vaults: [], errors: [] };
    // A killed Playwright worker cannot run its finally block. Its TMPDIR is
    // contained here, so independently stop any surviving synthetic vault.
    if (scratchPath) {
      try {
        const entries = await readdir(scratchPath, { withFileTypes: true });
        for (const item of entries.filter((entry) => entry.isDirectory() && entry.name.startsWith("docbank-date-recording-"))) {
          const vault = path.join(scratchPath, item.name, "vault");
          const env = { ...process.env, DOCBANK_HOME: vault };
          const binary = path.join(scratchPath, "docbank");
          const receipt = { vault, stopped: false, error: null };
          cleanup.residual_vaults.push(receipt);
          try {
            const status = command("residual daemon status", binary, ["daemon", "status", "--json"], { env, timeout: 90_000 });
            requireCommand(status);
            if (JSON.parse(status.result.stdout).running) {
              requireCommand(command("residual daemon stop", binary, ["daemon", "stop"], { env, timeout: 90_000 }));
            }
            const after = command("residual daemon status after stop", binary, ["daemon", "status", "--json"], { env, timeout: 90_000 });
            requireCommand(after);
            assert.equal(JSON.parse(after.result.stdout).running, false);
            receipt.stopped = true;
          } catch (error) { receipt.error = String(error); cleanup.errors.push(receipt.error); }
        }
      } catch (error) { cleanup.errors.push(String(error)); }
    }
    try {
      if (scratchPath && cleanup.errors.length === 0) {
        await rm(scratchPath, { recursive: true, force: true });
        cleanup.scratch_removed = true;
      }
    }
    catch (error) { cleanup.errors.push(String(error)); }
    try { cleanup.checkout_clean = git("status", "--porcelain", "--untracked-files=all") === ""; }
    catch (error) { cleanup.errors.push(String(error)); }
    report.cleanup = cleanup;
    if (!cleanup.scratch_removed || !cleanup.checkout_clean || cleanup.errors.length) {
      recordingError ??= new Error("recording proof cleanup failed");
      report.success = false; report.error ??= recordingError.stack;
    }
    if (proofError) { report.success = false; report.normal_error = proofError.stack; }
    await mkdir(results, { recursive: true });
    await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  }
  if (recordingError && !proofError) proofError = recordingError;
}
if (schemaError && !proofError) proofError = schemaError;
if (proofError) throw proofError;
