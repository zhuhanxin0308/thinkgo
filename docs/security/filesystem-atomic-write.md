# Local atomic write directory trust

Issue #135 concerned a temporary file whose opened handle was used for copying,
syncing, and closing, while chmod and rename selected its pathname again. On a
Unix shared writable directory, another UID could replace that directory entry
with a root-contained symlink. A pathname chmod could then widen an unrelated
private file's permissions, and rename could publish the substituted entry.

## Write, Copy, and upload behavior

Atomic temporary files now receive their final permissions through the opened
file descriptor. Changing the temporary pathname cannot redirect this chmod to
another inode. Write, WriteBytes, WriteStream, Copy, PutFile, and PutFileAs also
check the entire destination-parent chain from the already-open `os.Root`:

- Every component must be a real directory; a symlink parent is rejected even
  when its target stays within the disk root or listing is configured to skip links.
- On Unix, each directory must be owned by the process's effective UID or root,
  and group/other write bits must be clear. This rejects 0777, 0775, and sticky
  shared directories, including an unsafe ancestor above a seemingly private
  final parent. A foreign owner is rejected even if the current mode is 0700 or
  0755, because that owner could grant directory write permission later.
- Missing parents are created one component at a time, only beneath a parent
  that has already passed the checks. A configured shared-writable directory
  creation mode is rejected before creating that component.

An existing unsafe parent is rejected before creating a temporary or consuming
the destination writer's content. The error is identifiable with
`errors.Is(err, filesystem.ErrUnsafeLocalWriteDirectory)`. Upload validation may
already have inspected its private input snapshot before reaching this boundary.

The temporary file's original identity is retained across preparation and close.
A detected missing, symlinked, or different temporary inode is not renamed into
the destination, and cleanup does not unlink a replacement entry. The destination
keeps its old version on these pre-publication failures. If file identity
cannot be obtained for a newly opened temporary, the handle is closed and the
uncertain path is left untouched for operator inspection.

These identity checks are additional corruption detection. A last-moment Lstat
does not eliminate a race in a directory writable by an attacker. Protection
against the reported different-UID POSIX attack comes from the trusted parent
chain, which prevents that attacker from replacing the directory entries used
for chmod preparation, publication, or cleanup.

## Platform and deployment boundary

Unix owner/mode checks enforce the ordinary POSIX discretionary access model.
Linux POSIX ACL effective write permissions are limited by the corresponding
mode mask. The implementation does **not** inspect macOS extended ACLs, Windows
DACLs, ownership-disabled volumes, or other filesystem-specific authorization
that can grant modification rights beyond these mode bits. Those mechanisms
must also restrict directory creation, deletion, rename, and permission changes
to trusted actors. Windows retains its existing deployment ACL requirement;
descriptor chmod and temporary identity checks still apply.

Processes running as the application UID and administrators are trusted. They
must not weaken directory permissions or replace entries during a write. This
change does not claim to isolate mutually hostile processes sharing a UID, to
validate arbitrary filesystem ACLs, or to change the separate Move,
SetVisibility, and directory-management API contracts.

## Upgrade

Existing Unix configurations that write through shared writable parents,
foreign-owned parents, or symlink parents will now fail instead of publishing.
Use a dedicated application-owned directory, typically 0700 for private storage
or 0755 for public storage, and real child directories. Place any shared ingress
outside this publication tree and copy approved content into the dedicated
destination. Do not automatically chmod or chown shared directories during
upgrade: doing so could change another application's access.

Regressions cover unsafe roots and ancestors, group/other and sticky modes,
symlink parents, unsafe configured creation modes, foreign-owner validation,
Copy/upload reuse of the gate, and injected temporary replacement at copy,
descriptor-chmod, and close boundaries. The injection hooks run as the test
process and supplement the permission-policy checks; they are not represented
as a native two-UID attack reproduction. Cross-platform Go CI remains required.
