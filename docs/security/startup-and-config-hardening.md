# Startup and configuration hardening

This change addresses issues #19, #21, #31 and #47. It does not change routing,
CSRF defaults, authentication policy, dependencies or the release workflow.

## Production debug

In production/prod/release, either the runtime debug flag or app.app_debug in
configuration blocks startup through the existing startup-error mechanism.
This check precedes security-profile early returns. Development behavior is
unchanged. Runtime mutation after startup is not a supported configuration
reload mechanism; deployments must keep debug disabled.

## Explicit CORS origins

Setting cors.enable=true requires an explicit allow_origins array. Missing,
empty or invalid policies are rejected. Public APIs may explicitly select ["*"]
without credentials; wildcard plus credentials remains rejected. CORS remains
disabled by default and is not an authorization mechanism.

## Configuration-cache files

optimize:config atomically creates or replaces its reloadable configuration
snapshot with mode 0600, including replacement of older, broader-permission
files. Contents still include application secrets because this is a runtime
cache, not a sanitized diagnostic report. Protect the directory, backups and
artifact distribution. Windows requires a suitable filesystem ACL; POSIX mode
bits alone do not establish owner-only access there. Ordinary route/cache and
vendor publication writes retain their existing permission behavior.

## Native MySQL DSN text

Connection-error display redaction also recognizes user:password@tcp(...),
tcp4/tcp6 and unix forms, including ambiguous separators and multiline password
text. Ambiguous messages may lose more diagnostic text rather than disclose a
credential. Existing URI/key-value redaction and newline escaping remain. This
is display sanitization, not a change to driver error identity or arbitrary SQL
error handling. Never intentionally include credentials in logs.

## Verification and issue closure

Regressions cover the unchanged baseline failing and the candidate passing,
production aliases and independent debug flags, explicit CORS policy boundaries,
new/existing configuration permissions, non-configuration permission preservation,
and native DSN forms. Close the associated issues only after merge to main and
successful full CI and regressions on the resulting main-branch code.
