# Dataset media API (feature trial)

The `datasetMediaV1` capability extends filesystem v1. All routes use the existing
Bearer credential. Sources are configured filesystem roots, including unlocked
protected folders. Drive roots are not accepted by dataset jobs.

| Route | Contract |
| --- | --- |
| `POST /v1/datasets/uploads?source=…&path=…` | Raw bytes, required `Idempotency-Key` and `X-Content-SHA256`; maximum 2 GiB, no overwrite. Returns upload ID, location and revision. Retries validate the existing file checksum. |
| `POST /v1/datasets/jobs` | Required idempotency key; JSON request described below. Returns a durable job. |
| `GET /v1/datasets/jobs/{id}` | State, error/code, inspection info or published result location. Passive read; no timer reset. |
| `POST /v1/datasets/jobs/{id}/cancel` | Cancel queued/running work; published outputs remain. |
| `POST /v1/datasets/jobs/{id}/retry` | Requeue failed, blocked or cancelled work. |
| `GET /v1/datasets/jobs/{id}/media/{kind}` | Prepared `preview` or `thumb0`…`thumb7`; authenticated ranges, current source revision/protection required. |
| `POST /v1/datasets/activity` | JSON array of `{source,path}` references. Refresh an already-unlocked global protection session through the broker; never unlock. |

Job requests have `operation` (`prepare` or `render`), `model` (`qwen` or `wan`),
`inputs` (`id`, `source`, `path`, `revision`), `clips`, and `destination`
(`source`, `path`). Preparation takes one input and no clips. Rendering takes
1–500 sources and 1–500 edits. Wan edits contain `id`, `videoID`, `start`, `end`,
optional normalized `crop`, and UTF-8 `caption`. Qwen edits contain `id`, `control`,
`target`, optional normalized `controlCrop`/`targetCrop`, and `caption`. Crops have
`x`, `y`, `width`, `height`, each measured against the oriented display image.

States are `queued`, `processing`, `ready`, `failed`, `blocked`, `cancelled`.
Locked journal/media access returns HTTP 423 with `code: locked`; callers instruct
users to unlock through a FileBeans mobile app. Source revision conflicts return
`source_changed`. The worker runs independently of copy jobs under the existing
single-instance database advisory lease. Active processing holds broker filesystem
leases and sends activity every 30 seconds. Queued work does not keep folders open.

Previews and scratch live beside source files under hidden `.filebeans-thumbnails`,
inside encryption for protected sources. Output staging lives in the destination
boundary under `.filebeans-stage`. Complete datasets publish by exclusive rename
into `MakeLORA-<job ID>`; the rename is also the crash-recovery publication marker.
Explicitly selected destinations can export protected source contents under the
existing personal-server authorization model. No source file is modified.

The normalizer excludes final dataset folder names as well as cache/staging
subtrees so it cannot change training layouts. Deploy the matching normalizer,
API and broker for the feature trial. Existing FileBeans app builds can still
unlock and browse content; no FileBeans app release is required.
