# Explicit routing by default

Issue #17 changes ThinkGo defaults with the repository owner's approval:
`route.NewRouter()` does not enable automatic controller dispatch, and the
application's missing `route.url_route_must` value is `true`. A missing route
configuration, an empty object, and a partial object with unrelated settings
all keep this policy. Generated and reference project configurations explicitly
set `url_route_must` to `true`.

An existing exported controller method is not itself a route. Without a matching
explicit rule, the router returns no match and the HTTP layer retains its 404
behavior; a POST-only rule cannot fall back to invoking the same action for GET
or HEAD. There is no automatic OPTIONS fallback in the default mode.

Explicit Any, MISS, HEAD and OPTIONS registrations retain their behavior.
Global CORS preflight and public static-file handling have separate entry points
and are not disabled by this setting. `route_complete_match` still controls
prefix versus complete matching of explicit rules and is unchanged.

Applications may explicitly set `url_route_must=false`, or call and check
`EnableAutoRoute(true)` during registration. Existing explicit false values are
not rewritten. Automatic dispatch then keeps its existing method-independent
semantics; authentication, authorization and the rule that safe methods must
not mutate state remain the application's responsibility. Router configuration
cannot be changed after Freeze.

The generated application still has explicit welcome and hello routes, and its
executable HTTP tests verify both allowed routes and rejected implicit actions.
Existing automatic-routing regression cases now opt in explicitly instead of
relying on the old default; they are not removed or skipped.

Issue #17 is eligible for closure only after the change is merged into main
and the exact resulting main commit passes full CI and the issue regressions.
