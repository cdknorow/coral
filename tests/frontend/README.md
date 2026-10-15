# Frontend CDP suites

`node run_cdp_suites.mjs [--only name] [--shards N]` builds a dev Coral, starts N isolated
server and Chrome pairs (temp data directories, `--cloud-url off`) and runs every suite listed in
`suites.mjs`. See the comment at the top of `run_cdp_suites.mjs` for all its options, and
`coral-go/agent_docs/troubleshooting.md` for the test-server isolation rules and the disk space
a run needs.

## "Failed to fetch dynamically imported module" on a first attempt

A suite that fails on its first attempt with "Failed to fetch dynamically imported module"
(state.js, modals.js, ...) and passes on its retry was a request Chrome cancelled, not a server
error: Chrome aborts every request in flight, even to 127.0.0.1, when the host's network addresses
change (`net::ERR_NETWORK_CHANGED`; for example IPv6 temporary-address renewals). On Linux the
runner runs itself in a private network namespace with only loopback when the user may create one
(`unshare --net`), so those changes can't reach Chrome; it fetches the Go modules first, since there
is no network inside. Where namespaces aren't allowed it prints a note and runs as before.

- `CDP_NETNS=0` turns the namespace off.
- `CDP_NETLOG=1` (with `--keep`) has each shard's Chrome write its network log to
  `<shard>/netlog.json`.
