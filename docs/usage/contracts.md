---
title: Contracts
description: Register agreements and parties, send DocuSeal templates, and inspect local signing history.
---

# Contracts

## Parties

A party is a person or organization you refer to in your records. Create one
entry for each entity or counterparty, then reuse its stable ID. Mark your own
entities with `--own`. You can optionally connect a party to a person already
in Docbank; an organization does not need a person record.

```console
docbank party create "Northwind Storage Ltd" --kind organization --country CA
docbank party create "Example Holdings Inc" --kind organization --own
docbank party list --kind organization --own
docbank party show <party-id>
docbank party edit <party-id> --legal-name "Northwind Storage Limited"
docbank party retire <party-id>
```

`party list` shows up to 100 parties by default. When a page is full, it prints
the `--offset` to use for the next page; that page may turn out to be empty.
`--json` output stays plain JSON.

Kinds are `person` and `organization`. Country is an uppercase ISO 3166-1
alpha-2 code. Display names contain 1–200 characters and legal names at most
300. Both reject control characters. Docbank rejects a new or renamed active
party when its folded display name matches another active party and returns the
existing ID. Retired entries remain available through `show` and
`list --include-retired`, but are hidden from the default list. Retired parties
cannot be edited or un-retired. Use `--clear-country` or `--clear-person` to
remove an optional field. Every command supports `--json`.

The HTTP API provides `GET` and `POST /api/v1/parties`, `GET` and `PATCH
/api/v1/parties/{party_id}`, and `POST /api/v1/parties/{party_id}/retire`.
An edit or retirement requires an `If-Match` revision obtained from the
resource's `ETag`. The CLI reads the current revision unless `--revision` is
provided. A stale revision fails without changing the party. Creation needs no
revision. Party changes are refused in vaults with active audited history.
Parties are included in metadata backup and restore. The API also rejects names made only of whitespace or control characters, and country codes must be assigned ISO 3166-1 alpha-2 codes. New person links must refer to an active person.

Duplicate detection compares display names exactly after Unicode normalization,
case folding, and trimming and collapsing whitespace. For example,
`Northwind  Ltd` matches `Northwind Ltd`. Punctuation and legal suffix variants
are not treated as duplicates. Linking a person requires an active person; an
existing link remains readable and editable after that person retires. Merging
people moves their linked parties to the survivor and advances each party
revision.

A person split does not move party links; parties moved by a merge remain linked
to the survivor.

## Contract records

Register an agreement with its dates, terms, and parties. Create the parties
first, then refer to them by ID or active display name.

```console
docbank contract create "Storage agreement" --status active --own "Example Holdings Inc" --counterparty "Northwind Storage Ltd" --expires 2027-12-31
docbank contract list --status active --expiring-before 2028-01-01
docbank contract show <contract-id>
docbank contract edit <contract-id> --summary "Annual storage" --clear-expires
docbank contract delete <contract-id>
```

Stored statuses are `draft`, `pending_signature`, `active`, `terminated`, and
`cancelled`. An active agreement reads as `expired` after its expiry date; on
the expiry date it remains active. `--as-of` chooses the date used for that
calculation, and `--expiring-before` excludes the named date for every status. Combine it with `--status active` for active renewals. Title search
with `--q` is a literal, ASCII case-insensitive substring. A contract type is
an optional lowercase label containing letters, digits, and underscores.
Each short term is limited to 8,192 bytes.

An own-party link requires an own entity. Retired parties remain on existing
contracts but cannot be newly attached. Edit party links with `--own`,
`--counterparty`, `--remove-own`, and `--remove-counterparty`. Names for
removals match linked parties in the requested role, including retired parties;
use an ID when a name is ambiguous. Each change uses the contract revision.

The CLI checks requested party links before changing fields. An existing link
can be attached again even after the party is retired. A name that matches a
retained link while another active party uses the same name is refused; use an
ID. If a later change fails, the command reports confirmed steps. If a mutation
outcome is unknown, inspect the contract before retrying.

Deleting a contract removes its party links, not stored documents. Contract
mutations are refused in audited vaults. Metadata backups include contracts and
their links.

