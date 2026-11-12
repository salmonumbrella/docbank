import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { access, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const git = (...args) => execFileSync("git", args, { cwd: repository, encoding: "utf8" }).trim();
const commit = git("rev-parse", "HEAD");
const dirty = git("status", "--porcelain") !== "";
console.log(`Browser qualification: ${commit} (${dirty ? "dirty" : "clean"} tree)`);
// CI-only dependency generation proof for B. Two pristine worktrees of the
// exact B source each apply the same reviewed input (A's complete go.mod and
// go.sum, and the merged kit-ui pin), then generate go.mod/go.sum with
// go mod tidy and frontend/package-lock.json with a lock-only, script-free
// npm install. Every command, output byte and hash goes to
// report-export-b-modules.json in the existing upload; nothing is committed.
let moduleError;
if (process.env.CI) {
  const source = "f018cc5f2ed9f5502f45def23adb3426809f69fd";
  const sourceTree = "7d9fa3224504578840613c84009c1a49afd2a3b2";
  const goModulesFrom = "19e8ca3042316dcc47981b5b04cd1c0fc19f6d24";
  const kitUICommit = "b2ac6ae3910659b9c75476737034f8c305c3a714";
  const patchBase64 =
    "ZGlmZiAtLWdpdCBhL2Zyb250ZW5kL3BhY2thZ2UuanNvbiBiL2Zyb250ZW5kL3BhY2thZ2UuanNvbgppbmRleCBkMzMyMDljYjA3NzFhZTBlNjA1NDBkOTI5OWU4ODNkYTFkOGRiNDE1Li5hNmEyYTIyZWY5MGQ1ODM3NzJkNjI2MTI4NDljY2YxNTk2ZDlmOTUwIDEwMDY0NAotLS0gYS9mcm9udGVuZC9wYWNrYWdlLmpzb24KKysrIGIvZnJvbnRlbmQvcGFja2FnZS5qc29uCkBAIC0yMSw3ICsyMSw3IEBACiAgICJkZXBlbmRlbmNpZXMiOiB7CiAgICAgIkBlbWJlZHBkZi9wZGZpdW0iOiAiMi4xNS4xIiwKICAgICAiQGVtYmVkcGRmL3N2ZWx0ZS1wZGYtdmlld2VyIjogIjIuMTUuMSIsCi0gICAgIkBrZW5uLWlvL2tpdC11aSI6ICJnaXQraHR0cHM6Ly9naXRodWIuY29tL2tlbm4taW8va2l0LXVpLmdpdCM2ZWI0OWJhYjZjOGMxNmU4MWU1MjJmMzA4M2M1YWRiNDNjMWQ5NTFlIiwKKyAgICAiQGtlbm4taW8va2l0LXVpIjogImdpdCtodHRwczovL2dpdGh1Yi5jb20va2Vubi1pby9raXQtdWkuZ2l0I2IyYWM2YWUzOTEwNjU5YjljNzU0NzY3MzcwMzRmOGMzMDVjM2E3MTQiLAogICAgICJAbHVjaWRlL3N2ZWx0ZSI6ICIxLjY0LjAiLAogICAgICJAbm9ibGUvaGFzaGVzIjogIl4yLjIuMCIsCiAgICAgImxlYWZsZXQiOiAiMS45LjQiLApkaWZmIC0tZ2l0IGEvZ28ubW9kIGIvZ28ubW9kCmluZGV4IDRjMTQwNTJlMTE0NmNiOTVkNjVjZWI0ZDhkOGM1NWE5YjIzNGFmMDUuLmY2OTYwZTkwMTY2YjkwM2FiNjhmZjYxZDBmMDc3YzAyMzYwNDZkYjMgMTAwNjQ0Ci0tLSBhL2dvLm1vZAorKysgYi9nby5tb2QKQEAgLTE1MSw1ICsxNTEsMyBAQCByZXF1aXJlICgKICkKIAogdG9vbCBnb3Rlc3QudG9vbHMvZ290ZXN0c3VtCi0KLXJlcGxhY2UgZ28ua2Vubi5pby9raXQgPT4gZ2l0aHViLmNvbS9zYWxtb251bWJyZWxsYS9raXQgdjAuMzAuMi0wLjIwMjYxMTEyMDM1NDM1LTk1NWJjYTgzMTcxOApkaWZmIC0tZ2l0IGEvZ28uc3VtIGIvZ28uc3VtCmluZGV4IGYzZGE5N2FkMjczYjNjY2RhMzJkZjdkNmI4ZmNhNGZmMjkwZjFkOWUuLjI3MWQ1OTkzYjM4MTk0MzMzMTljYzMwM2RjYTE4Y2I1ODU1YjQyNDMgMTAwNjQ0Ci0tLSBhL2dvLnN1bQorKysgYi9nby5zdW0KQEAgLTI4OCw4ICsyODgsNiBAQCBnaXRodWIuY29tL3Jpdm8vdW5pc2VnIHYwLjQuNy9nby5tb2QgaDE6Rk4zU3ZyTStaZGoxNmp5TGZtT2tNTmJsWE1jb2M4RGZUSHJ1Q1BVYwogZ2l0aHViLmNvbS9yb2dwZXBwZS9nby1pbnRlcm5hbCB2MS4xNi4wIGgxOk85REsrdk5NRFZHTHIyQmVacW1wTGVNamlNTmt1WGZjcW50V2JaVjZTNWc9CiBnaXRodWIuY29tL3JvZ3BlcHBlL2dvLWludGVybmFsIHYxLjE2LjAvZ28ubW9kIGgxOkRyVVZaeXJKVSt0eFlXNS8xa3d0WFFTTUZpbzUyWk94WDd5TTFWSHZueHM9CiBnaXRodWIuY29tL3J1c3Nyb3NzL2JsYWNrZnJpZGF5L3YyIHYyLjEuMC9nby5tb2QgaDE6K1JteGd5OUt6SlZlUzkvMmdYSHh5bHFYaXlRRFlSeENWejU1am1lT1dUTT0KLWdpdGh1Yi5jb20vc2FsbW9udW1icmVsbGEva2l0IHYwLjMwLjItMC4yMDI2MTExMjAzNTQzNS05NTViY2E4MzE3MTggaDE6SXNoQjNrNWZLZzFXRURnSzVZSTVPd2poOXNqMGVLQy9FQURDd3p4UVRJVT0KLWdpdGh1Yi5jb20vc2FsbW9udW1icmVsbGEva2l0IHYwLjMwLjItMC4yMDI2MTExMjAzNTQzNS05NTViY2E4MzE3MTgvZ28ubW9kIGgxOkRiUS91RjlUZFcwSkkwNzVQUk1ZRmVDMllvQW15RWxpQUNic1F4RHhWZjg9CiBnaXRodWIuY29tL3NhbnRob3NoLXRla3VyaS9qc29uc2NoZW1hL3Y2IHY2LjAuMyBoMToxRVlCNUl6alphd3JybkVMVWk3OGY5ZlB1NTdIdVhqbWRkWlBqcmxzLzI4PQogZ2l0aHViLmNvbS9zYW50aG9zaC10ZWt1cmkvanNvbnNjaGVtYS92NiB2Ni4wLjMvZ28ubW9kIGgxOkpYZUwrcHM4cDcvS05NakRRazNUQ3dQcEJ5MHdZa2x5V1RmYmtJemRJRlU9CiBnaXRodWIuY29tL3NlYmRhaC9nb2xkaWUvdjIgdjIuOC4wIGgxOmRaYjl3UjhxNSsrb3BsbUVpSlQrVS81S3lvdFZEK0hOR0NBYzVnTnI4cmM9Cg==";
  const patchSHA256 = "cefae2981d0e0ac1b5a6e5fdad7233b3cade3bd0f885367fac6184bb88a17581";
  const inputPaths = ["frontend/package.json", "go.mod", "go.sum"];
  const inputBlobs = {
    "frontend/package.json": "a6a2a22ef90d583772d62612849ccf1596d9f950",
    "go.mod": "f6960e90166b903ab68ff61d0f077c0236046db3",
    "go.sum": "271d5993b3819433319cc303dca18cb5855b4243",
  };
  const inputTree = "22c7076e217320b177154597cefee6a5fa332b4b";
  const outputPaths = ["frontend/package-lock.json", "frontend/package.json", "go.mod", "go.sum"];
  const runner = "frontend/screenshots/report-export.run.mjs";
  const output = path.join(
    repository,
    "frontend/test-results/playwright/report-export-b-modules.json",
  );
  const sha = (data) => createHash("sha256").update(data).digest("hex");
  const report = {
    source,
    source_tree: sourceTree,
    go_modules_from: goModulesFrom,
    kit_ui_commit: kitUICommit,
    input_patch_sha256: patchSHA256,
    input_patch_base64: patchBase64,
    input_paths: inputPaths,
    input_blobs: inputBlobs,
    input_tree: inputTree,
    output_paths: outputPaths,
    proof: null,
    checkout: commit,
    checkout_tree: null,
    checkout_parents: [],
    proof_parents: [],
    commands: [],
    iterations: [],
    deterministic: false,
    success: false,
    error: null,
    cleanup: { worktrees: [], scratch_removed: false, errors: [] },
  };
  const command = (label, executable, args, cwd = repository, input) => {
    const result = spawnSync(executable, args, {
      cwd,
      input,
      env: { ...process.env, GOWORK: "off" },
      encoding: "utf8",
      timeout: 600_000,
      maxBuffer: 64 * 1024 * 1024,
    });
    const entry = {
      label,
      executable,
      args,
      cwd,
      status: result.status,
      signal: result.signal,
      error: result.error?.message ?? null,
      stdout: result.stdout ?? "",
      stderr: result.stderr ?? "",
    };
    report.commands.push(entry);
    if (result.error) throw result.error;
    assert.equal(result.status, 0, `${label} failed`);
    return entry.stdout;
  };
  const g = (label, cwd, ...args) => command(label, "git", args, cwd).trim();
  const names = (text) => text.split("\0").filter(Boolean).sort();
  const parents = (id) =>
    g(`raw commit ${id}`, repository, "cat-file", "-p", id)
      .split("\n\n", 1)[0]
      .split("\n")
      .filter((line) => line.startsWith("parent "))
      .map((line) => line.slice(7));
  const files = async (cwd) =>
    Object.fromEntries(
      await Promise.all(
        outputPaths.map(async (file) => {
          const data = await readFile(path.join(cwd, file));
          return [file, { sha256: sha(data), bytes: data.length, base64: data.toString("base64") }];
        }),
      ),
    );
  const digests = (snapshot) =>
    Object.fromEntries(Object.entries(snapshot).map(([file, value]) => [file, value.sha256]));
  let scratchRoot;
  try {
    const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
    report.proof = event.pull_request.head.sha;
    assert.equal(event.pull_request.base.sha, source);
    assert.match(report.proof, /^[0-9a-f]{40}$/);
    command("fetch source and proof", "git", [
      "fetch",
      "--no-tags",
      "--depth=1",
      "origin",
      source,
      report.proof,
    ]);
    assert.equal(g("source tree", repository, "rev-parse", `${source}^{tree}`), sourceTree);
    report.proof_parents = parents(report.proof);
    assert.deepEqual(report.proof_parents, [source]);
    report.checkout_parents = parents(commit);
    if (commit !== report.proof) assert.deepEqual(report.checkout_parents, [source, report.proof]);
    report.checkout_tree = g("checkout tree", repository, "rev-parse", "HEAD^{tree}");
    assert.equal(
      report.checkout_tree,
      g("proof tree", repository, "rev-parse", `${report.proof}^{tree}`),
    );
    assert.deepEqual(
      names(
        command("proof paths", "git", ["diff", "--name-only", "-z", source, report.proof, "--"]),
      ),
      [runner],
    );
    assert.equal(
      g("checkout status before", repository, "status", "--porcelain=v1", "--untracked-files=all"),
      "",
    );
    const inputPatch = Buffer.from(patchBase64, "base64");
    assert.equal(sha(inputPatch), patchSHA256);
    command("go version", "go", ["version"]);
    command("go env", "go", [
      "env",
      "-json",
      "GOFLAGS",
      "GOPROXY",
      "GOSUMDB",
      "GONOSUMDB",
      "GOPRIVATE",
      "GOTOOLCHAIN",
    ]);
    command("node version", process.execPath, ["--version"]);
    command("npm version", "npm", ["--version"]);
    scratchRoot = await mkdtemp(path.join(tmpdir(), "docbank-b-modules-"));
    const results = [];
    for (let iteration = 1; iteration <= 2; iteration++) {
      const worktree = path.join(scratchRoot, `input-${iteration}`);
      const item = {
        iteration,
        worktree,
        before: null,
        input: null,
        after: null,
        changed_paths: [],
        untracked_paths: [],
        output_tree: null,
        output_patch_sha256: null,
        output_patch_base64: null,
        kit_module: null,
        kit_ui_lock_entry: null,
      };
      report.iterations.push(item);
      command(`add worktree ${iteration}`, "git", [
        "worktree",
        "add",
        "--detach",
        worktree,
        source,
      ]);
      assert.equal(g("worktree source", worktree, "rev-parse", "HEAD"), source);
      assert.equal(
        g("worktree clean", worktree, "status", "--porcelain=v1", "--untracked-files=all"),
        "",
      );
      item.before = await files(worktree);
      command(
        "apply reviewed input",
        "git",
        ["apply", "--index", "--binary", "-"],
        worktree,
        inputPatch,
      );
      assert.deepEqual(
        names(
          command(
            "input paths",
            "git",
            ["diff", "--cached", "--name-only", "-z", "HEAD", "--"],
            worktree,
          ),
        ),
        inputPaths,
      );
      for (const file of inputPaths)
        assert.equal(g(`input blob ${file}`, worktree, "rev-parse", `:${file}`), inputBlobs[file]);
      assert.equal(g("effective input tree", worktree, "write-tree"), inputTree);
      item.input = await files(worktree);
      command("tidy module graph", "go", ["mod", "tidy"], worktree);
      item.kit_module = JSON.parse(
        command("resolved kit module", "go", ["list", "-m", "-json", "go.kenn.io/kit"], worktree),
      );
      assert.equal(item.kit_module.Replace, undefined);
      command(
        "generate package lock",
        "npm",
        ["install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund"],
        path.join(worktree, "frontend"),
      );
      await assert.rejects(access(path.join(worktree, "frontend/node_modules")));
      const lock = JSON.parse(
        await readFile(path.join(worktree, "frontend/package-lock.json"), "utf8"),
      );
      item.kit_ui_lock_entry = lock.packages["node_modules/@kenn-io/kit-ui"];
      assert.ok(item.kit_ui_lock_entry?.resolved?.endsWith(`#${kitUICommit}`));
      item.after = await files(worktree);
      item.changed_paths = names(
        command("all changed paths", "git", ["diff", "--name-only", "-z", "HEAD", "--"], worktree),
      );
      item.untracked_paths = names(
        command(
          "untracked paths",
          "git",
          ["ls-files", "--others", "--exclude-standard", "-z"],
          worktree,
        ),
      );
      assert.deepEqual(item.untracked_paths, []);
      assert.deepEqual(
        item.changed_paths.filter((file) => !outputPaths.includes(file)),
        [],
      );
      command("stage outputs", "git", ["add", "--", ...outputPaths], worktree);
      item.output_tree = g("output tree", worktree, "write-tree");
      const patch = command(
        "output binary patch",
        "git",
        [
          "diff",
          "--cached",
          "--binary",
          "--full-index",
          "--no-ext-diff",
          "HEAD",
          "--",
          ...outputPaths,
        ],
        worktree,
      );
      item.output_patch_base64 = Buffer.from(patch).toString("base64");
      item.output_patch_sha256 = sha(Buffer.from(patch));
      results.push(item);
    }
    assert.deepEqual(digests(results[0].before), digests(results[1].before));
    assert.deepEqual(digests(results[0].input), digests(results[1].input));
    assert.deepEqual(digests(results[0].after), digests(results[1].after));
    assert.equal(results[0].output_tree, results[1].output_tree);
    assert.equal(results[0].output_patch_sha256, results[1].output_patch_sha256);
    report.deterministic = true;
    report.success = true;
  } catch (error) {
    moduleError = error;
    report.error = error.stack ?? String(error);
  } finally {
    for (const item of report.iterations) {
      try {
        command(`remove worktree ${item.iteration}`, "git", [
          "worktree",
          "remove",
          "--force",
          item.worktree,
        ]);
        report.cleanup.worktrees.push({ path: item.worktree, removed: true });
      } catch (error) {
        report.cleanup.worktrees.push({ path: item.worktree, removed: false });
        report.cleanup.errors.push(error.stack ?? String(error));
      }
    }
    if (scratchRoot && report.cleanup.worktrees.every((item) => item.removed)) {
      try {
        await rm(scratchRoot, { recursive: true, force: true });
        report.cleanup.scratch_removed = true;
      } catch (error) {
        report.cleanup.errors.push(error.stack ?? String(error));
      }
    } else if (!scratchRoot) report.cleanup.scratch_removed = true;
    try {
      assert.equal(
        g("checkout status after", repository, "status", "--porcelain=v1", "--untracked-files=all"),
        "",
      );
      assert.equal(g("checkout head after", repository, "rev-parse", "HEAD"), commit);
    } catch (error) {
      report.cleanup.errors.push(error.stack ?? String(error));
    }
    if (report.cleanup.errors.length) {
      report.success = false;
      moduleError ??= new Error("B module proof cleanup failed");
    }
    await mkdir(path.dirname(output), { recursive: true });
    await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  }
}
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
if (moduleError) throw moduleError;
