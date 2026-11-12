---
last_edited: 2026-11-12
title: Records and contracts
description: Activate the contract type and manage typed records.
---

# Record types and contracts

Use a contract record to keep a title, type label, dates, and terms together in
one vault. Activate the built-in `contract` type before creating records:

```sh
docbank types activate contract
docbank record create contract "Example service agreement" \
  --set contract_type=service --set term.end=2027-12-31 --status active
```

`docbank types list` shows active types. Add `--include-inactive` to see the
built-in type before activation, and use `docbank types show contract` to
inspect its fields. Deactivating a type prevents record writes until it is
activated again. Existing records remain readable by ID; add `--include-inactive` to list them.

List and inspect records with `docbank record list --type contract` and
`docbank record show <id>`. Add `--json` to commands for machine-readable
output. A list supports `--status`, `--q` for a literal title substring,
`--field contract_type=service`, `--before term.end=2027-01-01`, and
`--after term.end=2026-01-01`. Date comparisons are exclusive. `--as-of`
chooses the date used for derived status; otherwise the daemon's local date
is used. Filtering happens before `--limit` and `--offset`.

A contract stores `draft`, `pending_signature`, `active`, `terminated`, or
`cancelled`. It displays `expired` only when its stored status is `active` and
its term end is before the selected date. `expired` is derived and cannot be
stored directly. The optional `contract_type` label uses lowercase ASCII
letters, digits, and underscores. Dates use `YYYY-MM-DD`. The term may have a
start, an end, or both; the start cannot follow the end. The optional `summary`,
`purpose`, `consideration`, `termination`, and note each have an 8,192-byte
limit.

Edit a record with `docbank record edit <id> --set summary='Revised terms'`.
Use `--clear term.end` to clear a date endpoint. `--title`, `--status`, and
`--note` change the corresponding top-level fields. `--expect-revision`
refuses a stale edit; without it the CLI reads the current revision first. An
unchanged edit keeps its revision. Retire a record with `docbank record retire
<id>`: it remains readable by ID but leaves ordinary lists and cannot be
edited. Types and records cannot be changed in an audited vault; reads remain
available. Record contents are not written to request logs.
