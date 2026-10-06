# Trace visibility and response boundaries

Issue #18 is addressed by applying the exception debug-page source policy to
Trace before asset handling or collector allocation. Both HTML and console
output modes require a loopback peer and reject forwarded-client headers that
identify a remote or unknown client. A forged loopback header cannot override
the actual remote peer. Remote requests continue to the application unchanged,
without a Trace collector or Trace assets.

The exported exception.CanExposeDebugPage helper delegates to the existing
exception policy; it is not a new authorization system. A local reverse proxy
must preserve accurate client information. A proxy that strips all such headers
can make a remote client indistinguishable from a local caller, so debug must
remain disabled on publicly exposed deployments. Production startup enforcement
is tracked independently in issue #19 / PR #63.

Only a valid text/html Content-Type receives injected output. Empty, plain-text,
JSON, misleading text/html prefixes and malformed media types are not modified.
Injected responses are marked Cache-Control: no-store. Returning HTML using the
framework's ordinary HTML default remains supported; explicit non-HTML responses
are no longer treated as pages. Existing escaping and request collector cleanup
remain in place. This does not sanitize arbitrary SQL/log content for publication.

Regressions cover both Trace modes, direct and forwarded peer combinations,
collector absence on unauthorized requests, the actual response header, remote
asset fallthrough and ordinary response identity. Existing Trace tests are
updated to declare local HTML intent; no race detection or CI gates are disabled.
Close the issue only after merge to main and complete main CI/regressions.
