# Cache store prefix ownership

Issue #39 concerned a low-level option that was accepted without changing any
backend key. `Cache.ConfigureStore` configures expiration and tag-prefix options;
it cannot rename or isolate an already registered driver. A nonempty
`StoreOptions.Prefix` now returns an error wrapping `ErrInvalidCacheStore` before
publishing any options. The error directs callers to `NewNamespaceDriver`.
Existing key/TTL/tag validation and error identities remain in effect.

For direct Go construction, wrap the backend with `NewNamespaceDriver` before
registering it as a store. Use distinct, application-controlled prefixes for
independent stores. The executable `ExampleNewNamespaceDriver_isolation` shows
two stores on one memory backend, verifies that flushing one preserves the other,
and checks that a silently ignored reconfiguration is rejected.

The application configuration path is different: `cache.stores.<name>.prefix`
is already applied during driver construction. Memory/file backends use the
namespace wrapper; Redis applies its prefix inside the Redis driver to constrain
SCAN/Clear and must not receive the same prefix twice. That path is unchanged.
The namespace constructor shares pure option validation with `ConfigureStore`,
not its rejection of a runtime prefix change; valid explicit wrappers still work.

The `Prefix` field remains in `StoreOptions` so old source can compile and receive
an actionable error instead of falsely claiming isolation. This is an intentional
fail-closed change for direct callers that supplied a previously ignored prefix.
It does not migrate old data, change the meaning of existing keys, or automatically
wrap active drivers. Empty prefixes do not provide isolation, and namespaces are
not authentication or tenant-authorization boundaries. Callers must handle errors
and protect backend access independently.

Regressions cover rejection without partial option publication, shared-backend
read/write/flush isolation, valid constructor/default-tag behavior, and malformed
prefix/tag values. Full current-dependency CI and post-merge main verification
are required before closing the issue. Local archived-dependency results are
recorded separately and do not replace those checks.
