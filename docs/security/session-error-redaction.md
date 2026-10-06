# Session error logging boundary

Issue #42 concerns bearer session identifiers reaching log consumers. Session
now removes its current identifier and identifiers supplied in session metadata
from error text before calling either built-in or custom Logger implementations.
Session metadata fields are redacted in a new map, and the existing full error
text sanitizer runs before the callback. Ordinary diagnostic fields remain.

The original error is still returned, preserving errors.Is/errors.As behavior.
The caller's metadata is not mutated. Logger selection and the current ID are
captured under the Session read lock; no Session lock is held while invoking a
custom logger. Initialization errors are also covered, even before the incoming
ID has been adopted as the current request ID.

This protects known Session identifiers and recognized credential text. It is
not a guarantee that arbitrary free-form messages or unrelated custom metadata
can never contain secrets. Custom log storage still needs access controls.

Regressions cover metadata redaction, current-ID redaction without metadata,
request initialization with a failing driver, custom Logger output and original
error identity. Issue closure requires merge to main and successful full CI and
regressions on the resulting main-branch code.
