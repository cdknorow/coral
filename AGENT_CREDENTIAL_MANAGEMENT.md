# Persistent, Scoped Credential Access (Proposal)

**Status: design only; not implemented.** This proposal addresses long-running agents losing a credential reference after compaction, restart, or resume. It does not make an agent trustworthy, repair missing authorization, or prevent provider expiration and rotation failures.

## Problem and boundaries

Coral currently persists session identity, resume lineage, agent capabilities, connected-app metadata, and (for OAuth connected apps) provider tokens in the local store. Launchers construct provider-specific settings and environment, while hooks and `coral-agent` identify a caller through Coral session variables and server state. Restart and resume create a new process and may reconstruct only the configured environment. Compaction can lose a textual reminder even when the process still has access. These are separate failure modes:

- **Compaction:** the agent forgets a reference or procedure; persistent grants can be rediscovered through a tool, but do not restore lost intent.
- **Restart/resume:** process environment and in-memory handles disappear; a broker can re-authorize a new process without copying the secret into prompts.
- **Authorization:** a valid reference must still be granted to this team/task/lineage and requested service. Storage does not grant access.
- **Expiry/rotation:** provider credentials can expire or be revoked. The broker must report `expired`, `revoked`, or `reauthorization_required`; it must not silently retry around policy denials.

Do not put credential values in prompts, chat, task bodies, board messages, transcripts, logs, artifacts, URLs, or normal API responses. Example references are identifiers only: `credref://github/build-bot`, `credref://aws/staging-deploy`.

## Threat model

The first version must explicitly retain Coral's current limits. Local process identity, caller IDs, role strings, session IDs, and board membership are useful routing signals but are not cryptographic agent isolation. A process with unrestricted access to the shared filesystem or database may be able to read or misuse material available to its OS user. Prompt injection can ask an agent to disclose a reference or use a granted service for an unintended purpose. The design must therefore:

1. require operator-managed grants and server-side checks for every access;
2. bind a grant to a team, task, agent lineage, and service, with least-privilege operations;
3. return redacted metadata only during discovery;
4. record audit events without values; and
5. preserve existing sandbox and tool approval denials. A denied tool call is a denial, never an invitation to retry through another route.

This is not a promise of isolation from a malicious local process. Strong isolation requires OS/container identity, separate users, or an external policy-enforcing broker.

## Smallest compatible first version

### Persistent records

Add a Coral database record containing only metadata and an opaque provider key/reference:

```text
credential_refs(
  id, name, provider, external_key,
  allowed_scopes, owner_team_id, owner_agent_name,
  created_at, updated_at, expires_at, rotated_at,
  status, version
)
credential_grants(
  id, credential_ref_id, team_id, task_id,
  agent_lineage_root, service, operations,
  not_before, expires_at, revoked_at, granted_by, reason
)
credential_audit(
  id, credential_ref_id, grant_id, session_id,
  operation, outcome, error_class, created_at
)
```

`external_key` is a key name in the selected provider, never a secret or encrypted value. `operations` should be narrow verbs such as `git.read`, `github.pull_request.read`, or `deploy.start`, not an unrestricted “token” capability. Store hashes or stable IDs for audit correlation, never request payloads or returned values.

### Broker API and tool

Expose a server-side broker used by an authorized Coral tool/helper, not a general “get secret” HTTP endpoint:

- `GET /api/agent/credentials` returns references visible to the caller: name, provider, scopes, status, expiry/rotation state, and last-use time; values are never returned.
- `POST /api/agent/credentials/{ref}/invoke` accepts a declared operation and provider-safe arguments. The broker checks session identity, proven resume lineage, team/task grant, expiry, revocation, capability policy, and existing approval/sandbox policy, then performs or delegates the operation and returns a redacted result.
- `POST /api/credentials/grants` and revoke/rotate actions are operator-only UI/API operations. Grant creation requires explicit confirmation and records who, why, scope, and expiry.

Prefer operation-bound adapters (for example, “create GitHub pull request”) or a short-lived process-side handle over returning raw bytes. If a provider requires a token-bearing subprocess, inject it only for that child process, scrub it from diagnostic output, avoid shell command interpolation, and document that child process memory, `/proc`, crash dumps, and provider tooling may expose it. Environment injection is a compatibility fallback, not the security boundary.

