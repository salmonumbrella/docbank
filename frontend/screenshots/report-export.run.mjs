import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, open, readFile, readdir, rename, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
// Replace both placeholders only after the source owner releases the final clean head.
const source = "b9fe6428dedccf01dd3c3556d25883a84b8b85fe";
const sourceTree = "7670b591a344a8f5c5733eeeedf87d431ac67eb6";
const git = (...args) => execFileSync("git", args, { cwd: repository, encoding: "utf8" }).trim();
const commit = git("rev-parse", "HEAD");
const dirty = git("status", "--porcelain") !== "";
console.log(`Browser qualification: ${commit} (${dirty ? "dirty" : "clean"} tree)`);
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
let proofError = failures.length
  ? new Error(`Browser qualification failed: ${failures.join(", ")}`)
  : null;

// A separate branch-only recording proof; prior paired captures remain separate.
if (process.env.CI) {
  // Placeholder: owner replaces this pin after the final source is released.
  const spec = "document-date-recording.screenshot.ts";
  const allowed = [`frontend/screenshots/${spec}`, "frontend/screenshots/report-export.run.mjs"];
  const results = path.join(repository, "frontend/test-results/playwright");
  const output = path.join(results, "report-export-date-recording.json");
  const bundle = path.join(results, "report-export-date-recording-frames.json");
  const bundlePartial = `${bundle}.partial`;
  const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");
  const gitBytes = (...args) =>
    execFileSync("git", args, { cwd: repository, maxBuffer: 64 * 1024 * 1024 });
  const names = (bytes) => bytes.toString("utf8").split("\0").filter(Boolean);
  const report = {
    source_head: source,
    source_tree: sourceTree,
    base_head: null,
    proof_head: null,
    proof_parent: null,
    checkout_head: commit,
    checkout_tree: null,
    checkout_parents: null,
    allowed_paths: allowed,
    changed_paths: null,
    historical_images: {
      source: "b5124da33b136e33f00b6f5eb74d464c737d12a0",
      proof: "80e979837cc2ebe124df98c622aa684ab88a5cc9",
      checkout: "529bdfc340c1e9285859764126cd3f1a16e47d44",
      tree: "7aa428e4714cc41a6ff8d58b0928b3698aa5627a",
      baseline: "19e8ca3042316dcc47981b5b04cd1c0fc19f6d24",
      captures: 42,
      recaptured: false,
    },
    normal_cases: {},
    normal_cleanup: normalCleanup,
    commands: [],
    original_evidence: null,
    playwright: null,
    receipt: null,
    frame_validation: null,
    frame_bundle: null,
    encoder: null,
    probe: null,
    mp4: null,
    cleanup: null,
    success: false,
    error: null,
  };
  let scratchPath = null;
  let recordingError;
  let retainable = null;
  const command = (name, executable, args, options = {}) => {
    const result = spawnSync(executable, args, {
      cwd: repository,
      env: process.env,
      encoding: "utf8",
      maxBuffer: 64 * 1024 * 1024,
      timeout: 600_000,
      ...options,
    });
    const entry = {
      name,
      executable,
      argv: args,
      status: result.status,
      signal: result.signal,
      error: result.error?.message ?? null,
      stdout: result.stdout ?? "",
      stderr: result.stderr ?? "",
    };
    report.commands.push(entry);
    return { result, entry };
  };
  const requireCommand = ({ result, entry }) => {
    if (result.error) throw result.error;
    assert.equal(result.status, 0, `${entry.name} failed`);
  };
  const captionStates = [
    "results",
    "filters-name",
    "sort-ascending-options",
    "sort-ascending",
    "sort-ascending-results",
    "filters-ascending",
    "sort-descending-options",
    "sort-descending",
    "sort-descending-results",
    "filters-descending",
    "new-search",
    "draft-json",
    "named",
    "saved-create",
    "edited",
    "saved-update",
    "zone-removed",
    "saved-noop",
    "saved-run",
  ];
  const captionTexts = [
    "date:2026-03-10 finds 2 results",
    "Filters and sort: Sort by Name, Ascending",
    "Sort by lists Document date",
    "Sort by Document date, Ascending",
    "Ascending: evening-offset.eml, then morning-utc.eml",
    "Filters and sort: Document date, Ascending",
    "Direction lists Ascending and Descending",
    "Sort by Document date, Descending",
    "Descending: morning-utc.eml, then evening-offset.eml",
    "Filters and sort: Document date, Descending",
    "Searches: New search",
    'JSON: "document_date", "desc", no "timezone" line',
    "Name: Synthetic March 10",
    'Saved Synthetic March 10. The JSON now shows "timezone": "UTC"',
    'JSON edited: "direction": "asc"',
    'Saved Synthetic March 10 with "direction": "asc"',
    'The "timezone" line is removed',
    'Saved Synthetic March 10. "timezone": "UTC" is back',
    "Saved search results: evening-offset.eml, then morning-utc.eml",
  ];
  const resultStates = new Set([
    "results",
    "sort-ascending-results",
    "sort-descending-results",
    "saved-create",
    "saved-update",
    "saved-noop",
    "saved-run",
  ]);
  const actionTargets = [
    "Advanced",
    "Sort by: Name",
    "Document date",
    "Search",
    "Advanced",
    "Direction: Ascending",
    "Descending",
    "Search",
    "Advanced",
    "Save…",
    "Edit as JSON",
    "Definition name",
    "Save as new",
    "Complete query JSON",
    "Save changes",
    "Complete query JSON",
    "Save changes",
    "Run Synthetic March 10",
  ];
  try {
    try {
      await rm(bundle, { force: true });
      await rm(bundlePartial, { force: true });
    } catch (error) {
      report.frame_bundle = { path: bundle, error: `stale bundle removal: ${String(error)}` };
      throw error;
    }
    for (const mode of ["cgo", "purego"]) {
      try {
        const normal = JSON.parse(
          await readFile(path.join(results, `report-export-${mode}.json`), "utf8"),
        );
        report.normal_cases[mode] = { stats: normal.stats, errors: normal.errors };
      } catch (error) {
        report.normal_cases[mode] = { error: String(error) };
      }
    }
    const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
    report.base_head = event.pull_request.base.sha;
    report.proof_head = event.pull_request.head.sha;
    assert.equal(report.base_head, source, "PR base must equal source");
    assert.match(report.proof_head, /^[0-9a-f]{40}$/);
    const fetch = command("fetch pins", "git", [
      "fetch",
      "--no-tags",
      "--depth=1",
      "origin",
      source,
      report.proof_head,
    ]);
    requireCommand(fetch);
    const parents = (head) =>
      git("cat-file", "-p", head)
        .split("\n")
        .filter((line) => line.startsWith("parent "))
        .map((line) => line.slice(7));
    assert.equal(git("rev-parse", `${source}^{tree}`), sourceTree, "source tree");
    assert.deepEqual(parents(report.proof_head), [source], "proof parent");
    report.proof_parent = source;
    report.checkout_parents = parents(commit);
    if (commit !== report.proof_head)
      assert.deepEqual(report.checkout_parents, [source, report.proof_head], "synthetic parents");
    report.checkout_tree = git("rev-parse", "HEAD^{tree}");
    assert.equal(
      report.checkout_tree,
      git("rev-parse", `${report.proof_head}^{tree}`),
      "checkout tree",
    );
    report.changed_paths = names(
      gitBytes("diff", "--name-only", "-z", source, report.proof_head, "--"),
    );
    assert.deepEqual(report.changed_paths, allowed, "proof paths");
    assert.equal(git("status", "--porcelain", "--untracked-files=all"), "", "proof starts clean");

    scratchPath = await mkdtemp(path.join(tmpdir(), "docbank-date-recording-proof-"));
    const captures = path.join(scratchPath, "recording");
    const bare = path.join(scratchPath, "evidence.git");
    const evidenceTree = path.join(scratchPath, "evidence");
    const archiveName = "proof28-artifact.zip";
    // Replace only after the reviewed evidence orphan is published.
    const evidenceCommit = "5ff4be97ad2d7484b67a4f1c9490e74a74c7fc50";
    const originalProof = "8046b3af6aa08f19f564ae2461a19fa2feef688a";
    const originalCheckout = "4b79fbe04b4c2e7b584fa034f36299a059e2348e";
    assert.match(evidenceCommit, /^[0-9a-f]{40}$/, "replace evidence placeholder");
    await mkdir(captures, { recursive: true });
    await mkdir(evidenceTree, { recursive: true });
    requireCommand(command("init evidence repository", "git", ["init", "--bare", bare]));
    requireCommand(
      command("fetch evidence commit", "git", [
        "--git-dir",
        bare,
        "fetch",
        "--no-tags",
        "--depth=1",
        "https://github.com/salmonumbrella/docbank.git",
        evidenceCommit,
      ]),
    );
    const evidenceGit = (...args) => {
      const result = command(`evidence git ${args[0]}`, "git", ["--git-dir", bare, ...args]);
      requireCommand(result);
      return result.result.stdout.trim();
    };
    assert.equal(evidenceGit("rev-parse", "FETCH_HEAD^{commit}"), evidenceCommit);
    const evidenceParents = evidenceGit("cat-file", "-p", evidenceCommit)
      .split("\n\n", 1)[0]
      .split("\n")
      .filter((line) => line.startsWith("parent "));
    assert.deepEqual(evidenceParents, [], "evidence orphan");
    const tree = evidenceGit("ls-tree", "-r", "--full-tree", evidenceCommit);
    assert.match(tree, /^100644 blob [0-9a-f]{40}\tproof28-artifact\.zip$/);
    requireCommand(
      command("checkout evidence archive", "git", [
        "--git-dir",
        bare,
        "--work-tree",
        evidenceTree,
        "checkout",
        evidenceCommit,
        "--",
        archiveName,
      ]),
    );
    const archive = path.join(evidenceTree, archiveName);
    const archiveBytes = await readFile(archive);
    assert.equal(archiveBytes.length, 69_857_068);
    assert.equal(
      sha256(archiveBytes),
      "86e71afd5f8a38d8df028614eebd12389c1b2e7d769ae79426462de48d8892c5",
    );
    const listing = command("list evidence archive", "unzip", ["-Z", "-1", archive]);
    requireCommand(listing);
    assert.deepEqual(listing.result.stdout.trim().split("\n").sort(), [
      "report-export-cgo.json",
      "report-export-date-recording-frames.json",
      "report-export-date-recording.json",
      "report-export-purego.json",
    ]);
    requireCommand(
      command("extract original evidence", "unzip", [
        "-o",
        archive,
        "report-export-date-recording.json",
        "report-export-date-recording-frames.json",
        "-d",
        evidenceTree,
      ]),
    );
    const originalBytes = await readFile(
      path.join(evidenceTree, "report-export-date-recording.json"),
    );
    assert.equal(originalBytes.length, 2_149_366);
    assert.equal(
      sha256(originalBytes),
      "847d6c3be9d55431f00f53a2e7ec7c10201e6b69c9696b947054b655b57fef11",
    );
    const original = JSON.parse(originalBytes);
    assert.equal(original.source_head, source);
    assert.equal(original.source_tree, sourceTree);
    assert.equal(original.base_head, source);
    assert.equal(original.proof_head, originalProof);
    assert.equal(original.proof_parent, source);
    assert.equal(original.checkout_head, originalCheckout);
    assert.equal(original.checkout_tree, "c110ba196610a4e877c6ba92ad649578ccb6dd17");
    assert.deepEqual(original.checkout_parents, [source, originalProof]);
    assert.deepEqual(original.changed_paths, allowed);
    assert.equal(original.cleanup.scratch_removed, true);
    assert.equal(original.cleanup.checkout_clean, true);
    assert.deepEqual(original.cleanup.residual_vaults, []);
    assert.deepEqual(original.cleanup.errors, []);
    const sidecarBytes = await readFile(
      path.join(evidenceTree, "report-export-date-recording-frames.json"),
    );
    assert.equal(sidecarBytes.length, 116_190_589);
    assert.equal(
      sha256(sidecarBytes),
      "72d2b681f2efabd11d6b02b195b66ba4a0d537daafd72cbf688e878c7258f9c2",
    );
    const sidecar = JSON.parse(sidecarBytes);
    assert.equal(sidecar.checkout_head, originalCheckout);
    assert.equal(sidecar.proof_head, originalProof);
    assert.equal(sidecar.frames.length, 903);
    assert.equal(original.receipt.frames.length, 903);
    assert.equal(original.frame_bundle.frames, 903);
    assert.equal(original.frame_bundle.frame_bytes, 87_052_201);
    assert.equal(original.frame_bundle.bytes, sidecarBytes.length);
    assert.equal(original.frame_bundle.sha256, sha256(sidecarBytes));
    assert.equal(original.frame_bundle.error, null);
    await mkdir(path.join(captures, "frames"), { recursive: true });
    let totalFrameBytes = 0;
    for (const [index, frame] of sidecar.frames.entries()) {
      const name = `frames/${String(index).padStart(6, "0")}.jpg`;
      const expected = original.receipt.frames[index];
      assert.equal(frame.file, name);
      assert.equal(expected.file, name);
      const bytes = Buffer.from(frame.base64, "base64");
      assert.equal(bytes.toString("base64"), frame.base64);
      assert.equal(bytes.length, frame.bytes);
      assert.equal(frame.bytes, expected.bytes);
      assert.equal(sha256(bytes), frame.sha256);
      assert.equal(frame.sha256, expected.sha256);
      totalFrameBytes += bytes.length;
      await writeFile(path.join(captures, name), bytes, { flag: "wx" });
    }
    assert.equal(totalFrameBytes, 87_052_201);
    report.original_evidence = {
      source_head: original.source_head,
      source_tree: original.source_tree,
      proof_head: originalProof,
      checkout_head: originalCheckout,
      checkout_tree: original.checkout_tree,
      checkout_parents: original.checkout_parents,
      evidence_commit: evidenceCommit,
      archive_sha256: sha256(archiveBytes),
      original_report_sha256: sha256(originalBytes),
      sidecar_sha256: sha256(sidecarBytes),
      original_cleanup: original.cleanup,
      frames: sidecar.frames.length,
    };
    report.playwright = original.playwright;
    report.receipt = original.receipt;
    assert.deepEqual(report.playwright.errors, []);
    const { expected, unexpected, flaky, skipped } = report.playwright.stats;
    assert.deepEqual(
      { expected, unexpected, flaky, skipped },
      { expected: 1, unexpected: 0, flaky: 0, skipped: 0 },
    );
    const rec = report.receipt;
    assert.equal(rec.tree, "branch");
    assert.deepEqual(rec.viewport, { width: 1440, height: 960 });
    assert.equal(rec.device_pixel_ratio, 2);
    assert.equal(rec.theme, "dark");
    assert.equal(rec.format, "jpeg");
    assert.deepEqual(rec.frame_size, { width: 1440, height: 960 });
    assert.equal(rec.dark_check.class_dark, true);
    assert.equal(rec.dark_check.stored, "dark");
    assert.ok(
      rec.dark_check.background.length === 3 &&
        rec.dark_check.background.every((channel) => channel < 80),
    );
    assert.equal(rec.pointer.kind, "macos-arrow-svg");
    assert.deepEqual(
      rec.captions.map(({ state }) => state),
      captionStates,
    );
    assert.deepEqual(
      rec.captions.map(({ text }) => text),
      captionTexts,
    );
    for (const [index, caption] of rec.captions.entries()) {
      assert.equal(caption.hold, resultStates.has(caption.state) ? "result" : "step");
      assert.ok(caption.text && Number.isFinite(caption.start) && Number.isFinite(caption.end));
      assert.ok(
        caption.end - caption.start >= (caption.hold === "result" ? 2 : 1.2),
        `${caption.state} hold`,
      );
      if (index)
        assert.equal(caption.start, rec.captions[index - 1].end, "caption intervals contiguous");
    }
    assert.deepEqual(
      rec.actions.map(({ target }) => target),
      actionTargets,
    );
    for (const action of rec.actions) {
      assert.equal(action.steps, 30);
      assert.ok(action.glide_start <= action.glide_end && action.glide_end <= action.clicked_at);
      assert.ok(Math.abs(action.tip.x - action.x) < 0.5 && Math.abs(action.tip.y - action.y) < 0.5);
    }
    const asc = { field: "document_date", direction: "asc" };
    const desc = { field: "document_date", direction: "desc" };
    const fixtures = Object.fromEntries(
      rec.facts.fixtures.map((item) => [item.name, item.node_id]),
    );
    const rows = [fixtures["evening-offset.eml"], fixtures["morning-utc.eml"]];
    assert.ok(rows.every(Number.isInteger) && rows[0] !== rows[1]);
    for (const [direction, sort, order] of [
      ["ascending", asc, rows],
      ["descending", desc, [...rows].reverse()],
    ]) {
      assert.equal(rec.facts.sort[direction].preview.status, 200);
      assert.deepEqual(rec.facts.sort[direction].preview.sort, sort);
      assert.equal(rec.facts.sort[direction].results.status, 200);
      assert.deepEqual(rec.facts.sort[direction].results.request_sort, sort);
      assert.deepEqual(rec.facts.sort[direction].results.rows, order);
    }
    assert.ok(rec.facts.saved.id);
    for (const [step, status, revision] of [
      ["create", 201, 1],
      ["update", 200, 2],
      ["noop", 200, 2],
    ]) {
      const item = rec.facts.saved[step];
      assert.equal(item.status, status);
      assert.equal(item.revision, revision);
      assert.equal(item.listed_revision, revision);
      assert.equal(item.etag, `"${revision}"`);
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
    assert.deepEqual(rec.cdp_cleanup, {
      stop_attempted: true,
      stopped: true,
      detach_attempted: true,
      detached: true,
      errors: [],
    });
    assert.ok(rec.frames.length > captionStates.length);
    assert.ok(
      Number.isFinite(rec.ended_at) &&
        Number.isFinite(rec.started_at) &&
        rec.ended_at > rec.started_at,
    );
    const selected = [];
    for (const [index, frame] of rec.frames.entries()) {
      assert.equal(frame.file, `frames/${String(index).padStart(6, "0")}.jpg`);
      assert.ok(Number.isFinite(frame.timestamp));
      if (index)
        assert.ok(frame.timestamp >= rec.frames[index - 1].timestamp, "frame timestamp order");
      const bytes = await readFile(path.join(captures, frame.file));
      assert.equal(bytes.length, frame.bytes);
      assert.equal(sha256(bytes), frame.sha256);
      if (frame.timestamp <= rec.ended_at) selected.push(frame);
    }
    retainable = { captures, frames: rec.frames.map(({ bytes, sha256 }) => ({ bytes, sha256 })) };
    assert.ok(selected.length > 0 && selected[0].timestamp >= rec.started_at - 1);
    const duration = rec.ended_at - selected[0].timestamp;
    assert.ok(duration > 0);
    const concat = path.join(captures, "frames.ffconcat");
    const lines = ["ffconcat version 1.0"];
    for (const [index, frame] of selected.entries()) {
      lines.push(`file '${frame.file}'`);
      lines.push("option framerate 1000");
      const next = index + 1 < selected.length ? selected[index + 1].timestamp : rec.ended_at;
      lines.push(`duration ${(next - frame.timestamp).toFixed(6)}`);
    }
    lines.push(`file '${selected.at(-1).file}'`);
    lines.push("option framerate 1000");
    await writeFile(concat, `${lines.join("\n")}\n`);
    report.frame_validation = {
      total: rec.frames.length,
      encoded: selected.length,
      excluded_after_end: rec.frames.length - selected.length,
      duration,
      concat,
    };
    const aptOptions = [
      "-o",
      "Acquire::Retries=1",
      "-o",
      "Acquire::http::Timeout=15",
      "-o",
      "Acquire::https::Timeout=15",
    ];
    requireCommand(
      command(
        "apt update",
        "kit-retry",
        ["3", "5", "sudo", "apt-get", ...aptOptions, "-o", "APT::Update::Error-Mode=any", "update"],
        { timeout: 120_000 },
      ),
    );
    requireCommand(
      command(
        "install ffmpeg",
        "kit-retry",
        [
          "3",
          "5",
          "sudo",
          "apt-get",
          ...aptOptions,
          "install",
          "--no-install-recommends",
          "-y",
          "ffmpeg",
        ],
        { timeout: 240_000 },
      ),
    );
    requireCommand(
      command("ffmpeg package version", "dpkg-query", [
        "-W",
        "-f",
        "${Package} ${Version}\n",
        "ffmpeg",
      ]),
    );
    const mp4 = path.join(scratchPath, "branch-recording.mp4");
    const encoder = command(
      "encode H264",
      "ffmpeg",
      [
        "-y",
        "-f",
        "concat",
        "-safe",
        "0",
        "-i",
        concat,
        "-t",
        duration.toFixed(6),
        "-vf",
        `tpad=stop_mode=clone:stop_duration=${(rec.ended_at - selected.at(-1).timestamp).toFixed(6)},scale=in_range=full:out_range=limited,format=yuv420p`,
        "-c:v",
        "libx264",
        "-pix_fmt",
        "yuv420p",
        "-r",
        "30",
        "-movflags",
        "+faststart",
        mp4,
      ],
      { timeout: 300_000 },
    );
    report.encoder = encoder.entry;
    requireCommand(encoder);
    const bytes = await readFile(mp4);
    report.mp4 = {
      bytes: bytes.length,
      sha256: sha256(bytes),
      base64: bytes.toString("base64"),
      metadata: null,
    };
    const probe = command(
      "verify MP4",
      "ffprobe",
      [
        "-v",
        "error",
        "-show_versions",
        "-select_streams",
        "v:0",
        "-show_entries",
        "program_version:library_version:stream=codec_name,width,height,pix_fmt:format=duration",
        "-of",
        "json",
        mp4,
      ],
      { timeout: 60_000 },
    );
    report.probe = probe.entry;
    requireCommand(probe);
    const metadata = JSON.parse(probe.result.stdout);
    assert.equal(metadata.streams.length, 1);
    assert.deepEqual(metadata.streams[0], {
      codec_name: "h264",
      width: 1440,
      height: 960,
      pix_fmt: "yuv420p",
    });
    assert.ok(Math.abs(Number(metadata.format.duration) - duration) <= 0.5, "encoded duration");
    report.mp4.metadata = metadata;
    report.success = !proofError;
  } catch (error) {
    recordingError = error;
    report.error = error.stack ?? String(error);
  } finally {
    if (retainable && !report.mp4?.metadata) {
      let handle = null;
      try {
        await mkdir(results, { recursive: true });
        handle = await open(bundlePartial, "wx");
        const hash = createHash("sha256");
        let bundleBytes = 0;
        let frameBytes = 0;
        const append = async (text) => {
          const chunk = Buffer.from(text);
          await handle.writeFile(chunk);
          hash.update(chunk);
          bundleBytes += chunk.length;
        };
        await append(
          `${JSON.stringify({ checkout_head: report.checkout_head, proof_head: report.proof_head }).slice(0, -1)},"frames":[\n`,
        );
        for (const [index, frame] of retainable.frames.entries()) {
          const file = `frames/${String(index).padStart(6, "0")}.jpg`;
          const bytes = await readFile(path.join(retainable.captures, file));
          assert.equal(bytes.length, frame.bytes, `frame ${index} length`);
          assert.equal(sha256(bytes), frame.sha256, `frame ${index} hash`);
          frameBytes += bytes.length;
          await append(
            `${index ? "," : ""}${JSON.stringify({ file, bytes: bytes.length, sha256: frame.sha256, base64: bytes.toString("base64") })}\n`,
          );
        }
        await append("]}\n");
        await handle.close();
        handle = null;
        await rename(bundlePartial, bundle);
        report.frame_bundle = {
          path: bundle,
          frames: retainable.frames.length,
          frame_bytes: frameBytes,
          bytes: bundleBytes,
          sha256: hash.digest("hex"),
          error: null,
        };
      } catch (error) {
        let detail = String(error);
        if (handle) {
          try {
            await handle.close();
          } catch (closeError) {
            detail += `; close: ${String(closeError)}`;
          }
        }
        try {
          await rm(bundlePartial, { force: true });
        } catch (removeError) {
          detail += `; partial removal: ${String(removeError)}`;
        }
        report.frame_bundle = { path: bundle, error: detail };
        recordingError ??= error;
        report.success = false;
        report.error ??= error.stack ?? String(error);
      }
    }
    const cleanup = {
      scratch_path: scratchPath,
      scratch_removed: !scratchPath,
      checkout_clean: false,
      residual_vaults: [],
      errors: [],
    };
    // A killed Playwright worker cannot run its finally block. Its TMPDIR is
    // contained here, so independently stop any surviving synthetic vault.
    if (scratchPath) {
      try {
        const entries = await readdir(scratchPath, { withFileTypes: true });
        for (const item of entries.filter(
          (entry) => entry.isDirectory() && entry.name.startsWith("docbank-date-recording-"),
        )) {
          const vault = path.join(scratchPath, item.name, "vault");
          const env = { ...process.env, DOCBANK_HOME: vault };
          const binary = path.join(scratchPath, "docbank");
          const receipt = { vault, stopped: false, error: null };
          cleanup.residual_vaults.push(receipt);
          try {
            const status = command(
              "residual daemon status",
              binary,
              ["daemon", "status", "--json"],
              { env, timeout: 90_000 },
            );
            requireCommand(status);
            if (JSON.parse(status.result.stdout).running) {
              requireCommand(
                command("residual daemon stop", binary, ["daemon", "stop"], {
                  env,
                  timeout: 90_000,
                }),
              );
            }
            const after = command(
              "residual daemon status after stop",
              binary,
              ["daemon", "status", "--json"],
              { env, timeout: 90_000 },
            );
            requireCommand(after);
            assert.equal(JSON.parse(after.result.stdout).running, false);
            receipt.stopped = true;
          } catch (error) {
            receipt.error = String(error);
            cleanup.errors.push(receipt.error);
          }
        }
      } catch (error) {
        cleanup.errors.push(String(error));
      }
    }
    try {
      if (scratchPath && cleanup.errors.length === 0) {
        await rm(scratchPath, { recursive: true, force: true });
        cleanup.scratch_removed = true;
      }
    } catch (error) {
      cleanup.errors.push(String(error));
    }
    try {
      cleanup.checkout_clean = git("status", "--porcelain", "--untracked-files=all") === "";
    } catch (error) {
      cleanup.errors.push(String(error));
    }
    report.cleanup = cleanup;
    if (!cleanup.scratch_removed || !cleanup.checkout_clean || cleanup.errors.length) {
      recordingError ??= new Error("recording proof cleanup failed");
      report.success = false;
      report.error ??= recordingError.stack;
    }
    if (proofError) {
      report.success = false;
      report.normal_error = proofError.stack;
    }
    await mkdir(results, { recursive: true });
    await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  }
  if (recordingError && !proofError) proofError = recordingError;
}
if (proofError) throw proofError;
