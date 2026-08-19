# Hellbox — Phase 1 Specification (Ripping)

*Draft 2 — 2026-07-27. Amendments from draft 1 are listed in §17.*

## 1. Purpose

Replace Automatic Ripping Machine with a purpose-built disc ripping service.

Phase 1 delivers exactly one capability, done well: **insert a disc, walk away,
come back to a complete, verified, immutable set of raw MKVs and an open tray.**

No transcoding. No metadata lookup. No library filing. Those are Phases 2 and 3,
and this document is written so they can be added without reworking anything
built here.

### Guiding invariants

These hold for all phases and should be treated as non-negotiable:

1. **A disc is never read twice.** Once ripped and verified, the raw files are
   permanent and immutable. Every later stage reads from them and writes
   elsewhere.
2. **The rips directory is self-describing.** If the database is lost, the rips
   tree alone contains enough information to rebuild it. The database is an
   index, not the system of record.
3. **The daemon never blocks on a human.** Anything ambiguous is recorded and
   deferred. Unattended operation is the default, not a mode.
4. **An open tray means success, and only success.**

---

## 2. Components

| Binary | Role |
|---|---|
| `hellboxd` | Long-running daemon. Owns the optical drives, the database, and the job queue. Runs under systemd. |
| `slay` | Terminal UI client. Connects to `hellboxd` over a unix socket. Stateless; may be started, killed, and restarted freely without affecting in-flight rips. |

The split exists so that closing a terminal, dropping an SSH session, or
rebooting a laptop cannot interrupt a rip. It also means a web UI added later is
just another client against the same interface, at near-zero additional cost.

### Deployment

`hellboxd` runs **on the host under systemd**, not in a container. It is a static
Go binary that needs `/dev/sr*`, and later `/dev/dri`. Containerising it would
reintroduce exactly the device-passthrough, group-ID, and `privileged: true`
problems that motivated leaving ARM. Jellyfin stays in Docker.

Runs as `charris` (uid 1000) with supplementary group `cdrom`.

### Dependencies

- **Go** — installed under `~/.local/go` rather than system-wide.
- **MakeMKV** (`makemkvcon`) — no package exists for Ubuntu 26.04.
  `scripts/install-makemkv.sh` builds `makemkv-oss` and `makemkv-bin` from the
  official tarballs. This is the only non-Go runtime dependency in Phase 1.
- **SQLite** — via the Go library; no separate service.

#### The MakeMKV key is an operational dependency, not just a licence

MakeMKV will not run **at all** without a valid registration key. The
"free for DVD" position describes which discs may be converted, not whether the
program starts, and this was confirmed directly with MakeMKV's maintainers on
the project forum. The published beta key expires roughly monthly.

For an appliance that is deliberately unattended, that is a scheduled outage
every few weeks, presenting as a rip failure with no apparent connection to the
disc or the drive — the exact class of mystery failure hellbox exists to remove.
The daemon therefore treats key expiry as a condition it repairs itself; see
§13. A purchased permanent key would remove the problem outright, but at the
time of writing MakeMKV's purchase page is unreachable, so the design cannot
assume one is obtainable.

---

## 3. Filesystem layout

The existing ARM-shaped tree is vestigial and is replaced. Nothing in
`/srv/media/arm` is retained (all of it will be re-ripped).

```
/srv/media/
  rips/            # immutable raw rips, one directory per physical disc
  work/            # Phase 2 transcode scratch (created now, unused in Phase 1)
  library/
    Movies/
    TV/
  music/
```

### Rip directory

One directory per unique physical disc:

```
/srv/media/rips/2026-07-27--still-game-series-1-disc-1--a3f9c2e10b47/
    disc.json         # parsed disc description (see §6)
    makemkv-info.txt  # verbatim `makemkvcon -r info` output
    rip.log           # verbatim ripping output
    title_00.mkv
    title_01.mkv
    ...
```

Directory name: `YYYY-MM-DD--<label-slug>--<fingerprint12>`

- `label-slug`: volume label lowercased, non-alphanumerics collapsed to `-`,
  truncated to 40 characters. Empty or generic labels (`DVD_VIDEO`, `LOGICAL_VOLUME_ID`)
  become `unlabeled`.