The existing connected-app store is a useful provider/OAuth integration point, but its token columns and refresh flow must not be exposed directly to agents. A credential reference may point to an existing connected-app record through an internal ID; the broker remains responsible for grant checks and redaction.

### CLI and UI

Add discoverability without values:

```text
coral-agent credential list
coral-agent credential status <reference>
coral-agent credential use <reference> --operation github.pull_request.read
```

`list` and `status` show provider, scope summary, expiry state, and authorization errors. `use` invokes the broker and prints only the operation result after redaction. No command prints a token, client secret, refresh token, or provider file. The Team Settings UI should manage references, grants, expiry, revocation, and rotation status; task/chat views should show only a reference name and redacted audit state.

## Platform and storage choices

- **macOS first:** use Keychain Services through a small broker adapter. The Keychain item key is derived from Coral installation identity plus `external_key`; ACL/access prompts remain OS-controlled. Do not place a plaintext fallback beside the database.
- **External manager adapter:** support 1Password Connect, macOS-compatible enterprise vaults, or another configured provider through a narrow interface (`Resolve`, `Invoke`, `Rotate`, `Revoke`). The external manager owns encryption, rotation, and access policy.
- **Other platforms:** Windows Credential Manager and Linux Secret Service can be later adapters with the same interface. If no secure provider is configured, references may be listed as `unavailable`, but Coral must not silently fall back to plaintext SQLite, dotfiles, process-wide environment, or custom cryptography.

Do not implement encryption in Coral or invent a key-storage scheme. A local encrypted store is acceptable only if a platform/external key provider protects the encryption key and the design has undergone security review.

## Lifecycle and error behavior

On launch, restart, or resume, the process receives no secret value automatically. It receives its session/lineage identity and can rediscover authorized references. A broker call creates a short-lived operation context. Revocation invalidates active contexts where the provider supports it and prevents new calls. Rotation updates the provider key/version; old versions become `rotated` and fail closed after their grace period.

Use explicit errors: `not_found`, `not_granted`, `capability_denied`, `approval_required`, `expired`, `revoked`, `provider_unavailable`, `reauthorization_required`, and `redaction_failed`. Never retry a denied or approval-required request through another credential, session ID, board identity, or subprocess route. Audit both success and failure with timestamps and redacted identity metadata.

## Migration

No migration should scrape prompts, transcripts, board messages, shell history, or arbitrary environment values. Operators create references through the Settings UI/CLI and complete provider-specific authorization. Existing connected-app rows can be adopted by creating metadata-only references that point at their internal connection IDs; tokens remain in their existing protected integration until the broker adapter is ready. Existing agents continue to use current behavior until an explicit grant is made.

## Acceptance tests with dummy credentials

1. A dummy provider operation succeeds only for the matching team, task, service, and resume lineage; another agent with the same role string or workspace is denied.
2. Discovery returns reference metadata and never returns the dummy value in JSON, CLI output, logs, board messages, transcripts, artifacts, or audit rows.
3. Restart/resume can rediscover an unexpired grant without copying a value into the prompt or inherited environment; compaction does not change authorization.
4. Expired, revoked, rotated, missing, and unavailable-provider states fail with the documented error class and no fallback retry.
5. Existing sandbox/tool approval denials remain denials, including subprocess attempts to invoke a credential operation.
6. Concurrent calls are scoped and audited independently; cancellation prevents a late result from reaching a different session.
7. Provider adapter tests use deterministic dummy references and a fake vault; no real account or credential is required.

## Staged roadmap and open decisions

1. **Design/security review:** settle trust boundaries, grant UX, redaction rules, audit retention, and whether operation adapters are sufficient for the first provider.
2. **Metadata-only foundation:** references, grants, audit schema, operator UI/CLI, and broker authorization with a fake provider. No real secret handling.
3. **One secure adapter:** macOS Keychain or one approved external manager; add short-lived operation contexts and rotation/revocation tests.
4. **Provider operations:** add narrowly scoped GitHub/deploy examples and document subprocess limitations.
5. **Additional platforms/adapters:** Windows Credential Manager, Linux Secret Service, and enterprise vaults after security review.

Open decisions include whether grants attach to task IDs or workflow runs, how much lineage survives manual forks, whether operation adapters can cover all initial use cases, audit retention and export, enterprise policy hooks, and the minimum OS identity needed before claims of isolation are acceptable.
