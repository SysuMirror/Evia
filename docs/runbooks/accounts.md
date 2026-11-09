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
same transaction as the account. `actor_kind='operator'` identifies the database
operator execution path; it does not assert an in-app human identity. `target_id`
identifies the new account, and `occurred_at` records database time. Passwords,
hashes and private environment values are absent from these records. Retain
database/operator access records separately when individual operator attribution
is required.

The command refuses an empty installation and an installation with no enabled
accounts. It is not an account recovery path. Password reset, disabling accounts
and recovery remain separate work.