- `fingerprint12`: first 12 hex characters of the disc fingerprint (§5).

Naming is by **disc identity, never by guessed content**. Phase 3 will associate
titles with shows and films by writing records, not by renaming or moving
anything here.

Output files are named `title_NN.mkv` by MakeMKV title index — not by MakeMKV's
suggested filename, which is derived from its own guesswork and is unstable.

---

## 4. Drives

Multi-drive support is a Phase 1 requirement, not a later addition. The daemon
supports N drives ripping concurrently, where N is bounded only by hardware.

### Identity

Device paths (`/dev/sr0`, `/dev/sr1`) are **not** stable identifiers — with two
USB drives, enumeration order can change across reboots. Drives are identified by
their `/dev/disk/by-id/` symlink basename, e.g.
`usb-ASUS_SDRW-08D2S-U_...`, resolved to a current `/dev/srN` at runtime.

Currently attached: one `ASUS SDRW-08D2S-U`, a DVD±RW writer. It reports max
media `DVD+R-DL` and **cannot read Blu-ray**. Blu-ray support is deferred until
capable hardware exists; the code should not pretend otherwise.

### Detection

Each drive gets a dedicated worker goroutine that exclusively owns that device.
Disc presence is polled every 2 seconds. Polling is preferred over udev
subscription: no external dependency, and it cannot miss a state change across a
daemon restart the way an event subscription can.

**`CDROM_DRIVE_STATUS` alone is not trustworthy.** Draft 1 of this document
specified it as the sole detection mechanism. On the ASUS SDRW-08D2S-U — the
only drive this system has — it returns `CDS_DISC_OK` for an empty drive with
the tray closed, and continued to do so with the tray open. This was verified by
calling the ioctl directly, against a drive `makemkvcon`, `blkid` and a raw read
all agreed was empty. USB ATAPI bridges are widely unreliable here; the drive is
lying, and the kernel is faithfully relaying the lie.

Taken alone it would mean the daemon believes a disc is present on an empty
drive, scans, fails to open anything, and lands in `FAILED` with the tray shut —
on every startup, for a disc that never existed.

The kernel-level signals were all measured and none is sufficient alone:

| Signal | With an unreadable disc loaded | Verdict |
|---|---|---|
| `CDROM_DRIVE_STATUS` | `CDS_DISC_OK` | wrong |
| `CDROM_DISC_STATUS` | `CDS_NO_DISC` | wrong in the other direction |
| `/sys/block/srN/size` | previous disc's size | stale |
| read of sector 0 | `EIO` | true but uninformative |

Presence is therefore established with a **SCSI `TEST UNIT READY` issued through
`SG_IO`**, which asks the drive directly and answers in sense data. That
separates conditions every one of the signals above collapses together:

| Sense | Meaning | State |
|---|---|---|
| GOOD status | readable medium loaded | `DISC OK` |
| `0x3a/0x00`, `0x3a/0x01` | no medium | `EMPTY` |
| `0x3a/0x02` | no medium, tray open | `TRAY OPEN` |
| `0x04/0x01` | becoming ready | `LOADING` |
| `0x30/xx` | **incompatible medium** — a disc is present that this drive cannot read | `INCOMPATIBLE` |

`CDROM_DRIVE_STATUS` is retained only as a fallback for a device that refuses
`SG_IO`, where an answer that is sometimes wrong beats no answer at all.

### Incompatible media

A disc this drive cannot read is a distinct state, not a failure and not an
empty drive. It was found in practice: a disc believed to be a DVD turned out to
report `0x30/0x00 INCOMPATIBLE MEDIUM INSTALLED`, almost certainly a Blu-ray in
a drive that reads no Blu-ray.

- **It is not `EMPTY`.** Something is physically loaded and will stay there
  until a person removes it. Reporting an empty drive would leave the operator
  with no idea why nothing was happening.
- **It is not `FAILED`.** Nothing was attempted and nothing went wrong.
- **It is not retried.** No amount of re-reading turns a Blu-ray into something
  a DVD drive can open.
- **The tray stays shut**, per the standing convention: an open tray means the
  disc is done, and this one was never read.

Note that physical damage is a different condition entirely. A scratched disc
reports MEDIUM ERROR (sense key `0x03`), which is emphatically not an absent or
incompatible disc and must still be attempted.

