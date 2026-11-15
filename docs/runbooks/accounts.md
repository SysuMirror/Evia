# Operator account provisioning

`evia bootstrap` creates the first account once. After an installation has at
least one enabled account, a database operator can run `evia account-create` to
create an additional enabled account. This command does not create a session or
project membership. A project owner adds the returned existing account through
the member management API.

Apply the checked-in migrations explicitly with `evia migrate` before running
the command. `account-create` requires only `EVIA_DATABASE_URL` and these private
environment values:

| Variable | Policy |
| --- | --- |
| `EVIA_ACCOUNT_USERNAME` | Trimmed and lowercased; 1–100 ASCII letters, digits, `.`, `_`, `@`, or `-`. |
| `EVIA_ACCOUNT_DISPLAY_NAME` | Nonblank, no NUL, at most 200 bytes. |
| `EVIA_ACCOUNT_PASSWORD` | 12–72 bytes; hashed with the same bcrypt policy as bootstrap. |

Supply these variables through the operator's secret manager or an environment
file outside the repository with mode `0600`. Do not put passwords in command
arguments, shell history, logs, or a checked-in file. The command accepts no
positional credentials. For a private file containing `EVIA_NAME=value` entries,
the existing helper injects values without executing shell code or printing them:

```sh
python3 tooling/verification/browser_runtime.py run /private/account-create.env -- /path/to/evia account-create
```

Successful output includes only the new `account_id` plus the usual process log
metadata. Existing usernames fail without changing an account or its password;
this includes retries after lost output. Resolve an uncertain result by checking
the account and the operator audit record through authorized database access,
rather than expecting a retry to replace its credentials.

Each successful creation inserts an `account_provision_events` record in the
same transaction as the account with `event_type='account_created'`.
`actor_kind='operator'` identifies the database operator execution path; it does
not assert an in-app human identity. `target_id` identifies the new account, and
`occurred_at` records database time. The `event_type` column (migration 028) is a
closed set of `account_created`, `account_disabled`, `account_enabled` and
`password_reset`, enforced by a CHECK constraint; the column has no default, so
an insert that omits the type fails instead of being mislabelled as a creation.
Passwords, hashes and private environment values are absent from these records.
Retain database/operator access records separately when individual operator
attribution is required.

The command refuses an empty installation and an installation with no enabled
accounts. It is not an account recovery path. Password reset remains separate work.


## Disabling and re-enabling accounts

`evia account-disable` and `evia account-enable` toggle the existing
`accounts.disabled` flag for one username. Like `account-create`, they need only
`EVIA_DATABASE_URL` and `EVIA_ACCOUNT_USERNAME` (same normalization and length
policy), perform no session or membership writes, and never accept positional
credentials. The change runs under the same installation advisory lock as
provisioning.

```sh
python3 tooling/verification/browser_runtime.py run /private/account-disable.env -- /path/to/evia account-disable
```

Disabling is safety-guarded: the last enabled account cannot be disabled
(`LAST_ENABLED_ACCOUNT`), because doing so would lock every operator out and
leave no account able to provision or bootstrap. An unknown username fails with
`ACCOUNT_NOT_FOUND`. Re-disabling an already disabled account and re-enabling an
enabled account are idempotent. Once disabled, the account is immediately rejected
on every authenticated path because all session and membership reads filter
`NOT disabled`; no separate session deletion is required for correctness. The real-PostgreSQL
integration test `TestSetAccountDisabledImmediatelyRevokesAndRestoresSession` proves an existing
session is rejected immediately after `account-disable` and resolves again after `account-enable`.
`account-enable` restores access the same way.

Each real state transition writes one typed `account_provision_events` row in
the same transaction as the flag update: `account_disabled` when an enabled
account becomes disabled, `account_enabled` for the reverse. An idempotent
re-disable or re-enable that changes no state writes nothing, so every row
corresponds to an actual account-state change rather than a repeated command
invocation. Operator attribution still relies on the operator's own access
records and process logs, because these rows deliberately carry no in-app actor.
Invitation/registration self-service remains separate work.


## Resetting an account password

`evia account-reset-password` is the operator account-recovery path. It needs
`EVIA_DATABASE_URL`, `EVIA_ACCOUNT_USERNAME` and a new `EVIA_ACCOUNT_PASSWORD`
that satisfies the same 12–72 byte policy as creation, supplied through a `0600`
environment file rather than positional arguments. The new password is hashed
with bcrypt cost 12 and the old hash is replaced; the command never echoes the
password or resulting hash and only logs the `account_id`.

The hash replacement and session revocation run in one transaction under the
installation advisory lock: every existing session for the account is deleted,
so a reset used to recover a compromised account cannot leave an old session
valid. The account's `disabled` flag is not changed; a disabled account must be
re-enabled separately with `account-enable`. An unknown username fails with
`ACCOUNT_NOT_FOUND`. The real-PostgreSQL test
`TestResetAccountPasswordReplacesHashAndRevokesSessions` verifies the old
credential stops verifying, the new one verifies at cost 12, and sessions are
revoked. Every reset also inserts an `account_provision_events` row with
`event_type='password_reset'` in that same transaction, so a failed audit write
rolls the hash replacement and session revocation back together; resets always
replace the credential and therefore always leave a row.

## Reading account audit history

`evia account-audit` is the read-only path over the typed
`account_provision_events` trail. Like the mutating commands it needs only
`EVIA_DATABASE_URL` and `EVIA_ACCOUNT_USERNAME` (same normalization and length
policy), performs no writes, and never accepts positional credentials.

```sh
python3 tooling/verification/browser_runtime.py run /private/account-audit.env -- /path/to/evia account-audit
```

It resolves the username to its account first, so an unknown account fails with
`ACCOUNT_NOT_FOUND` instead of returning an empty trail. On success it emits one
structured summary log plus one log line per event, oldest first, each carrying a
1-based `sequence`, the closed-set `event_type` and a UTC `occurred_at`. The
output contains no username echo, password, hash, credential identifier or
in-app actor; the fixed `actor_kind='operator'` row semantics still mean operator
attribution lives in the operator's own database access records. An idempotent
re-disable or re-enable that changes no state produces no event, so every line
corresponds to an actual account-state or credential change. The real-PostgreSQL
test `TestAccountAuditEventsReadsTypedHistory` pins the full ordered trail
(`account_created`, `account_disabled`, `account_enabled`, `password_reset`),
UTC normalization and the not-found failure.
