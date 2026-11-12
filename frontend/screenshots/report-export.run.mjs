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
let carrierError;
// Qualification runs in a separate, disposable source checkout, never either UI tree.
if (process.env.CI) {
  const source = "d169cb229b85592e85fd40611dd869d405a3e5ef";
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
    checkout_status_before: null,
    checkout_status_after: null,
    output_bytes_first: null,
    output_bytes_second: null,
    patch_second_base64: null,
    mode_errors: [],
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
  let scratch;
  let checkout;
  let capturePath;
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
  const gitAt = (cwd, ...args) => {
    const result = spawnSync("git", args, { cwd, maxBuffer: 64 * 1024 * 1024, timeout: 600_000 });
    const entry = { name: "git", executable: "git", args, cwd, timeout_ms: 600_000, status: result.status, signal: result.signal, error: result.error?.message ?? null, stdout_base64: (result.stdout ?? Buffer.alloc(0)).toString("base64"), stderr: (result.stderr ?? Buffer.alloc(0)).toString("utf8") };
    report.commands.push(entry);
    if (result.error) throw result.error;
    assert.equal(result.status, 0, `git ${args[0]} failed`);
    assert.equal(result.signal, null, `git ${args[0]} signalled`);
    return result.stdout;
  };
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
  const run = (name, pkg, tests, extra = [], cgo = "1", required = tests) => {
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
      env: { ...process.env, DOCBANK_CI1_CAPTURE: capturePath, CGO_ENABLED: cgo, GOWORK: "off" },
      encoding: "utf8",
      maxBuffer: 64 * 1024 * 1024,
      timeout: 600_000,
    });
    const entry = {
      name,
      executable: "go",
      args,
      cwd: checkout,
      env_overrides: { DOCBANK_CI1_CAPTURE: capturePath, CGO_ENABLED: cgo, GOWORK: "off" },
      timeout_ms: 600_000,
      status: result.status,
      signal: result.signal,
      error: result.error?.message ?? null,
      stdout: result.stdout ?? "",
      stderr: result.stderr ?? "",
      events: [],
      parse_errors: [],
      started: [],
      passed: [],
      failed: [],
      skipped: [],
      verified: false,
    };
    report.commands.push(entry);
    for (const line of entry.stdout.split("\n")) {
      if (!line) continue;
      try {
        const event = JSON.parse(line);
        entry.events.push(event);
        if (event.Test && event.Action === "run") entry.started.push(event.Test);
        if (event.Test && event.Action === "pass") entry.passed.push(event.Test);
        if (event.Test && event.Action === "fail") entry.failed.push(event.Test);
        if (event.Test && event.Action === "skip") entry.skipped.push(event.Test);
      } catch {
        entry.parse_errors.push(line);
      }
    }
    if (result.error) throw result.error;
    assert.equal(result.status, 0, `${name} failed`);
    assert.equal(result.signal, null, `${name} signalled`);
    assert.deepEqual(entry.parse_errors, [], `${name} emitted non-JSON output`);
    assert.deepEqual(entry.failed, [], `${name} had failing tests`);
    assert.deepEqual(entry.skipped, [], `${name} skipped tests`);
    for (const test of required) {
      assert.ok(entry.started.includes(test), `${name} did not start ${test}`);
      assert.ok(entry.passed.includes(test), `${name} did not pass ${test}`);
    }
    entry.verified = true;
    return entry;
  };
  let attached = false;
  let qualificationError;
  try {
    scratch = await mkdtemp(path.join(tmpdir(), "docbank-email-qualification-"));
    checkout = path.join(scratch, "source");
    capturePath = path.join(scratch, "capture.json");
    const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
    report.base_head = event.pull_request.base.sha;
    report.proof_head = event.pull_request.head.sha;
    assert.equal(report.base_head, source);
    assert.match(report.proof_head, /^[0-9a-f]{40}$/);
    gitAt(repository, "fetch", "--no-tags", "--depth=1", "origin", source, report.proof_head);
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
        "frontend/screenshots/report-export.run.mjs",
      ],
    );
    assert.equal(gitText(repository, "rev-parse", `${source}^{tree}`), "d41b498057d5791cf55dc72de9460d1f3a16bd79");
    report.checkout_status_before = gitText(repository, "status", "--porcelain", "--untracked-files=all");
    assert.equal(report.checkout_status_before, "");
    gitAt(repository, "worktree", "add", "--detach", checkout, source);
    attached = true;
    report.scratch_head = gitText(checkout, "rev-parse", "HEAD");
    report.scratch_tree = gitText(checkout, "rev-parse", "HEAD^{tree}");
    assert.equal(report.scratch_head, source);
    assert.equal(report.scratch_tree, "d41b498057d5791cf55dc72de9460d1f3a16bd79");
    guard();
    report.hashes_before = await hashes();
    const version = spawnSync("go", ["version"], { cwd: checkout, encoding: "utf8", timeout: 600_000 });
    report.commands.push({ name: "go version", executable: "go", args: ["version"], cwd: checkout, status: version.status, signal: version.signal, error: version.error?.message ?? null, stdout: version.stdout ?? "", stderr: version.stderr ?? "", verified: !version.error && version.status === 0 && version.signal === null });
    assert.ok(report.commands.at(-1).verified, "go version failed");
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
        report.output_bytes_first = Object.fromEntries(await Promise.all(allowed.map(async (file) => [file, (await readFile(path.join(checkout, file))).toString("base64")])));
        report.patch_base64 = patch.toString("base64");
        report.patch_sha256 = sha256(patch);
      } else {
        report.hashes_after_second = await hashes();
        report.output_bytes_second = Object.fromEntries(await Promise.all(allowed.map(async (file) => [file, (await readFile(path.join(checkout, file))).toString("base64")])));
        report.patch_second_base64 = patch.toString("base64");
        assert.deepEqual(report.output_bytes_second, report.output_bytes_first, "output bytes changed");
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
    // Both supported modes run independently; failures are retained while the other mode runs.
    const emailCases = ["weekday_mismatch", "unknown_weekday", "invalid_calendar", "ambiguous_duplicate", "valid_weekday_compact_utc", "colon_positive_zero", "colon_negative_zero", "compact_negative_zero", "colon_offset", "compact_offset", "colon_limit", "compact_limit", "short_numeric_one_digit", "short_numeric_two_digits", "colon_out_of_range", "compact_out_of_range", "rfc_fixed_zone", "unknown_zone", "military_zone", "no_zone", "fraction_two_digit_year_comment"];
    const selections = [
      ["email dates", "./internal/processing/", ["TestEmailDateZones", "TestInvalidEmailOffsetRetainedThroughMetadata", "TestEmailDateExtractionToEvent"], ["TestEmailDateZones", "TestInvalidEmailOffsetRetainedThroughMetadata", "TestEmailDateExtractionToEvent", ...emailCases.map((name) => `TestEmailDateExtractionToEvent/${name}`)]],
      ["retained email adapters", "./internal/processing/", ["TestAdaptersDeriveRetainedRawDatesFromExtractor", "TestAdaptersRecoverRawEmailMinutePrecisionAndUnknownZone", "TestAdaptersRecoverCanonicalLegacyRawEmailClaims", "TestAdaptersRejectMalformedOffsetWithoutInventingAnInstant", "TestRebuildReusesRetainedIANAInstant"], null],
      ["event retention", "./internal/store/", ["TestDocumentEventDeriverFingerprint", "TestPublishDocumentEventsIsIdempotentAndDetectsCorruption", "TestDocumentEventEvidenceLoadsExactBindingAndChangesWithMTime", "TestChangedEvidenceRevokesPublishedDocumentEvents"], null],
      ["qualification fixtures", "./internal/processing/", ["TestSourceMetadataQualificationsExecuteRegisteredFixtures", "TestFormatCoverageMatchesPinnedFixture", "TestMetadataQualificationSurvivesToolchainChanges", "TestSourceMetadataExtractorFingerprintIncludesEmailRecipe"], ["TestSourceMetadataQualificationsExecuteRegisteredFixtures", ...fixtureIDs.map((id) => `TestSourceMetadataQualificationsExecuteRegisteredFixtures/${id}`), "TestFormatCoverageMatchesPinnedFixture", "TestMetadataQualificationSurvivesToolchainChanges", "TestSourceMetadataExtractorFingerprintIncludesEmailRecipe"]],
      ["qualification registry", "./internal/formatqualification/", ["TestLookupRequiresTheExactQualifiedTuple", "TestQualificationsReturnsACopy"], null],
      ["qualification codec", "./document/", ["TestFormatCoverageCanonicalRoundTripAndRejections", "TestFormatCoverageValidationRequiresCompleteRegisteredClaims", "TestFormatCoverageValidationBindsProviderEvidenceToItsDescriptor"], null],
    ];
    for (const cgo of ["1", "0"]) {
      for (const [name, pkg, tests, required] of selections) {
        try { run(`${name} CGO_ENABLED=${cgo}`, pkg, tests, [], cgo, required ?? tests); }
        catch (error) { report.mode_errors.push({ cgo, name, error: error.stack ?? String(error) }); }
      }
    }
    assert.deepEqual(report.mode_errors, [], "one or more verification commands failed");
    guard();
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
      attached ||= !!checkout && gitText(repository, "worktree", "list", "--porcelain")
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
      if (cleanup.worktree_removed) {
        if (scratch) await rm(scratch, { recursive: true, force: true });
        cleanup.scratch_removed = true;
      } else cleanup.errors.push("scratch retained because worktree removal was not verified");
    } catch (error) {
      cleanup.errors.push(String(error));
    }
    try {
      report.checkout_status_after = gitText(repository, "status", "--porcelain", "--untracked-files=all");
      assert.equal(gitText(repository, "rev-parse", "HEAD"), commit);
      assert.equal(report.checkout_status_after, report.checkout_status_before);
    } catch (error) { cleanup.errors.push(String(error)); }
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
  if (qualificationError) carrierError = qualificationError;
}
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
if (carrierError) throw carrierError;
