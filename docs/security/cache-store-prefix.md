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
is already applied during driver construction. Memory, file, and Redis backends
all pass that complete logical prefix to the namespace wrapper. Application Redis
stores use an empty low-level Redis prefix, so an unframed outer prefix cannot
defeat the wrapper's encoding. SCAN/Clear receives the complete nonempty framed
namespace. The existing 256-byte limit on an application Redis prefix is retained.
Application Redis prefixes must not contain `__thinkgo_lock__:` or
`__thinkgo_cache_fence__:`. These protected markers would make Redis's conservative
clear-scope check ambiguous; configuration now rejects them at startup with an
actionable error. Choose a business namespace without those markers. Prefixes
containing `__thinkgo_namespace__:` remain supported and isolated.
Direct `redis.NewRedis` callers retain its literal low-level prefix behavior; for
shared Redis storage, keep that prefix empty and provide the logical namespace to
`NewNamespaceDriver` instead. The injective mapping below applies to wrappers over
the same backend scope, not arbitrary concatenated low-level Redis prefixes.
The namespace constructor shares pure option validation with `ConfigureStore`,
not its rejection of a runtime prefix change; valid explicit wrappers still work.

The `Prefix` field remains in `StoreOptions` so old source can compile and receive
an actionable error instead of falsely claiming isolation. This is an intentional
fail-closed change for direct callers that supplied a previously ignored prefix.
It does not automatically wrap active drivers. Namespaces are not authentication
or tenant-authorization boundaries. Callers must handle errors and protect backend
access independently.

## Namespace encoding and metadata isolation (#126, #128)

`NewNamespaceDriver` treats the complete configured prefix as an identifier,
including an empty prefix. Its physical business namespace is
`__thinkgo_namespace__:v2:<UTF-8 byte length>:<prefix>:`. Length framing means
`tenant:` and `tenant:vip:` cannot address or clear each other's entries even
when a business key contains `vip:`. Two wrappers with the same logical prefix
over the same backend still share the same business namespace.

Tag membership and reverse-tag metadata keep separate reserved key prefixes
inside that namespace. The custom `tag_prefix` is also length-framed after its
metadata type; it is never substituted for the reserved prefix. A normal key
such as `tag:default:billing` is therefore an ordinary business value and cannot
read, overwrite, or delete the internal billing membership list. Existing public
API rejection of `__thinkgo_tag__:`, `__thinkgo_tag_reverse__:`,
`__thinkgo_cache_fence__:`, and `__thinkgo_lock__:` keys remains in force.

Get/Set/Delete, batch operations, counters, atomic updates, and lock operations
all use the same mapping. Clear operations use the complete encoded namespace;
an empty configured prefix no longer invokes backend-wide Clear. A backend must
support the relevant prefix-clearing capability, otherwise the wrapper fails
closed. Fencing metadata remains outside business Clear, in a namespace tied to
the new encoding, so clearing data does not reset monotonic generations.

### Upgrade and existing cache data

This is a physical key-format change. Upgrade all instances sharing a cache
namespace together, or drain the old instances before sending traffic to the new
ones. Old and new instances do not share values, tag relationships, locks, or
fencing state. Plan for a cold cache and the resulting origin load; prewarm
important entries from their authoritative data after upgrading.

Application Redis stores also move their configured prefix inside the versioned
framing; it is no longer prepended as literal bytes outside that frame. This
includes prefixes containing the namespace marker itself. Previously accepted
application Redis prefixes containing a lock or fencing marker must be renamed
as part of the coordinated rollout. Low-level Redis lock/fencing protection is
unchanged. Resource identities remain logical isolation identifiers, not
filesystem paths; file stores expose
their canonical physical directory separately through `CacheDirectory`.

The wrapper deliberately does not fall back to legacy keys or delete them during
Clear: a legacy concatenated key cannot reliably identify its owning namespace.
Let TTL-bound old entries expire. For persistent old entries, remove only a
verified application-owned scope after all old processes have stopped; do not
use a broad prefix deletion on a shared backend. The framework does not perform
automatic migration of old tag or lock metadata.

Regressions cover rejection without partial option publication, shared-backend
read/write/flush isolation, nested and empty prefixes, ordinary-key/tag metadata
separation with custom tag prefixes, legacy-key noninterference, valid
constructor/default-tag behavior, malformed prefix/tag values, application Redis
prefixes containing the namespace marker, and file-cache startup on Windows. Full
current-dependency CI and post-merge main verification
are required before closing the issue. Local archived-dependency results are
recorded separately and do not replace those checks.
