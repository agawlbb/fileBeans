# Standalone filesystem API trial

This feature replaces Quantum for native fileBeans and Apple Files. It remains
on `feature/filesystem-api` in the separate `.worktrees/filesystem-api` checkout
until the owner accepts it. Quantum source adapters, configuration examples and
release history are retained for rollback, but the new API and app do not call
Quantum. No browser file manager is included.

## Service and API

The existing Go companion hosts both `/v1/fs/*` and its existing job, Trash,
Drive and protection routes. There is no database schema change. The private
Bearer token authorizes all configured roots; this is a personal server, not a
multi-user permissions service. Unix permissions and verified mount availability
still apply. Match root names and visible root directories to the previous app
configuration before switching. Paths are relative to each configured root.

Enable only in the trial companion configuration:

```json
"filesystem": {
  "enabled": true,
  "ffmpeg": "/usr/bin/ffmpeg",
  "pdftoppm": "/usr/bin/pdftoppm"
}
```

The defaults for the executable paths are those shown above. Startup checks both
executables. Keep the current `protectedRuntime`, storage roots, service UID/GID,
state directory, SQL database, OAuth credentials and token. Disable the feature
or omit the block to retain companion-only behavior.

| Method and route | Behavior |
| --- | --- |
| GET `/v1/fs/sources` | Protocol version 1, sources and personal-server permissions |
| GET `/v1/fs/resources?source=…&path=…` | Metadata and direct children for folders |
| GET/HEAD `/v1/fs/content?source=…&path=…` | Original contents; HTTP ranges, ETag and conditional reads |
| POST `/v1/fs/content?source=…&path=…` | Create without replacement |
| PUT `/v1/fs/content?source=…&path=…` | Replace with a required quoted revision in `If-Match` |
| POST `/v1/fs/folders?source=…&path=…` | Create one directory without replacement |
| POST `/v1/fs/move` | JSON `source`, `from`, `to`; same-boundary no-overwrite rename |
| GET `/v1/fs/thumbnail?source=…&path=…` | Cached or generated JPEG preview |

Metadata includes `name`, full source-relative `path`, MIME `type`, `size`,
RFC3339 `modified`, opaque `revision`, `hasPreview`, and optional `children`.
Revisions derive from modification time and size, rather than hashing file
contents across Samba. PUT without a revision returns 428, stale replacement
returns 412, and name collisions return 409. API publication checks are serialized;
arbitrary external writers can still race a revision check. Copies and complex
moves use the existing durable jobs; deletions always use managed Trash.

Transfers stream through the service without full-file buffering. Read/write
inactivity deadlines allow large transfers to continue while making progress.
There is no transcoding. The existing iOS resource loader still requests bounded
ranges and handles seeking. All routes require authentication, including previews
and cached preview hits. Private service directories and symbolic links are not
exposed. There is no recursive search, continuous polling or persistent filename
index.

## Thumbnails and protection

Thumbnails are generated on request into `.filebeans-thumbnails` inside the
original file's containing folder. Cache keys cover its name, revision, fixed
320-pixel output size and renderer version. Originals are never modified.
JPEG/PNG/GIF and bounded text previews render in-process; video, PDF and other
supported image formats use FFmpeg/Poppler. Unsupported or read-only locations
retain file icons. Text previews show a bounded excerpt with ASCII font fallback.
An archive is a file: listing it does not unpack it or generate previews for its
contents. There is no archive-wide thumbnail scan.

Two renderers can run concurrently. Matching requests share a serialized cache
lookup. Files publish atomically. Preview misses opportunistically remove cached
JPEGs older than 30 days and renderer leftovers older than 48 hours in that
folder. Interrupted requests clean up their upload temporary file; spools left
by a process crash can be removed while the service is stopped. They are never
visible through the API.

Protected media, thumbnail caches and upload staging share the encrypted mount.
Renderer input uses pinned descriptors; subprocess working/scratch directories
stay inside that mount. There is no plaintext server thumbnail fallback. Broker
leases cover filesystem operations and complete response delivery, including
cache hits. Locking removes access; encrypted location hiding may return 404
rather than 409. Missing broker registry denies access, including cached reads.
Caches are excluded from browsing, copy jobs, move manifests and normalization.
Same-filesystem renames may naturally carry a folder's cache along with it.

