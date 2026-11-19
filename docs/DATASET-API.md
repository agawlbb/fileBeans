# Dataset originals API

`datasetOriginalsV1` provides direct original access and export-only rendering.
Routes require the existing Bearer credential and configured filesystem roots,
including protected folders. Dataset operations do not use Drive.

| Route | Contract |
| --- | --- |
| `GET /v1/fs/resources?source=…&path=…` | Original path, MIME type, size, revision, optional opaque `vault` identity. Register this reference in MakeLORA; no preparation job. |
| `GET /v1/datasets/source/info?source=…&path=…&revision=…&vault=…&model=…` | Read-only dimensions, duration, optional UTF-8 sibling caption. Creates no media or cache. |
| `GET` or `HEAD /v1/datasets/source/content?source=…&path=…&revision=…&vault=…` | Original bytes, ranges, MIME type, ETag, revision validation, and a broker lease through response completion. |
| `POST /v1/datasets/uploads?source=…&path=…` | Raw original bytes with idempotency key and SHA-256; maximum 2 GiB, no overwrite. Temporary staging is removed after publication or failure. |
| `POST /v1/datasets/jobs` | Explicit export only: `operation: render`, model, original inputs, edits, destination, and required idempotency key. |
| `GET /v1/datasets/jobs/{id}` | Export state, error/code, output location. Passive read; no timer reset. |
| `POST /v1/datasets/jobs/{id}/cancel` | Cancel export; published outputs remain. |
| `POST /v1/datasets/jobs/{id}/retry` | Retry a failed, blocked, or cancelled export. |
| `POST /v1/datasets/activity` | Array of `{source,path,vault?}` references. Refresh the existing unlocked one-hour session; never unlock. |

Inputs contain `id`, `source`, `path`, `revision`, optional `vault`. Its opaque
identity allows locked errors even while protected locations are hidden. Locked
access returns HTTP 423 and instructs users to unlock through a FileBeans mobile
app. Changed revisions return `source_changed`.

Rendering accepts 1–500 sources and edits. Wan edits contain `id`, `videoID`,
`start`, `end`, optional normalized `crop`, and UTF-8 `caption`. Qwen edits contain
`id`, `control`, `target`, optional normalized `controlCrop`/`targetCrop`, and
`caption`. Crops use oriented source coordinates. Only explicit export encodes,
trims, and crops media. Originals remain unchanged. Encoding writes directly into
`.filebeans-stage` in the output destination, then publishes by exclusive rename
into `MakeLORA-<job ID>`.

Preparation requests are rejected. Old preparation jobs are retired without
executing them; the old media route returns HTTP 410. Existing originals and
published outputs are not removed. MakeLORA preview videos and thumbnail files
are no longer created.

Active accesses and exports renew inactivity through the existing broker.
Exports hold filesystem leases and renew every 30 seconds. Status polling does
not keep protected content unlocked. The existing normalizer excludes final
dataset folders and internal staging. Existing FileBeans mobile apps provide
unlocking; no FileBeans app release is required for this API change.
