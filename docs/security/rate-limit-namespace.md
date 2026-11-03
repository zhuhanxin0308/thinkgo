# Redis rate-limit namespace isolation

Issue #131 concerned ambiguous physical keys formed by concatenating the
configured Redis prefix and the logical client key. `tenant:` with `vip:client`
and `tenant:vip:` with `client` previously addressed the same quota. That allowed
one configured namespace to consume or reset another namespace's quota.

`RedisStoreOptions.Prefix` is now encoded as a complete namespace identifier:
`__thinkgo_ratelimit__:v2:<byte length>:<prefix>:`. Both `Take` and `Reset` append
the validated logical key to that encoded prefix. Nested prefixes are distinct;
multiple instances using the same prefix continue to share their atomic GCRA
quota. The Redis server clock, operation timeout, error handling, and policy-key
rules are unchanged.

## Coordinated upgrade

This changes the physical Redis keys. New instances start with fresh quota state;
the first request receives the configured initial burst. Old and new versions
do not share their counters. Upgrade or drain all instances sharing a limit
together and account for this one-time quota reset when scheduling the rollout.
If a gateway enforces an independent limit, keep it active during the transition.

There is no fallback to legacy keys: their ownership is ambiguous. `Reset` only
removes the new namespace's key. Existing legacy rate-limit keys retain their
original TTL and expire naturally; no broad Redis cleanup is required.

The regression uses an isolated miniredis server with fixed time to verify
nested-prefix quota isolation, Reset isolation, same-prefix sharing, and
noninterference with legacy keys. It does not substitute for a real Redis
deployment test.
