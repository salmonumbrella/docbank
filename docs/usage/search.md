---
last_edited: 2026-10-27
title: Automatic and processing search
description: Choose automatic retrieval or an explicit profile and source-version set.
---

# Automatic and processing search

With no processing options, `docbank search renewal terms` tries hybrid
search when a processing profile has a query embedding binding. The CLI
resolves the current versions of all live files through the daemon; you do not
need profile names or version UUIDs for this path. The daemon enforces query
consent and index authority.

The CLI runs ordinary lexical search after the hybrid attempt. Its results
are shown when hybrid search falls back. Folders have no content version, so the
hybrid source scope cannot include them. After a successful hybrid search, the
CLI adds folder name matches from the lexical search instead. They are read
after query embedding, like the revalidated hybrid results. Folders count
toward `--limit` and replace the lowest-ranked hybrid results when the page is
full. The report is `truncated` when either search omitted a match.

Profiles and query bindings are tried in ascending name order until one
succeeds. A failure that could differ by binding moves on to the next pair:
missing consent, `processing_failed`, `processing_profile_unavailable`,
`stale_index`, or `not_found` from a binding whose vector index is not built. A failure of the whole source scope, such as
`stale_version` or `scope_too_large`, stops at once. The CLI never silently
truncates the semantic population; more than 4,096 live versions falls back
with `scope_too_large`. When every pair fails, the command reports the first
failure reason.

Some requests never try hybrid search and report no degradation:

- no profile has a query embedding binding, or processing is unavailable
- the vault has no live files
- a tag, directory, MIME, or modification filter, or `--fuzzy`
- `--limit` above 100, the processing search maximum
- query text longer than 8,192 Unicode characters, the processing maximum
- `--mode lexical` with no processing options

Cancellation and authentication failures stop the command. Any other hybrid
failure falls back to lexical results with the daemon's stable problem code as
the reason. A failure without a problem code, such as an invalid response,
reports `semantic_search_failed` with the error text. Search never grants
consent itself.

Human and `--json` output write one `degraded: REASON` line to stderr, so
stdout keeps the lexical table or JSON unchanged. `--agent` output puts the
reason in its header instead.

`--json` output depends on which search produced the results. Lexical search
returns the ordinary report with `hits`. Successful hybrid search returns the
processing report with `actual_mode`, `coverage`, and `results`, plus a
`folders` array of ordinary folder name hits. Check for `actual_mode` to tell
the shapes apart, or pass `--mode lexical` to always get the ordinary report.

After semantic retrieval, the daemon revalidates the complete report against
current live versions, including unembedded lexical matches. A match trashed or
replaced during query embedding is removed, and moved paths are refreshed. A
file-name match is removed when the file is renamed. A tag edit keeps the match.
Removed candidates make the report `truncated`, and surviving result ranks and coverage
reflect the final check. This applies to automatic and explicit semantic/hybrid
search, including when expansion and reranking are disabled.

## Choose exact versions and a profile

Explicit processing search uses rendition text (text derived from a document
version) and, where configured, vectors. It searches only the source set you
name. The CLI obtains the daemon's vault UUID and sends repeated
`--source-version` values for you:

```bash
docbank search "renewal terms" \
  --mode hybrid \
  --profile <configured-profile> \
  --source-version 11111111-1111-4111-8111-111111111111
```

Explicit processing search requires a mode, executable profile, and at least one source
version. Replace `<configured-profile>` with a name reported by `docbank
processing profiles`. A search accepts at most 4,096 unique versions and
returns at most 100 results. `--binding` is optional when the selected profile
has one embedding binding and required when it has several. Lexical and auto
modes reject `--binding`. Processing search cannot be combined with ordinary
tag, media-type, directory, or modification-time filters.

## Consent before semantic or hybrid search

`semantic` and `hybrid` send query text to the selected embedding provider.
They require an active `query_text` consent grant for that profile and provider
disclosure, even when the document's vectors already exist. Missing consent
returns `processing_consent_required`. Expired or revoked consent returns
`processing_consent_expired` or `processing_consent_revoked`. Search does not
grant consent automatically.

Preview `docbank processing plan <path-or-id> --profile <configured-profile> --json`
and review its query-text disclosure. Then either:

- Run the reviewed [processing build](document-processing.md) with
  `--plan-fingerprint <fingerprint-from-plan> --consent`. This grants the
  profile's configured operations and runs its document processing.