> **Verification status.** GOOD status is unambiguous by construction, but has
> not been observed on this hardware — no readable disc has been through the
> drive yet. The empty, tray-open and incompatible cases are all confirmed
> against the real drive.

### Drive state machine

```
UNKNOWN ─→ EMPTY ─→ LOADING ─→ READY ─→ SCANNING ─→ RIPPING ─→ VERIFYING ─→ COMPLETE ─→ EJECTING ─→ EMPTY
                                          │                                                  ▲
                                          ├─→ DUPLICATE ─────────────────────────────────────┤
                                          │                                                  │
                                          └─→ FAILED (disc retained, tray stays closed)      │
                                                   └── operator retry / eject ───────────────┘
```

**On failure the tray does not open.** This preserves the "open tray means
success" convention — you can reshelve any disc the machine hands back without
checking anything. A failed disc stays in its drive with a persistent error in
the TUI until you retry or eject it manually. Automatic retry (`max_rip_attempts`,
default 2) happens before entering `FAILED`.

The tradeoff: a failed disc occupies its drive indefinitely. With multiple drives
this is acceptable, and it is far preferable to silently reshelving a disc whose
rip failed. Configurable via `eject_on_failure` for anyone who disagrees.

---

## 5. Disc fingerprinting

The fingerprint enables the single most useful unattended-operation feature: if
you insert a disc that has already been ripped, the daemon recognises it within
~20 seconds and ejects, instead of spending 40 minutes producing a duplicate.
This matters specifically because the operator is not watching.

Computed from `makemkvcon info` output only — no reading of the disc body:

```
SHA-256( volume_label ‖ title_count ‖ Σ sorted("<duration_secs>:<size_bytes>") )
```

Title entries are sorted before hashing to guard against any nondeterminism in
MakeMKV's enumeration order. Durations to the second combined with exact byte
sizes make collisions between genuinely different discs effectively impossible.

Known limitations, accepted:

- A different pressing or region variant of the same film fingerprints
  differently and will be ripped again. Correct behaviour — they are different
  discs.
- A badly damaged disc that MakeMKV enumerates differently on a second read will
  not match. Rare, and the `forget` command exists for manual correction.

---

## 6. Disc description (`disc.json`)

This file is the most important artifact Phase 1 produces, because it is the
entire input to Phase 3's identification work. Recording it thoroughly now means
episode-mapping logic can be developed, tested, and re-run indefinitely against
existing rips **with no disc in the drive**.

Per disc: fingerprint, volume label, disc type, total size, rip timestamp, drive
used, MakeMKV version.

Per title: index, duration, chapter count, byte size, source filename
(`VTS_01_1.VOB` — meaningful for DVD structure analysis), segment map, output
filename.

Per stream within each title: kind (video/audio/subtitle), codec, language,
channel count, resolution, frame rate, aspect ratio, and flags (default, forced).

> **Implementation note.** MakeMKV's robot mode (`-r`) emits `TINFO`/`SINFO`/`CINFO`
> lines keyed by numeric attribute codes. The exact code-to-attribute mapping is
> documented only informally and should be **verified empirically against a real
> disc during implementation** rather than trusted from any table. To make
> mis-parsing recoverable, the verbatim output is always preserved in
> `makemkv-info.txt`, so `disc.json` can be regenerated later without touching
> the disc again.

---

## 7. Ripping

Scan:

```
makemkvcon -r --cache=1 info dev:/dev/srN
```

Rip:

```
makemkvcon -r --progress=-same --messages=-stdout --cache=1024 \
           --minlength=<min_title_seconds> mkv dev:/dev/srN all <rip_dir>
```

`dev:/dev/srN` is used rather than `disc:N` because MakeMKV's disc index is
assigned per-invocation and is not stable across drives.

**Rip everything** above `min_title_seconds` (default 60). Pruning from disk
later is trivial; re-handling physical media is not.

Progress is parsed from the robot output stream:

- `PRGC:` / `PRGT:` — current and total operation names
- `PRGV:current,total,max` — progress values
- `MSG:code,flags,count,text,...` — human-readable messages and errors

All output is also written verbatim to `rip.log`.

---

## 8. Verification

