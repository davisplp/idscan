# idscan — ID Card Phone Number Scanner & Attendance Marking

Phase 1 (scan pipeline) and phase 2 (attendance matching) are both
implemented and tested end-to-end against a real Postgres database — see
"Verified results" below.

## What's implemented

**Scan pipeline** (`/api/scan` — no database required):
- `internal/preprocess` — grayscale + contrast stretch + upscaling, stdlib only.
- `internal/ocr` — Tesseract via `gosseract`, tuned for short structured text.
- `internal/extract` — label-anchored "Contact No." extraction + Kenyan
  E.164 normalization.
- `internal/pipeline` — the shared decode → preprocess → OCR → extract
  sequence, used by both `/api/scan` and `/api/attendance/scan`.

**Attendance marking** (`/api/attendance/scan` — requires `DATABASE_URL`):
- `internal/match` — finds a student by phone number against the existing
  `students` table, tolerating mixed formatting already in the roster
  (`+254...`, `0740...`, with/without spaces) by comparing the last 9
  digits rather than requiring an exact string match.
- `internal/attendance` — records every scan attempt in `scans` (the audit
  trail / accuracy dataset), then marks attendance in
  `attendance_records`, enforcing **one record per student per day** at
  the database constraint level (not just application logic) so
  concurrent scans from two entrances can't race past it.
- `migrations/0001_attendance.sql` — adds `scans` and `attendance_records`,
  foreign-keyed to your existing `students` and `hubs` tables.
- `migrations/0002_phone_lookup_index.sql` — optional expression index so
  phone lookups stay fast as the roster grows, instead of a sequential
  scan per request.

**Frontend** (`web/index.html`):
- Live camera viewfinder with a card-shaped alignment guide, crop-to-guide
  before upload, client-side blur/brightness check, and a confirm/edit
  screen showing the photo next to the extracted number.
- `web/test.html` — plain file-upload dev fallback (no camera needed).

Unit tests: `internal/extract/extract_test.go` covers extraction and
normalization, including both real card layouts from the sample PDF and a
negative test proving the card number is never mistaken for a phone
number.

## Verified results (this build)

Tested against a real Postgres database seeded with your actual schema and
a real phone-camera photo of Yomlat Gatluak Geng's card (see conversation):

| Scenario | Result |
|---|---|
| First scan of a registered card | `attendance_status: "marked"`, correct student identified, 1 row inserted into `attendance_records` |
| Same card scanned again same day | `attendance_status: "already_marked"`, **no new row created** — verified directly in the database (still exactly 1 row) |
| Duplicate insert attempted directly in SQL | Rejected by the `attendance_records_one_per_day` UNIQUE constraint — proven at the database level, not just the application layer |
| Card with a phone number not in the roster | `attendance_status: "unmatched"`, scan still logged to `scans` for accuracy tracking |
| Every scan attempt above | Logged to `scans` regardless of outcome — 4 scans logged across the 3 scenarios above |

## Requirements

- Go 1.21+
- Tesseract OCR + dev headers:
  ```
  apt-get install -y tesseract-ocr libtesseract-dev libleptonica-dev pkg-config build-essential
  ```