- Grant consent without rebuilding through
  [`POST /api/v1/processing/consent/grants`](../architecture/http-api.md#processing-consent).
  Send the plan's `selector` and its `fingerprint` as `plan_fingerprint`.
  Supply `expires_at` for a grant that expires.

These grants cover the daemon operator's use of that profile across documents
and searches. A profile with reranking includes a `query_text_and_excerpt`
grant in the same approval. Review the reranking provider and its disclosure
alongside the document and query operations. CLI grants have no expiry.
Explicit processing `lexical` and `auto` use text already stored locally. They do not require
query-text consent or call an embedding provider.

## Search documents in force on a date

Processing search evaluates owner-declared [supersession and effective dates](organizing.md#document-validity)
after ranking. It places documents in force first and preserves the relative
order in each group. Scores stay unchanged: use `rank` for display order,
because scores need not decrease with rank. Vaults without
declarations keep their existing order and scores.

Use `--as-of YYYY-MM-DD` with the explicit processing form to evaluate another
calendar date. Omission uses the daemon host's local date. Dates include both
endpoints. Search still reads current content versions; it does not retrieve
historical document contents. Add `--keep-superseded-order` to retain the
original order while receiving the same validity reasons. Both flags require
`--mode`, `--profile`, and `--source-version`; the CLI rejects them when it
would use ordinary lexical search.

```sh
docbank search renewal --mode lexical --profile private \
  --source-version 11111111-1111-4111-8111-111111111111 \
  --as-of 2024-06-01
```

Text output marks `superseded`, `expired`, and `not_yet_effective` hits. A
superseded hit names a successor path already present in the response, or its
`id:N` selector when only the direct successor identity is available. The
report separately names up to 16 terminal successors in force as guidance for
what to read next. An undated replacement applies at every evaluation date and
sets `undated: true`.

Automatic search reserves space for matching folders. If this removes an
out-of-force file hit, it clears terminal successor suggestions. When only
outdated files remain and suggestions were cleared, text output says that
successor guidance was omitted.
Status and counts describe the displayed file page. Use explicit processing search when
you need the daemon's complete bounded replacement guidance.

JSON reports always include `validity.as_of`, `status`, `declared_results`, and
`successors`. Status is `empty` for no hits, `current` when any hit is in force,
`future_only` when every hit starts later, and `outdated_only` otherwise.
`outdated_only` describes the returned page; a current document below the limit
may still exist. Each declared hit includes its state, dates, and direct
`superseded_by` node IDs. Undeclared hits omit the per-hit object.

Validity uses only current live versions inside the request's source fence.
An excluded or trashed successor cannot demote a hit or appear in its metadata.
For media-source searches, selectors constrain searched evidence; replacement
authority can use any current live version already in the request fence.
The web requests `keep_order` while it does not present validity reasons.
[MCP search](mcp.md#tool-catalog) exposes the same validity metadata and accepts
`as_of` and `superseded: "demote"` (default) or `"keep_order"`.

## Rerank search results

Add `--rerank` to send the query and candidate excerpts to the profile's
[configured reranking provider](../configuration.md#search-reranking):

```bash
docbank search "renewal terms" \
  --mode hybrid \
  --profile <configured-profile> \
  --source-version 11111111-1111-4111-8111-111111111111 \
  --rerank
```

Reranking works with every search mode and runs only when requested. It needs
the profile's active `query_text_and_excerpt` grant, including in lexical and
auto modes. Review and grant the plan using the consent steps above. Search
does not grant consent itself.

Human output shows the reranking outcome and candidate count. `--json` includes
a `reranking` receipt whether or not you pass `--explain`. In the receipt,
`applied` means reranking succeeded, `skipped` means there were no candidates,
and `degraded` means the search kept its original ordering after a reranking
failure. The configured `degrade` failure policy returns that original
ordering. `fail_closed` returns `reranking_failed` instead. See the
[search response contract](../architecture/http-api.md#coverage-and-source-fenced-search)
for receipt fields and degradation causes.

## Modes and coverage

- **`lexical`** searches canonical segments from renditions.
- **`semantic`** searches the selected embedding binding.
- **`hybrid`** combines lexical and semantic candidates.
- **Explicit `auto`** uses lexical retrieval and reports `lexical` as the actual mode.

HTTP and embedded callers can [prefer content matches over filenames](../architecture/http-api.md#coverage-and-source-fenced-search)
with `content_first: true` or `ContentFirst: true`.

Each search response reports the actual mode, degradation, and the number of
complete documents in the supplied source set. The coverage API separately
reports rendition and embedding state, including complete, unavailable, stale,
ineligible, and rebuilding counts. `previous_generation_serving` counts rebuilding
documents whose previous complete result is still available. A rebuild alone
does not establish that a previous result exists. Missing required coverage keeps
the aggregate state `partial`, even while another result is rebuilding. See the
[coverage contract](../architecture/http-api.md#coverage-and-source-fenced-search)
for the fields and snapshot boundary. A direct-file-only embedding can produce
a match without an excerpt, because Docbank kept no readable text to quote.

## Source-fenced consumer contract

The source fence is the vault identity and content-version set the caller
supplies. Docbank enforces it before retrieval, not as a filter on results. It
validates the vault identity and applies the content-version set before lexical
retrieval, vector scoring, candidate expansion, and reranking. Only vectors
attached to current versions of live documents inside that fence are eligible.
Missing vector-row authority stops the search. A foreign vault or an empty,
duplicate, malformed, or oversized fence is rejected.

Consumers store a source's identity as `(vault_uid, node_id,
content_version_id)`. A hit's rendition attachment, rendition build, lexical
segment, embedding set, and vector space identify the evidence used for that
query. They are not a second identity for the document. Fetch readable
evidence from Docbank's authenticated rendition endpoint instead of keeping a
parallel chunk or vector corpus.

The consumer must still re-check whether each candidate is visible immediately
before presentation. That final check closes the interval between retrieval and
display when a source version is revoked or otherwise becomes unauthorized.

Processing search does not require a user-facing evidence sidecar. Results
carry evidence references, and an authenticated rendition carries its own
identity and navigation metadata. Consumers may request those when they need to
explain a result.

For ordinary name and verified plain-text search, see [Searching](searching.md).