Phase 1 verification is deliberately shallow, because ffmpeg is not yet a
dependency:

- `makemkvcon` exit code is 0
- expected number of output files present
- each file exceeds `min_output_bytes` (default 10 MB)
- each file begins with a valid EBML/Matroska magic sequence

Deferred to Phase 2, when ffmpeg is present anyway: full `ffprobe` validation and
cross-checking actual stream duration against the duration MakeMKV reported at
scan time. This is the check most likely to catch a subtly truncated rip, and it
is worth doing — just not worth adding a large dependency for in Phase 1.

A disc reaches `COMPLETE` only after verification passes.

---

## 9. Data model

SQLite in WAL mode at `/var/lib/hellbox/state.db`. The daemon is the only writer.
`slay` never opens the database — all reads go through the socket, which keeps
the interface honest and allows a remote client later.

```sql
CREATE TABLE drives (
  id            INTEGER PRIMARY KEY,
  stable_id     TEXT NOT NULL UNIQUE,   -- /dev/disk/by-id basename
  device_path   TEXT,                   -- current /dev/srN; may change
  label         TEXT,                   -- operator-assigned, e.g. "top"
  vendor_model  TEXT,
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL
);

CREATE TABLE discs (
  id            INTEGER PRIMARY KEY,
  fingerprint   TEXT NOT NULL UNIQUE,
  volume_label  TEXT,
  disc_type     TEXT,                   -- dvd | bluray
  title_count   INTEGER,
  total_bytes   INTEGER,
  first_seen    INTEGER NOT NULL,
  rip_dir       TEXT,                   -- relative to rips root; NULL until ripped
  makemkv_info  TEXT                    -- verbatim scan output
);

CREATE TABLE jobs (
  id            INTEGER PRIMARY KEY,
  disc_id       INTEGER NOT NULL REFERENCES discs(id),
  drive_id      INTEGER NOT NULL REFERENCES drives(id),
  state         TEXT NOT NULL,
  attempt       INTEGER NOT NULL DEFAULT 1,
  started_at    INTEGER,
  ended_at      INTEGER,
  error         TEXT,
  titles_total  INTEGER,
  titles_done   INTEGER
);

CREATE TABLE titles (
  id            INTEGER PRIMARY KEY,
  disc_id       INTEGER NOT NULL REFERENCES discs(id),
  title_index   INTEGER NOT NULL,
  duration_secs INTEGER,
  chapters      INTEGER,
  size_bytes    INTEGER,
  source_file   TEXT,                   -- e.g. VTS_01_1.VOB
  output_file   TEXT,
  ripped        INTEGER NOT NULL DEFAULT 0,
  verified      INTEGER NOT NULL DEFAULT 0,
  UNIQUE(disc_id, title_index)
);

CREATE TABLE streams (
  id            INTEGER PRIMARY KEY,
  title_id      INTEGER NOT NULL REFERENCES titles(id),
  stream_index  INTEGER NOT NULL,
  kind          TEXT,                   -- video | audio | subtitle
  codec         TEXT,
  language      TEXT,
  channels      INTEGER,
  resolution    TEXT,
  frame_rate    TEXT,
  flags         TEXT
);

CREATE TABLE events (
  id            INTEGER PRIMARY KEY,
  ts            INTEGER NOT NULL,
  job_id        INTEGER REFERENCES jobs(id),
  drive_id      INTEGER REFERENCES drives(id),
  level         TEXT,                   -- info | warn | error
  message       TEXT
);

CREATE TABLE schema_version (version INTEGER NOT NULL);
```

`titles` and `streams` are not needed by Phase 1 logic beyond verification counts.
They exist because Phase 3 needs queryable runtime and stream data, and capturing
it costs nothing now.

---

## 10. Client protocol

Unix socket at `/run/hellbox/hellbox.sock`, mode 0660, group `charris`.
Newline-delimited JSON. Hand-rolled request/response with an `id` field, plus
server-pushed events after `subscribe`. No RPC framework — the surface is small
and streaming over a socket is simpler than HTTP for this.

Phase 1 methods:

