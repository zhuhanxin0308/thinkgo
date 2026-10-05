# Static file visibility

Issue #53 is addressed at the static file boundary. URL path segments beginning
with a dot are rejected, except the root /.well-known/ namespace defined by
RFC 8615. Hidden children of that namespace and nested .well-known directories
remain hidden. Ordinary public assets remain available.

The same visibility policy is applied to the resolved path relative to the real
public root, including the final identity check. A visible alias pointing at
.env or a hidden directory does not bypass the policy. Existing containment,
encoded-separator rejection, regular-file and file-identity checks remain.

This is not a file-content classifier. Never put secrets or executable uploads
in the public directory, and do not expose project internals through custom
handlers. Hard links or copied secrets under ordinary public filenames are not
detectable by a filename visibility policy. The public root itself remains an
operator-selected directory, even when its ancestors have dot-prefixed names.

Regressions exercise actual HTTP GET/HEAD, percent-encoded dot segments, regular
assets, well-known paths, hidden file/directory aliases and symlink escapes.
Symlink subtests report a skip only on hosts that cannot create a symlink; the
normal file tests still run. CI and main-branch verification are required before
closing the issue.

Reference: https://www.rfc-editor.org/rfc/rfc8615.html