The HTTP API exposes `/api/v1/contracts` and individual contracts at
`/api/v1/contracts/{contract_id}`. Mutations of existing records use the
revision in `If-Match`; a stale revision fails without changing that operation.
The contract ETag carries the aggregate revision for `If-Match` and does not
change when a linked party is renamed or the date-derived status changes.

Contract party links use `PUT` and `DELETE` on
`/api/v1/contracts/{contract_id}/parties/{party_id}?role=own|counterparty`.
A repeated PUT is a revision-fenced no-op; deleting a missing link returns
404. An unknown party returns `invalid_contract`; an invalid role is rejected by request validation. A non-own
party in the own role returns `party_not_own`, and a retired party returns
`party_retired`. List filters include type, party ID and exclusive expiry;
`limit` and `offset` select pages, with a short page indicating the end.

## DocuSeal connection

Docbank can check an optional DocuSeal connection, list templates, and send a template for signature. Configure
`[integrations.docuseal]` as described in [Configuration](../configuration.md#docuseal-connection),
then restart the daemon.

```console
docbank contract sign check
docbank contract sign templates
```

Both commands accept `--json`. A full page returns a continuation ID; pass it with `--after` to check for
another page. A full final page can be followed by an empty page. Template names
are display text, capped at 512 bytes with control characters replaced and quoted in human output. A name containing the API key, before or after display conversion, causes the page to fail. Roles
are exact identities for later signing: invalid or oversized roles cause the
page to fail rather than being rewritten. Each provider response is limited to 2 MiB. If a template page exceeds that limit, Docbank retries the GET with a smaller page, down to one template; a single oversized template returns an error. Pages contain at most 100 templates, 1,024 roles per template and 4,096 bytes per role.


## Send a template for signature

Choose a DocuSeal template with one exact role per signer, up to 20 signers.
Docbank rejects template roles that differ only in ASCII letter case, because
DocuSeal resolves those roles ambiguously. Docbank sends the
provider the template ID and each signer's role, name, email and an opaque
external ID. It does not send contract terms, parties or document bytes. The
provider emails the signers using the document already in its template. By
default DocuSeal invites the first signer initially, then invites the next signer
after the preceding signer completes. A confirmed creation does not prove an
invitation was delivered. Local signer states stay `awaiting` until progress
tracking is available.

```console
docbank contract sign request <contract-id> --template 7 --signer 'Tenant=tenant@example.com:Example Tenant'
docbank contract sign status <contract-id>
docbank contract sign status <request-id> --request
```

Repeat `--signer` for each role. For a role or email containing a delimiter,
use a JSON object such as `--signer '{"role":"Buyer=Tenant","email":"tenant@example.com","name":"Example Tenant"}'`.
Names may be empty and are limited to 512 UTF-8 bytes; emails are bare addresses
of at most 254 characters. History is stored locally and remains readable when
the integration is disabled. Backups include signer names and email addresses.

Only one request may be open for a contract. A confirmed send moves a draft to
`pending_signature`, preserving concurrent edits. Other contract statuses stay
unchanged. If the provider outcome is unknown, the request stays `creating`;
run `docbank contract sign status <contract-id>` before retrying and do not send
another invitation blindly.
Archive requests in DocuSeal manually when needed. Archiving there does not clear
the local open request. A successful or uncertain request blocks another request
and contract deletion until cancellation or reconciliation is available in a
later release. Terminal request history is removed with the contract, while
stored documents remain. Signature mutations are refused in audited vaults.
If audit is enabled between admission and settlement, even a confirmed provider
send can remain `creating` with an unknown outcome.

The API uses `POST` and `GET /api/v1/contracts/{contract_id}/signature-requests`
and `GET /api/v1/signature-requests/{request_id}`. The POST requires the
contract revision in `If-Match`; the individual request returns its own ETag.
Invalid signer or template-role input returns 422 `invalid_signature_request`;
an already open request returns 409 `signature_request_open`. After an intent
is recorded, a provider refusal returns 502 `docuseal_creation_failed` and an
uncertain outcome returns 502 `docuseal_creation_unknown`. When either problem
includes an opaque `request_id`, use it to inspect the local request. Other
provider preflight failures can also return 502 before an intent is recorded.