| Method | Effect |
|---|---|
| `status` | Full snapshot: all drives, active jobs, health check results |
| `subscribe` | Stream state deltas and events until disconnect |
| `history` | Past jobs, paginated |
| `disc.get` | Disc record with titles and streams |
| `eject` | Open a drive's tray |
| `retry` | Re-attempt a failed job |
| `cancel` | Abort an in-flight rip |
| `forget` | Drop a disc's dedupe record so it can be ripped again |
| `rescan` | Re-detect drives and re-run the health checks |

The protocol carries a version field from day one so Phase 2 can extend it
without ambiguity.

---

## 11. TUI (`slay`)

Built with Bubble Tea. Deliberately plain.

**Default view — one panel per drive:**

```
hellbox 0.1.0                                    2 drives · 41 discs ripped

 top    /dev/sr0    STILL GAME SERIES 1 DISC 1
        RIPPING     title 4 of 6      [████████████░░░░░░░░]  58%     ~12m left

 side   /dev/sr1    (empty)
        IDLE        waiting for disc


 recent
   14:02  top   scan complete — 6 titles, 7.4 GB
   13:58  top   disc detected — STILL GAME SERIES 1 DISC 1
   13:51  side  complete — AVATAR (2009), 1 title, 34m18s
```

Keys: `d` drives · `h` history · `l` log · `e` eject · `r` retry · `q` quit.

Health warnings render as a persistent banner, not a transient message. The
MakeMKV key expiry warning in particular must be impossible to miss — a silently
expired beta key is the single most likely cause of a mystery failure, and
diagnosing it badly is one of the things ARM does worst.

---

## 12. Configuration

Single TOML file at `/etc/hellbox/config.toml`. One config file, not the two
mutually-shadowing files the current stack maintains.

```toml
rips_dir           = "/srv/media/rips"
work_dir           = "/srv/media/work"
library_dir        = "/srv/media/library"

min_title_seconds  = 60
min_output_bytes   = 10485760
max_rip_attempts   = 2
eject_on_success   = true
eject_on_failure   = false

# See §13. Refreshing is attempted only after the installed key has been found
# bad, never speculatively and never on a schedule.
auto_refresh_key     = true
key_refresh_interval = "6h"
# makemkv_settings_path = "…/.MakeMKV/settings.conf"   # default: running user's
# beta_key_url          = "…"                          # default: built in

[[drives]]
stable_id = "usb-ASUS_SDRW-08D2S-U_..."
label     = "top"
enabled   = true
```

Drives are auto-discovered. The `[[drives]]` blocks are optional and exist only
to assign friendly labels or disable a drive.

---

## 13. Startup health checks

Run at daemon start and re-checked periodically; all results exposed via `status`
and displayed by `slay`:

- `makemkvcon` present, and its version
- MakeMKV key present and **not expired**
- At least one enabled drive detected
- `rips_dir` exists and is writable
- Free space above a warning threshold
- Socket path creatable
- Database schema version matches binary

### Key refresh

An expired key is the one health failure the daemon can plausibly repair
itself, and — per §2 — the one most likely to strand an unattended machine. When
a check finds the key expired or missing, the daemon fetches the currently
published beta key, installs it, and re-checks so the reported state reflects
what MakeMKV makes of the new key rather than what was hoped.

Constraints on that behaviour, all deliberate:

- **Reactive, never speculative.** No request is made until a check has already
  found the installed key bad. In normal operation the daemon makes no outbound
  requests at all.
- **Rate-limited.** An expired key with no replacement published must not become
  a request every health check.
- **An unchanged key is a warning, not a fix.** If the published key is the one
  already installed, MakeMKV has not issued a replacement yet. Nothing in the
  daemon can repair that, and reporting it as success would leave rips failing
  with no explanation.
- **Nothing that fails the key shape is written.** A change of forum layout is a
  refusal, not a `settings.conf` that leaves `makemkvcon` unable to start. The
  previous file is kept as `settings.conf.bak`.
- **The key never appears in a log or the UI.** It is a credential, and health
  detail strings are rendered verbatim by clients.

Scraping a forum post is not a durable interface, and this is understood to be a
workaround for a licensing model that suits a desktop tool rather than an
appliance. It is failure-tolerant by construction: when it breaks, it breaks
into a clearly reported health warning, which is the same state the operator
would have been in without it.

### Detecting a bad key

