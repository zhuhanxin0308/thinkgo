# Debug-page key-material redaction

Issue #41 concerns secret fields in exception debug pages, not authorization to
view the page. The existing direct-loopback and forwarded-client access checks
are unchanged.

The sensitive-key classifier now normalizes case and `-`, `_`, `.` separators
before matching. It also covers private keys, signing keys, encryption keys,
credentials and client secrets. JSON bodies (including nested arrays), form
bodies, URL parameters and headers all use this classifier. Sanitization does
not mutate the original request or remove ordinary diagnostic fields.

This is a denylist-based display defense, not a guarantee that arbitrary text or
unrecognized field names cannot contain secrets. Do not enable debug in a
publicly exposed deployment. Custom error renderers remain responsible for their
own output.

Regression commands:

```sh
go test -race -mod=readonly -shuffle=on -count=20 -run '^TestIssue41' ./exception
go test -race -mod=readonly -shuffle=on -count=3 ./exception ./middleware .
```

The issue is eligible for closure only after this change is merged into `main`
and the CI run for that exact main-branch commit succeeds. PR CI or local test
results alone do not satisfy this condition.