The broker must explicitly select `"filesystemConsumer": true` during trial
cutover. Only its control UID (the API service) may then send readiness heartbeats;
the former Quantum reader cannot keep this replacement configuration ready.
The API advertises the existing guard capabilities only after startup checks.
The normalizer never registers as a consumer. Broker keys and mount privileges
remain in the separate broker process.

The app hides protected thumbnails while locked. No special encryption or secure
erasure of mobile thumbnail caches is added.

## Verification

Run project tools inside the VM development container. From this worktree:

```sh
dev bash -lc 'cd /home/dev/projects/fileBeans/.worktrees/filesystem-api/companion && /tmp/filebeans-go/go/bin/go test -race ./... && /tmp/filebeans-go/go/bin/go vet ./...'
dev bash -lc 'cd /home/dev/projects/fileBeans/.worktrees/filesystem-api && /home/dev/projects/fileBeans/.venv/bin/python scripts/validate-source.py'
sh scripts/test-filesystem-fuse.sh
```

The FUSE runner compiles test executables and uses the existing development
runtime with synthetic keys and a loopback-only CIFS fixture. It does not build
images or access production files. It covers encrypted cache persistence,
locking during response delivery, registry loss, boundary moves, local/CIFS
folder lifecycle and independent ciphertext recovery. Ordinary API tests cover
ranges, collisions, stale/concurrent writes, interrupted uploads, hidden paths,
FIFO rejection, cache invalidation and real image/video/PDF/text rendering.

Go tests requiring SQL still need `FILEBEANS_TEST_CONFIG` pointing to a dedicated
VM `.test-config` database; they skip without it. Apple source validation is
syntax/configuration checking, not Apple SDK compilation or XCTest execution.
Simulator tests, device playback/Files acceptance, runtime image validation and
live cutover remain separate steps requiring their applicable build/deployment
requests. Do not merge or change branch protections to enable a trial build.

## Requested build, cutover and rollback

Do not execute these steps without the corresponding explicit request.

1. Save current image digests, private configs, proxy routes and the installed
   app build number. Preserve Quantum's private database and original images.
   Configure Quantum to exclude `.filebeans-thumbnails` for clean rollback.
2. After committing the trial revision, prepare independent runtime contexts with
   `scripts/prepare-vault-runtime.py --filesystem` inside development. This
   compiles Linux binaries and prepares the filesystem Dockerfile; it does not
   prepare or fetch Quantum. Build requested images only with the VM image-builder
   procedure. The runtime includes FFmpeg, Poppler and pinned rclone. The normalizer
   needs the matching source revision to exclude thumbnail caches.
3. Request Apple build/testing from this feature branch, preserving app/domain
   identifiers and versioning. The native player needs real startup, seek,
   resume and cancellation checks on local, Samba and protected media. Exercise
   Apple Files create/edit/rename/move/copy/delete and conflict retention.
4. Stop Quantum before enabling the trial API against the same production roots.
   Switch the broker's consumer mode, deploy matching API/normalizer artifacts,
   and retain existing HTTPS companion routing. Never run both backends as
   active writers. No SQL migration is needed for this replacement.
5. Existing app connections reuse companion URL/token, IDs and Files domains.
   Original Quantum URLs and primary tokens remain stored for rollback. Direct
   API connections use a single URL/token. Changed URLs require an explicit
   token rather than silently forwarding credentials to a new endpoint.
6. For rollback, stop the trial API and normalizer, restore the previous broker
   executable/config (or set consumer mode false), restart the original
   companion/normalizer/Quantum images and restore the previous native app build.
   Existing data and encrypted folders remain in their existing formats. Software
   rollback does not undo file edits made during the trial. No cache deletion,
   database restore or ciphertext conversion is required.
7. Only after owner acceptance and device/live validation, merge the feature
   branch and retire Quantum's service and build dependencies. Preserve rollback
   artifacts according to the existing release procedure.

Current branch work makes no production configuration changes and triggers no
Apple build or deployment.