How MakeMKV reports key trouble was established by running 1.18.4 against each
state in turn, rather than assumed:

| Key state | What MakeMKV says |
|---|---|
| Valid | nothing |
| Invalid | `MSG:5020` "The stored activation key is invalid. I guess someone tampered with settings…" |
| Invalid | `MSG:5021` "This application version is too old… or enter a registration key" |
| **Absent** | **nothing at all** |

Two consequences shape the implementation:

- **Silence does not mean a good key.** With no key at all, MakeMKV starts and
  says nothing, so the settings file is inspected directly. Reading the silence
  as success reported "accepted" for a machine that had never been registered —
  the state of every fresh install, and exactly the case that needs the refresh.
- **Match on message codes, not prose.** No reasonable substring list would have
  anticipated the wording of 5020. Codes are stable where wording is not, so
  they are matched first, with prose kept as a backstop for codes not yet seen.

Because `makemkvcon` locates its key at `$HOME/.MakeMKV/settings.conf`, the
daemon hands it a `HOME` derived from the configured settings path. Otherwise
the setting would govern only where hellbox *writes* the key while `makemkvcon`
read a different file — a refresh reporting success and changing nothing. This
matters under systemd, where `HOME` need not be the account's own.

Because the daemon rewrites `settings.conf`, the systemd unit keeps
`ProtectHome=read-only` but grants write access to the MakeMKV settings
directory alone. Without that the feature would work when run by hand and fail
silently under systemd.

Confirmed against the running service by reading its mount namespace:

```
ro   /home
rw   /home/charris/.MakeMKV
```

Note that this cannot be checked with `systemd-run --user`, which ignores
`ProtectHome` entirely — a test there reports the home directory writable no
matter what the unit says.

---

## 14. Phase 1 non-goals

Explicitly out of scope, listed so they don't creep in:

transcoding · metadata lookup · library filing · renaming for Jellyfin ·
web UI · notifications · Blu-ray (no capable hardware) · multi-disc box-set
grouping · subtitle OCR · watch-folder ingest for YouTube-sourced material

---

## 15. Hooks for later phases

Built now, unused now:

- `disc.json` and the `titles`/`streams` tables carry the full runtime and stream
  data that Phase 3 title-role assignment needs.
- The job state machine is table-driven so Phase 2 states (`TRANSCODING`,
  `VALIDATING`, `IMPORTED`) are additions, not surgery.
- The `events` table gives Phase 2 a place to log without new plumbing.
- The socket protocol is versioned.
- A `source` abstraction is defined with a single implementation (optical disc),
  so a watch-folder source for YouTube-pulled VHS captures and commercials is an
  additive change.

---

## 16. Open questions

1. ~~**Repository location.**~~ Settled: `~/src/hellbox`, its own git
   repository, with the Jellyfin compose stack left separate in
   `~/media-box-rebuilt`.
2. **Removing `/srv/media/arm`.** ARM output to be re-ripped. Deleting it is a
   destructive step that should be run only on explicit confirmation, not folded
   into a setup script. (The directory is already gone from `/srv/media`; confirm
   nothing wanted was in it before considering this closed.)
3. **Drive labels.** "top"/"side" above is a placeholder. Physical labelling
   convention is worth settling before a second drive arrives.
4. **Blu-ray.** Deferred for want of capable hardware. Worth noting that Blu-ray
   is the part of MakeMKV that is actually shareware, so acquiring a Blu-ray
   drive and relying on the beta key are decisions that interact.

---

## 17. Amendments since draft 1

Recorded so the reasoning is not lost, and so anything derived from draft 1 can
be re-checked:

| § | Change |
|---|---|
| 2 | MakeMKV's key requirement documented as an operational dependency: it gates whether the program runs at all, not merely which discs it will convert. |
| 4 | Detection moved to SCSI `TEST UNIT READY` after `CDROM_DRIVE_STATUS` was found to report `CDS_DISC_OK` for a drive holding no readable medium. New `INCOMPATIBLE` state for a disc the drive cannot read. |
| 12 | Key refresh configuration added. |
| 13 | Key refresh behaviour specified, including how a bad key is actually detected — established by testing MakeMKV 1.18.4 against each key state rather than assumed. |
| 16 | Repository location settled. |
