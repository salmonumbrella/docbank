#!/usr/bin/env bash
# Exercise fork tag acquisition without network access or publishing tags.
set -euo pipefail
script=$(cd "$(dirname "$0")" && pwd)/check-released-schemas.sh
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_AUTHOR_NAME=Test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=Test GIT_COMMITTER_EMAIL=test@example.invalid
export REAL_GIT
REAL_GIT=$(command -v git)
export FIXTURE_UPSTREAM="$scratch/upstream" FETCH_LOG="$scratch/fetch.log"
mkdir -p "$FIXTURE_UPSTREAM/internal/store/testdata" "$scratch/bin"
git init -q "$FIXTURE_UPSTREAM"
cd "$FIXTURE_UPSTREAM"
printf 'const currentStorageSchemaVersion = 3\n' > internal/store/store.go
printf 'var releasedStorageSchemaVersions = []int{3}\n' > internal/store/upgrade.go
printf 'CREATE TABLE example(id INTEGER);\n' > internal/store/schema.sql
cp internal/store/schema.sql internal/store/testdata/schema-v0.10.0.sql
git add . && git commit -qm fixture && git tag v0.10.0
git clone -q --no-tags "$FIXTURE_UPSTREAM" "$scratch/fork"
cat > "$scratch/bin/git" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
if [[ $1 == fetch ]]; then
	[[ $# == 4 && $2 == --no-tags && $3 == https://github.com/kenn-io/docbank.git && $4 == 'refs/tags/v*:refs/tags/v*' ]]
	printf 'fetch\n' >> "$FETCH_LOG"
	exec "$REAL_GIT" fetch --no-tags "$FIXTURE_UPSTREAM" "$4"
fi
exec "$REAL_GIT" "$@"
SHIM
chmod +x "$scratch/bin/git"
export PATH="$scratch/bin:$PATH"
cd "$scratch/fork"
# Local runs still require the caller to supply full release history.
if GITHUB_ACTIONS=false GITHUB_REPOSITORY=salmonumbrella/docbank bash "$script" > "$scratch/local.log" 2>&1; then
	echo 'tagless local run unexpectedly passed' >&2; exit 1
fi
grep -q 'no release tags found' "$scratch/local.log"
[[ ! -e $FETCH_LOG ]]
# Fork CI obtains exactly the canonical release refs, then validates them.
GITHUB_ACTIONS=true GITHUB_REPOSITORY=salmonumbrella/docbank bash "$script"
[[ $(wc -l < "$FETCH_LOG") == 1 ]]
[[ $(git rev-parse v0.10.0) == $(git -C "$FIXTURE_UPSTREAM" rev-parse v0.10.0) ]]
# Upstream CI and ordinary local execution never fetch.
GITHUB_ACTIONS=true GITHUB_REPOSITORY=kenn-io/docbank bash "$script"
GITHUB_ACTIONS=false GITHUB_REPOSITORY=salmonumbrella/docbank bash "$script"
[[ $(wc -l < "$FETCH_LOG") == 1 ]]
# A fetched release still must have its exact compatible schema fixture.
printf 'different\n' > internal/store/testdata/schema-v0.10.0.sql
if GITHUB_ACTIONS=true GITHUB_REPOSITORY=salmonumbrella/docbank bash "$script" > "$scratch/mismatch.log" 2>&1; then
	echo 'incompatible fixture unexpectedly passed' >&2; exit 1
fi
grep -q 'no exact fixture' "$scratch/mismatch.log"
echo 'released-schema fork regression tests passed'