- Postgres (only needed for `/api/attendance/scan`; `/api/scan` works without it)
- `CGO_ENABLED=1` (gosseract's cgo bindings)

## Build & run

```bash
cd idscan
CGO_ENABLED=1 go build -o idscan-server ./cmd/server
```

**Without a database** (scan pipeline only):
```bash
./idscan-server
```

**With attendance marking:**
```bash
# Run once against your database:
psql "$DATABASE_URL" -f migrations/0001_attendance.sql
psql "$DATABASE_URL" -f migrations/0002_phone_lookup_index.sql  # optional but recommended

DATABASE_URL="postgres://user:pass@host:5432/dbname?sslmode=disable" ./idscan-server
```

The server logs which mode it's in on startup:
```
database connected — /api/attendance/scan enabled
```
or
```
DATABASE_URL not set — /api/attendance/scan disabled, /api/scan still works
```

## API

**`POST /api/scan`** — OCR only, no database. Same as before.

**`POST /api/attendance/scan`** — OCR + match + mark attendance:

```bash
curl -X POST -F "image=@card.jpg" http://localhost:8080/api/attendance/scan
```

```json
{
  "ok": true,
  "normalized_phone": "+254740133967",
  "attendance_status": "marked",
  "student": { "first_name": "Yomlat", "last_name": "GatLuak" },
  "marked_at": "2026-09-16T18:44:25Z",
  "message": "attendance marked"
}
```

`attendance_status` is one of:
- `"marked"` — new attendance record created for today
- `"already_marked"` — student already marked present today; `previous_marked_at` is set instead of `marked_at`
- `"unmatched"` — phone number read successfully, but no student record matches it

## Performance

Measured against real card photos: a scan takes roughly **1.0-1.5 seconds**
end to end (decode + preprocess + OCR + database match + attendance
write). Per-stage timing is logged on every scan (`api: attendance scan
timing — decode=...ms preprocess=...ms ocr=...ms extract=...ms
total=...ms`), so any future change to the pipeline has a real before/after
number rather than a guess.

**The single biggest cost is OCR itself (~85-95% of total time)** — this
was cut roughly in half by fixing a real bug: `internal/ocr` originally
called `client.Text()` for the recognized text and `client.GetBoundingBoxes()`
separately for the confidence score. Benchmarking against real scans
showed each call independently re-runs Tesseract's full recognition pass
(gosseract does not cache the result between separate `Get*`/`Text`
calls) — so getting both text and confidence cost roughly *twice* a single
real OCR pass, for no benefit. Since `GetBoundingBoxes` already returns
each word's recognized text, the fix reconstructs the full text locally
from that single call instead of also calling `Text()` — this measured
~2.3s → ~0.9-1.0s per scan on a real phone photo, with no loss of
accuracy or the confidence score.

One implementation detail worth knowing if you touch `internal/ocr`:
gosseract's `BoundingBox.BlockNum`/`ParNum`/`LineNum` fields were observed
to always be 0 (not populated), so line reconstruction clusters words by
their Y-coordinate instead, which was empirically accurate. This matters
for correctness, not just formatting — `internal/extract`'s label-anchored
matching depends on the "Contact No." line being distinct from the "Card
No." line, so a bad line-merge here could make extraction grab the wrong
digits. See `reconstructLines` in `internal/ocr/ocr.go` for details, and
its behavior is covered indirectly by the existing `internal/extract`
tests (which exercise line-separated input).

**What was tried and intentionally not pursued for this pass:**
- Pooling/reusing Tesseract engines across requests — benchmarked first;
  engine creation costs ~1-2ms, negligible next to OCR itself, so this
  wouldn't have helped and would have added concurrency complexity for no
  measured benefit.
- Reducing the preprocessed image resolution to speed up OCR further —
  a real lever (smaller image → less pixel data for Tesseract to process),
  but changing it without accuracy data risks trading speed for missed
  scans. Worth trying with the timing logs above as a guide once there's
  a larger sample of real scan outcomes to check accuracy against.

## Matching: card number first, phone as fallback

`/api/attendance/scan` extracts **both** the card's own identifier
("Card No." / "Identification No.") and the phone number from every scan,
and matches in this order:

1. **Card number** (`students.card_number`) — exact, case-insensitive
   match. Preferred because it's unambiguous once populated: no format
   normalization needed, and (once `students_card_number_uidx` is added —
   see `migrations/0003`) guaranteed unique per card.
2. **Phone number** — the original fallback, used when the card doesn't
   have a `card_number` on file yet, or when that field wasn't legible in
   the photo.

The response's `matched_via` field (`"card_number"` | `"phone"`) records
which path actually identified the student, and the same value is stored
per-row in `scans.matched_via` — so once there's real scan volume,
```sql
SELECT matched_via, count(*) FROM scans GROUP BY matched_via;
```
tells you how often each path is actually used in practice, which is
useful for deciding how much it's worth pushing to get `card_number`
fully backfilled across the roster.

Verified end to end against the real card image and a synthetic
phone-only card (no Card No. field at all): the first correctly matched
via `card_number`, the second correctly fell back to `phone` and still
identified the same student — see conversation history for the exact
`scans` table output proving both paths land distinctly.

## Hub authentication (hub_tags — supersedes the earlier hubs-based design)

`/api/attendance/scan` requires a valid session, obtained by logging in
against **`hub_tags`** — a small, clean, authoritative table of ~35
scan-session identities (`AIY`, `ASA`, `YOM`, ...) seeded directly from a
reference list, **not** derived from the existing `hubs` table.

**Why a separate table, not `hubs.login_code`** (added in migration
0005, now superseded): the card-number prefix codes were shown, via two
independent investigations, to map to *multiple* different `hubs.id`
values in production — e.g. `EWI` spans 5 distinct rows, `GKP` spans 10.
That's a data-quality problem in the existing `hubs` table, not something
this project should silently paper over by guessing which row is "the
real one" per code. `hub_tags` sidesteps it entirely: a fresh table with
its own UUIDs, with no dependency on `hubs`'s data quality at all.

**Cross-hub scanning is expected, not a bug.** A logged-in tag can scan a
student belonging to any other tag — e.g. `AIY` scanning a `YOM`-coded
student's card — and this is exactly the point of keeping
`scanning_hub_tag_id` (who's scanning) independent of the matched
student's own `hub_id` (where they're enrolled). Verified end to end: an
`AIY` session scanning a `YOM` student correctly marks attendance under
the student's own hub while recording `AIY` as the scanning tag.

**Endpoints:**
- `GET /api/auth/hub-tags` — public, read-only list of `{code, name}` for
  the frontend's login dropdown. No secrets in the response.
- `POST /api/auth/hub-login` — `{login_code, pin}` → session token. Same
  PIN + lockout mechanics as before (see below).

**Setting a tag's PIN** (no admin UI yet):
```sql
-- Generate a hash via internal/hubauth.HashPIN("1234"), then:
UPDATE hub_tags SET pin_hash = '$2a$...' WHERE code = 'AIY';
```
A tag with no `pin_hash` set cannot log in — set one before a hub is
expected to start scanning.

**What's unused/deprecated now**: `hubs.login_code`, `hubs.pin_hash`,
`hub_sessions`, and `scans.scanning_hub_id` /
`attendance_records.scanning_hub_id` (from migrations 0005-0006) are left
in place but no longer written to — not dropped, to avoid a destructive
migration on columns that may already have data. `scanning_hub_tag_id`
(migration 0007) is what the application actually uses going forward.

**Design choices carried over from the original design, unchanged:**

 **A short PIN's real defense is lockout, not the hash algorithm.** A
  4-6 digit PIN has only 10,000-1,000,000 possible values — bcrypt alone
  doesn't meaningfully protect that if unlimited guesses are allowed. What
  actually stops brute-forcing is `hubs.failed_login_attempts` /
  `locked_until`: 5 wrong attempts locks the hub out for 15 minutes.
  Verified end to end: a 6th consecutive wrong attempt is rejected with a
  lockout message, and the database shows the correct `locked_until`
  timestamp.
- **Sliding session expiration**, not a fixed timeout from login. Each
  authenticated request extends the session by another
  `hubauth.SessionDuration` (4 hours) — a hub actively scanning stays
  logged in indefinitely through the day; one left idle expires a few
  hours after its last use, matching how a shared device actually gets
  used.
- **The frontend persists the token in `localStorage`**, appropriate for
  a shared kiosk device that shouldn't need re-login on every page reload.
  A stale/expired token is only actually caught server-side on the next
  scan attempt (which 401s and drops back to the login screen) — this is
  intentional simplicity, not a security gap, since the server is the
  actual source of truth on session validity either way.

## Design decisions worth knowing about

**Attendance uniqueness enforced by the database, not application code.**
`markAttendance` uses `INSERT ... ON CONFLICT (student_id, session_date) DO
NOTHING` rather than "check if a record exists, then insert" — the latter
has a race window if two instructors at two entrances scan the same card
within the same request-handling window. Relying on the UNIQUE constraint
makes "already marked" outcome correct even under concurrent scans.

**Attendance day is defined in Africa/Nairobi time**, regardless of what
time zone the server itself runs in (`internal/attendance/attendance.go`).
This matters if you ever deploy to a US-region cloud host — without this,
"today" could flip at the wrong wall-clock time for your hubs.

**Every scan is logged, matched or not.** `scans` exists specifically so
you can later ask "what fraction of scans fail to match, and why" —
without this table, that data disappears the moment the HTTP response is
sent.

**Phone matching tolerates existing formatting rather than requiring
migration/cleanup first.** `internal/match` strips non-digits and compares
the last 9 digits, so it works against your current roster as-is. The
optional index in `migrations/0002` keeps this fast without needing to
touch existing data.

**No OpenCV / gocv, no `golang.org/x/image` dependency** — see the earlier
note; still the top candidate upgrade once you're testing against a wider
variety of real phone photos (skewed, poor lighting, older phone cameras).

## Next steps (in rough order)

1. **Auth**: gate both scan endpoints behind login/session middleware —
   right now anyone with the URL can mark attendance.
2. **Instructor-facing UI polish**: show a running count of who's been
   marked today at a given hub; a way to manually resolve `unmatched`
   scans (search the roster, assign, retry).
3. **Perspective correction** (`gocv`) once real phone-photo testing shows
   skew/angle is a meaningful failure mode.
4. **Reporting**: attendance-rate views/queries over `attendance_records`
   per hub, per cohort, over time.
5. **Ambiguous-match handling**: `internal/match.ByPhone` currently returns
   an error if two students share a phone suffix (a data-quality issue) —
   decide how the instructor-facing flow should surface and resolve that
   case rather than just failing the scan.

