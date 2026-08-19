# Hellbox v2 — Design

*Draft 1 — 2026-08-08. Supersedes [`phase1-spec.md`](phase1-spec.md) for everything
it covers. Written after a full v1 that ripped, encoded and filed real discs
unattended; every claim marked **measured** was observed on this machine, and
every claim marked **assumed** was not.*

---

## 1. What this is

A local appliance that turns a shelf of DVDs and Blu-rays into a correctly
named, correctly ordered Jellyfin library, with as little proprietary software
in the path as the discs allow, and without a person watching it.

Three things it must do well, in order of how much they hurt when done badly:

1. **Read the disc.** Every disc, including the region 2 ones, without spending
   a drive region change and without a licence key that expires.
2. **Know what it read.** A film goes to `Movies/Roman Holiday (1953)/`. An
   episode goes to `TV/Still Game/Season 03/Still Game - S03E05 - Doacters.mkv`
   — the right season, the right number, the right order.
3. **Ask for help exactly when it needs it, and never otherwise.** Ripping never
   waits for a person. Filing may.

v1 does (1) and does it well. v1 does not do (2) at all — it produces
`TV/Still Game/Still Game - c887d54d5276 - t00.mkv` and expects you to rename it
by hand — and it has no mechanism for (3), because its governing invariant
forbade one.

v2 keeps v1's ripping almost intact, replaces its dependency on MakeMKV,
and adds a catalog and a web UI that between them make (2) and (3) possible.

### The governing split

> **Go owns facts about bytes. Rails owns judgments about meaning.**

Everything in the architecture falls out of that line. Whether a title verified,
how many sectors dvdbackup could not read, which render node encoded a file —
facts, and they live in Go. Whether this disc is season 3 disc 2, which title is
episode 5, whether the OCR read "Doacters" or "Docaters" — judgments, revisable,
and they live in Rails where a person can correct them.

---

## 2. What v1 taught us

This section is the reason v2 can be built quickly. Everything here was paid for
once already.

### 2.1 The hardware lies, and the kernel relays the lie

**Measured.** The ASUS SDRW-08D2S-U reports `CDS_DISC_OK` from
`CDROM_DRIVE_STATUS` for an empty drive with the tray *open*. Every kernel-level
signal was measured and none is sufficient alone:

| Signal | With an unreadable disc loaded | Verdict |
|---|---|---|
| `CDROM_DRIVE_STATUS` | `CDS_DISC_OK` | wrong |
| `CDROM_DISC_STATUS` | `CDS_NO_DISC` | wrong the other way |
| `/sys/block/srN/size` | previous disc's size | stale |
| read of sector 0 | `EIO` | true but uninformative |

Presence comes from a SCSI `TEST UNIT READY` through `SG_IO`, read out of the
sense data. **Keep this code exactly as it is.** `internal/drive/` is the most
thoroughly validated package in the repo and none of its problems have changed.

The same lesson generalises: `ffprobe`'s opinion, `dvdbackup`'s exit code and
MakeMKV's silence are all unreliable narrators. Ask the thing that knows, and
check the answer against something independent.

### 2.2 MakeMKV is the single most fragile dependency, and it is now avoidable

**Measured.** MakeMKV will not start at all without a valid registration key.
"Free for DVD" describes which discs it will convert, not whether the program
runs — confirmed with its maintainers on the project forum. The published beta
key expires roughly monthly. For an appliance nobody is watching, that is a
scheduled outage every few weeks presenting as a disc fault.

v1 answered this with `internal/makemkv/key.go`: scrape the forum post, install
the key, re-check. It works, and it is a load-bearing workaround built on a
forum's HTML layout.

**It is no longer necessary.** ffmpeg 8.0.1, as shipped in Ubuntu 26.04 on this
machine, is built `--enable-libdvdread --enable-libdvdnav --enable-libbluray`
and carries a **`dvdvideo` demuxer** (measured, `ffmpeg -h demuxer=dvdvideo`):

```
-title        <int>  title number (0=auto)
-pgc          <int>  entry PGC number (0=auto)
-chapter_start/-chapter_end
-preindex     <bool> enable for accurate chapter markers, slow (2-pass read)
-region       <int>  playback region number (0=free)
-menu         <bool> demux menu domain
-menu_vts     <int>  menu VTS (0=VMG root menu)
-angle        <int>
```

That is title selection, chapter preservation, region-free playback and menu
extraction — every job v1 used MakeMKV for, in a dependency that is already
installed, already GPL, and has no key. §4.2 specifies the path built on it.

The lesson is not "MakeMKV bad". It is that v1 committed to a proprietary
enumerator before checking whether the free stack had caught up, and it had.

### 2.3 The region problem is solved, and the solution also solves region 2

**Measured.** SCSI `REPORT KEY` (format `08h`) on this drive:

```
type code      : 0     no region set
region_mask    : 0xff  nothing playable
user changes   : 5 of 5 remaining
rpc_scheme     : 1     RPC-2, enforced in firmware
```

An RPC-2 drive with no region set will not perform CSS authentication. Scanning
works — IFO structures are unencrypted, so all 7 titles of `ROMAN_HOLIDAY`
enumerate with correct sizes and durations. Ripping does not: MakeMKV logs
`Region setting of drive ... does not match` thirty times, then
`Exception: Error while reading input`, and stops reading **without exiting**.
One such rip sat for seventeen hours before `rip_stall_timeout` existed.

libdvdcss does not need that authentication. When the drive refuses to hand over
a title key it derives the key from the disc's own data. v1 exploits this by
copying the disc out with `dvdbackup` and ripping the copy — half an hour per
disc, and the region counter untouched at 5 of 5.

**The generalisation v1 missed:** if libdvdcss is doing the decryption, the
drive's region is irrelevant for *every* disc, not just the ones that fail. And
if the drive's region is irrelevant, so is the disc's. **A region 2 DVD is not a
special case — it is the same case.** v2 therefore:

- never sets a drive region, ever;
- routes all CSS decryption through libdvdcss;
- treats the ~5% of the collection that is region 2 identically to the rest.

The 5-of-5 change counter is a resource to be *never spent*, not conserved.
Setting it would lock the drive to one region and make a mixed-region collection
unservable by that drive forever.

What region 2 *does* change is downstream, and v1 has no answer for it: see
§5.4 on PAL speedup, which is the one place region genuinely matters.

### 2.4 Exit codes are not verification

**Measured, and expensively.** `dvdbackup` exits **successfully** having written
a copy it could not read. A Star Trek disc produced a `VTS_02_5.VOB` of five
gigabytes: one gigabyte of real data, a four gigabyte hole, then stray writes at
an offset that looks like an absolute disc sector used as a file position.
MakeMKV then reported one title where the drive had reported four, and **three
episodes were silently lost** — a rip that verified clean and was three-quarters
missing.

The fix in `internal/decrypt/decrypt.go` is the model for all verification in
v2: check the thing against a specification, not against the program's opinion
of itself. A DVD VOB cannot exceed 1 GB less one 64 KB block; that is in the
DVD-Video spec, so a larger file is not a big VOB, it is a broken one.

Three derived rules, all of which v1 arrived at the hard way and all of which
carry forward:

- **Verify on read as well as on write.** A copy that was corrupt when made is
  still corrupt when found. v1 initially only verified at write time, so a bad
  cached copy won forever and a cleaned disc would never be re-read.
- **Completion is a separate artifact.** `.complete` is written last. "The
  directory exists" cannot mean "the copy is usable", because a daemon killed
  mid-decrypt leaves most of a disc.
- **Judge a partial copy by running time, not title count.** Commit `646e10a`.
  Counting titles hides the case where one long title came back short.

### 2.5 A volume label is not a title

**Measured, across the actual shelf.** The label is at most 11 characters on the
ISO9660 filesystem most DVDs carry, upper-cased, chosen by an authoring house
with no interest in being parsed. This collection contains:

- `STNGD3`, `STNGD5`, `STNG4`, `NEXTGEN`, `NEXTGEN2` — five schemes for one
  television series;
- **six physically different Still Game discs all carrying the byte-identical
  string `STILL_GAME`**;
- `SPACEBALLS_LB`, where `LB` is letterbox — a property of the transfer, not a
  word in the title;
- discs whose label is `DVD_VIDEO` or nothing at all.

No parser separates those, because the information is not there. v1's response
was the right one and survives into v2 intact: **several independent nets, each
saying what it thinks and how sure it is, resolved by agreement rather than by
whichever ran last** (`internal/identify/identify.go`).

The design rule that came out of it is the important part:

> A net that guesses confidently is worse than one that abstains. The whole
> design assumes a stronger net can overrule a weaker one, and it cannot
> overrule a fabrication that arrived with high confidence.

Hence `LabelNet` reporting `STNG` as an abbreviation at confidence 0.15 rather
than expanding it to Star Trek: The Next Generation from a lookup table that
would be wrong for the first disc nobody thought of.

### 2.6 Menu OCR works, because the runtime is a checksum

**Measured.** OCR of a DVD menu is not clean — 720x480, ornate fonts, artwork
behind the text. A real disc produced `On Location (x1:17)` and
`ALTERNATE ENDIN® (1:01)`.

The insight in `internal/identify/menutext.go` is that the name does not have to
be clean, because the *disc already told us how long every title is*. A running
time printed beside a name says which title the name belongs to. The name only
has to be legible enough to be worth reading; the pairing carries the
confidence. Lines with no time are dropped, which correctly discards "Play",
"Scene Selection" and "Main Menu" — the exact lines that would otherwise be
filed as episode names.

v2 extends this from "which title is this extra" to "which episode is this",
which is the highest-value signal available for the ordering problem (§5.3).
It also gets a much better way to capture the frames: `ffmpeg -f dvdvideo -menu`
rather than v1's plan of rendering menus by hand.

### 2.7 Classification by shape works, and its failures are instructive

**Measured.** A film disc is lopsided; an episode disc is level. Two corrections
were needed and both are worth keeping:

- **Compare against the median, not the second-longest** (commit `516027f`). A
  DVD routinely carries its feature twice — widescreen and fullscreen, or the
  variants of a seamless-branching disc. *National Treasure* offers two
  identical 131-minute titles among nineteen extras, and comparing the top two
  made the most obviously film-shaped disc in the collection look like neither a
  film nor a set of episodes.
- **Ask the shape of the remainder first** (commit `a485595`). A Star Trek disc
  holding one 90-minute two-parter and two 45-minute episodes has a longest
  title exactly twice its median, which is the textbook definition of a film
  disc. Take the longest title away, and what remains on an episode disc is
  still episodes.

Still unhandled, and acknowledged as such in the v1 README: a concert disc, an
anthology, a film with a feature-length documentary. v2's answer is not a better
heuristic — it is that classification stops being the final word and becomes one
net among several, with a human review queue behind it.

### 2.8 Do not trust the disc's default audio flag

**Measured.** v1 originally picked the audio track the disc flagged as default,
with channel count breaking ties. Many discs flag their *stereo* track as
default, for players that cannot handle more, and 5.1 tracks were being dropped.

The order is now channel count first, default flag only as a tiebreak, after
commentary and wrong-language tracks are already gone. The reasoning generalises
beyond audio:

> Trusting that flag means letting the disc decide something it has no idea
> about: what this library is played back on. Channel count is a claim about
> content; the default flag is a claim about intent, made in 2003, for someone
> else's television.

Also from `internal/transcode/select.go`, both worth keeping: a language filter
that matches nothing is treated as no filter (many discs tag nothing, and a
strict filter would produce a silent film and call it a success); and *kept*
tracks are logged as well as dropped ones, because otherwise "dropped nothing"
and "there was only ever one track" read identically.

### 2.9 Cap the bitrate, do not fix the size

**Measured, on two real discs at quality 20:**

| Source | At quality 20 alone | With a ceiling |
|---|---|---|
| 1953 film, clean, 5.4 GB | 1.78 GB (1.36 Mb/s — never near the cap) | 1.5 GB |
| 2002 sitcom, noisy, 1.1 GB | **1.0 GB — larger than its source** | 475 MB |

The same quantizer that compressed a film threefold spent its entire budget
preserving broadcast noise. A quality cap plus a bitrate ceiling serves both
without the encoder needing to know what it is looking at. Quality 20 measures
SSIM 0.978; 18 costs another 500 MB per film for 0.0017 SSIM.

VAAPI is ~5× faster than software for output within 0.0013 SSIM. Nothing is
scaled and nothing is resampled: the stack this replaced ran PAL television
through a "HQ 720p30 Surround" preset and produced 900 MB episodes that looked
worse than the disc.

Interlacing is **detected per file, not configured**. On one disc here the
feature is progressive film and an extra is interlaced video, so a per-disc
setting would have been wrong either way.

### 2.10 Never clobber, and reconcile rather than react

Three v1 behaviours that cost nothing and prevent real losses:

- **A rip directory is never overwritten.** The successful `ROMAN_HOLIDAY` rip
  landed beside two earlier failed attempts. `-2` suffix, not replacement.
- **A file already in the library is never replaced.** It may be one you
  renamed. hellbox must not lose work it did not do.
- **Filing reconciles.** Anything encoded while filing was off, or before filing
  existed, is picked up on the next pass rather than needing a re-encode.

### 2.11 Failures need grouping, not just reporting

`internal/disc/failure.go`. "Three discs failed" says nothing about what to do.
"Three discs were not in the AACS key database" says update KEYDB.cfg, and
"three discs use BD+" says something else entirely. Ten groups, each with
advice, the raw message always retained beside the group. Carries forward
unchanged; the Rails UI gets to render it properly.

### 2.12 What v1 got wrong or left undone

Honest list, because these define the v2 backlog:

| | |
|---|---|
| **No identification at all** | Episodes file as `Still Game - c887d54d5276 - t00.mkv`. This is the headline gap. |
| **Blu-ray yields one title** | Only the main playlist is reachable without the playlist list. A box set of episodes produces one file. Fixed in v2 (§4.3). |
| **Blu-ray keeps no raw** | 18 GB each. A deliberate trade; kept, with mitigation (§4.3). |
| **Blu-ray has no chapters** | ffmpeg's `bluray:` is a protocol, not a demuxer. Partially fixed by playlist enumeration. |
| **`retry` is stubbed** | `internal/daemon/server.go:247`. Eject-and-reinsert is the workaround. |
| **Health check races the scan** | `runHealthChecks` shells `makemkvcon -r info disc:9999`, enumerating every drive, concurrently with a scan on the same device. Two processes contending for one drive. Disappears with MakeMKV. |
| **Invariant 3 is too strong** | "The daemon never blocks on a human" is right for ripping and wrong for filing. v2 splits it. |
| **The TUI is the ceiling** | Bubble Tea is a fine status board and a bad place to reorder eleven episodes and search TMDB. |

---

## 3. Shape

Three processes on one box. Nothing is exposed off the machine and nothing
authenticates anything — this is a local appliance and that is a deliberate
non-goal.

```
                    ┌──────────────────────────────────────┐
   BD-RE      ──────│  hellboxd  (Go, systemd, host)       │
   (the only  ─────▶│  ─────────────────────────────────── │
    drive)          │  SCSI presence, drive workers        │
              │     │  libdvdread/libdvdcss  · libbluray   │
              │     │  ffmpeg encode (VAAPI)               │
              │     │  verification                        │
              │     │  SQLite: in-flight state + rip facts │
              │     └───────┬──────────────────────▲───────┘
                            │  SSE (Last-Event-ID) │  HTTP
                            ▼                      │
                    ┌──────────────────────────────┴───────┐
                    │  slay  (Rails 8, Puma, localhost)    │
                    │  ─────────────────────────────────── │
                    │  Postgres: the catalog               │
                    │  identification nets · TMDB/TVDB     │
                    │  review queue · Hotwire UI           │
                    │  filing: hardlinks + NFO             │
                    │  Solid Queue: lookups, OCR, filing   │
                    └──────────────────────┬───────────────┘
                                           │ hardlink
                    ┌──────────────────────▼───────────────┐
                    │  Jellyfin (Docker, unchanged)        │
                    │  reads library/ read-only            │
                    └──────────────────────────────────────┘
```

### Why the daemon stays in Go

It needs `SG_IO` ioctls, `/dev/sr*`, `/dev/dri`, long-lived per-drive
goroutines, and a process lifetime measured in weeks. It must survive Rails
being restarted mid-rip, and it must run under systemd on the host rather than
in a container — containerising it reintroduces exactly the device-passthrough
and group-ID problems that motivated leaving ARM.

`internal/drive`, `internal/decrypt`, `internal/transcode` and `internal/store`
carry forward largely as-is. This is not a rewrite.

### Why the frontend is Rails

The remaining work is a CRUD-and-queues problem wearing a media costume: search
TMDB, correct a season number, reorder eleven episodes, approve a batch, retry a
failed job, watch it happen live. Rails 8 ships Solid Queue, Turbo Streams and
Active Record; every one of those is a thing that would otherwise be hand-rolled
in Go and be worse.

### Why hellboxd stays useful with Rails down

The daemon rips, verifies and encodes autonomously. If Rails is stopped, discs
still go in and verified rips still land on disk; they simply do not get filed.
The daemon's SSE ring buffer holds recent events, and Rails catches up on
reconnect via `Last-Event-ID`. Nothing is lost, because the rips tree is
self-describing (invariant 2) and Rails can reconcile from the filesystem alone.

### The invariants, revised

1. **A disc is never read twice.** Raw rips are permanent and immutable; every
   later stage reads from them and writes elsewhere. *(Unchanged. Blu-ray
   remains the documented exception — §4.3.)*
2. **The rips tree is self-describing.** Both databases are indexes. Delete
   Postgres and every judgment is lost but recoverable; delete the rips tree and
   you are back at the shelf.
3. **Ripping never blocks on a human. Filing may.** *(Revised.)* The drive-side
   pipeline runs to completion unattended, always. The catalog is allowed to
   hold a disc in a review queue indefinitely without that costing anything —
   the bytes are already safe on disk and the drive is already free.
4. **An open tray means the rip succeeded, and only that.** *(Narrowed.)* It no
   longer implies the disc was identified or filed.
5. **There is one drive, and it is the BD-RE.** *(Decided 2026-08-09; the ASUS
   was physically removed the same day.)* Every disc of every type goes to
   `HL-DT-ST BD-RE BT10N`, stable id
   `usb-HL-DT-ST_BD-RE_BT10N_DD564198838A4-0:0`. It reads raw sectors at
   5.7 MB/s, against the ~1.3 MB/s the ASUS managed on DVD (B.5).

   **There is no spare.** Nothing in the design may assume a second drive
   exists, a disc can be moved to another drive, or a drive fault can be
   isolated by comparison. Every one of those was available on 2026-08-08 and
   is not now.

   **And one drive is the target, not a temporary shortage.** *(Added
   2026-08-19.)* The speed figures above explain which drive was kept; they do
   not explain why a second is not simply plugged back in. The reason is the
   bus: the drives are USB and share one, and two of them reading at once does
   not halve each one's throughput, it collapses both. So `discSlot`
   serialising disc work across every drive stays whatever the drive count, and
   anyone re-adding a drive is buying a spare to swap to, not a second worker
   to run beside this one. See §10.5.

   > **The one untested combination is now the only combination.** Phase A
   > proved native CSS decryption on the ASUS, which had *no region set*. This
   > drive is **locked to region 1** with 4 changes left. A region 1 disc is
   > unaffected and is the overwhelming majority of the collection. A **region 2
   > disc** is unverified here, and there is no longer a second drive to fall
   > back to. libdvdcss should behave identically — a drive refusing CSS
   > authentication is the same situation whether its region is unset or simply
   > wrong — but this is now a single point of failure, so confirm it on the
   > first region 2 disc rather than assuming.

   Address the drive by stable id, never by `/dev/srN`. It currently enumerates
   as `/dev/sr1` despite being the only optical device, and there is nothing to
   stop the kernel calling it `/dev/sr0` after a reboot.

6. **No further region change is ever spent.** *(New.)* The remaining drive is
   locked to region 1 with 4 user changes left (Appendix A.7), and that is what
   it reads forever. The counter is surfaced by the health check, because a
   budget you cannot see is one you spend by accident. The drive that still had
   an unspent 5 of 5 is gone, so there is no reserve.

---

## 4. hellboxd

### 4.1 Unchanged from v1

Keep, essentially untouched:

- `internal/drive/` — SCSI presence, region/protection reads, stable-id
  resolution, per-drive workers, the `INCOMPATIBLE` state. The most validated
  code in the repo.
- `internal/decrypt/` — dvdbackup + libdvdcss, VOB-size verification, the
  `.complete` marker, `DVDCSS_CACHE` pointed at `work_dir`, carriage-return
  progress scanning.
- `internal/transcode/` — VAAPI-first encoding, per-file interlace detection,
  quality+ceiling, stream selection with channel-count-first audio.
- `internal/disc/failure.go` — the ten failure groups and their advice.
- The one-disc-at-a-time `discSlot` across all drives, and encoding in the
  daemon rather than the drive worker so the tray opens while the GPU is busy.

### 4.2 The DVD path, natively

This is the largest change in v2 and it removes MakeMKV from the default path.

**Enumerate** with libdvdread via the IFO structures — unencrypted, so this
works on any disc in any drive regardless of region. Per title: PGC count,
chapter (PTT) count, cell durations, audio and subpicture stream tables, aspect
ratio, frame rate. `lsdvd` (in Ubuntu, `0.21-2`) is the quickest way to get
this as parseable output; a small cgo binding to libdvdread is the tidier one.
**Decision: start with `lsdvd`, replace it if its output proves lossy.** The
whole point of `disc.json` is that the raw output survives, so this is cheap to
revisit.

**Decrypt**, when the drive cannot: unchanged from v1. dvdbackup mirrors to
`work_dir`, verified against the 1 GB VOB limit, `.complete` written last.

**Extract** with the `dvdvideo` demuxer, one invocation per title:

```
ffmpeg -f dvdvideo -preindex 1 -region 0 -title N -i <VIDEO_TS parent>  \
       -map 0 -c copy -avoid_negative_ts make_zero  title_NN.mkv
```

`-preindex 1` is a two-pass read that buys accurate chapter markers — the thing
v1's Blu-ray path had to go without. `-region 0` requests region-free playback.
`-c copy` because a raw rip is a remux, never a re-encode.

Three things this changes for the better, beyond dropping the key:

- **`min_title_seconds` stops being load-bearing.** v1's most dangerous footgun
  was that MakeMKV numbers titles within its *filtered* list, so a disc scanned
  under one value and ripped under another rips the wrong titles. DVD title
  numbers are a property of the disc. Filtering becomes an ordinary predicate
  with no coupling.
- **Menus become extractable in-process** — `-menu -menu_vts N` — which is what
  feeds the OCR net (§5.3) without a separate rendering step.
- **The health check stops fighting the scan.** No `makemkvcon -r info
  disc:9999` enumerating every drive while one of them is being read.

**MakeMKV survives as an opt-in rescue path.** Config `makemkv_fallback = true`
(default `true`, since it is already installed here). It is attempted when the
native path produces no titles, or produces titles that fail verification, and
the reason is logged as a distinct failure group so it is visible how often it
is actually needed. Everything in `internal/makemkv/` — the robot-mode parser,
the verified attribute codes, the key refresh, the stall detector — stays,
demoted rather than deleted.

> **Verified 2026-08-08 against a real CSS disc.** See [Appendix A](#appendix-a--phase-a-results).
> libdvdcss decrypts transparently under libdvdread on an RPC-2 drive with **no
> region set**, reading a region 1/3/4 disc, with no region change spent and no
> dvdbackup copy. Chapters, full stream tables and menus all come through. The
> native path is the default.

### 4.3 The Blu-ray path

**Enumerate the playlists — always, and before attempting any decryption.**
This fixes v1's worst content bug, a box set of episodes yielding exactly one
file: libbluray nominates a single "main title", and on Firefly disc 1 that is
the 87-minute pilot, silently dropping three episodes (Appendix B.6).

Use `bd_list_titles` and `bd_info` from `libbluray-bin` (`1:1.4.1-1` in Ubuntu).
No mount and no root is needed — libbluray opens the device directly.

> **Enumeration is independent of decryption, and the design must exploit that.**
> Playlists and `BDMV/META` are not encrypted; only the `.m2ts` payload is. A
> BD+ disc that cannot be played can still be *completely catalogued* — name,
> cover art, episode count, runtimes, chapter counts, stream layouts. So a disc
> hellbox cannot rip should still reach the UI as **"Firefly: Disc 1 — 4
> episodes, BD+, needs MakeMKV"**, never as an opaque failure. Enumerate first,
> decrypt second, and record the enumeration either way.

**Filter to real titles by stream layout, not duration alone.** Measured on
Firefly disc 1: every real episode carries 4–5 audio tracks and 6 subtitle
tracks; the 28-minute extra carries 1 audio and no subtitles; the 24 junk
playlists carry no audio at all. Duration alone cannot separate the extra from
an episode. The predicate is *duration ≥ `min_title_seconds` **and** audio ≥ 1*,
with subtitle and chapter counts as strong secondary evidence. Then dedupe by
duration and stream layout to collapse the near-identical playlists discs use
for seamless branching and anti-rip decoys.

**Read the disc's own metadata.** `BDMV/META/DL/bdmt_eng.xml` carries a
human-written disc title on many Blu-rays. This is strictly better evidence than
any DVD volume label and v1 never looked at it. It becomes an identification net
(§5.3) with meaningfully higher confidence than `LabelNet`, because it is prose
someone typed rather than 11 upper-case characters.

**Encode straight from the disc**, per playlist:

```
ffmpeg -playlist P -i bluray:/dev/srN  ...
```

libaacs decrypts in place as ffmpeg reads, and AACS has no region enforcement so
none of the DVD region machinery applies.

> **Corrected 2026-08-08 — this works only for AACS-only discs.** A disc that
> also carries **BD+** cannot be opened by the free stack at all: `libbdplus` is
> installed but the BD+ virtual-machine data it needs is shipped by nobody, so
> `bdplus_init()` fails and libbluray refuses the disc. **BD+ discs require
> MakeMKV**, which keeps the registration key load-bearing for that slice of the
> collection. Detect BD+ from `BDSVM/00000.svm` in the disc structure — visible
> before any decryption is attempted — and route those discs to MakeMKV
> immediately. Full evidence and consequences in [Appendix B](#appendix-b--blu-ray-results-and-a-wrong-assumption).

**Keep the no-raw trade, with mitigation.** A Blu-ray raw is ~18 GB and a few
dozen would fill the machine; a DVD title is ~1 GB and keeping it is what makes
a re-encode cost minutes. The mitigation is that identification changes *names*,
not bytes, and names are hardlinks — so a disc re-identified six months later
costs a `rename(2)`, not a trip to the shelf. The residual risk is picking the
wrong playlist, which playlist enumeration substantially reduces.

`KEYDB.cfg` is present at `~/.config/aacs/KEYDB.cfg`, 64 MB, installed
2026-07-30. Its age becomes a health check: a stale KEYDB is the direct cause of
the `aacs-key` failure group, and the UI should say how old it is before you
work out that that was the problem.

### 4.4 Verification

v1's verification was deliberately shallow because ffmpeg was not yet a
dependency. It is now, from the first stage, so verification gets what
`phase1-spec.md` §8 deferred:

| Check | Catches |
|---|---|
| Output file exists and exceeds `min_output_bytes` | An empty write |
| Valid EBML/Matroska magic | A truncated header |
| `ffprobe` duration within tolerance of the enumerated duration | **The truncated rip** — the check that would have caught the Star Trek loss |
| Stream count and kinds match what was enumerated | A dropped audio track |
| VOB sizes within the DVD-Video spec, on decrypted copies | The 5 GB VOB |
| Sum of title runtimes against the disc's total | A title that came back short |

Duration tolerance needs a real number and does not have one yet. Start at ±2%
absolute with a floor of 5 seconds, log every near-miss, and tighten once fifty
discs have been through. **Assumed, not measured.**

### 4.5 What the daemon decides

It decides everything about bytes and nothing about meaning. Concretely: it
detects, enumerates, deduplicates by fingerprint, decrypts if needed, extracts
every title over `min_title_seconds`, verifies, ejects, encodes, and reports.
It does not identify, name, or file.

**Deduplication stays in Go**, because "have I already got these bytes" is a
fact about the filesystem, not a judgment. The daemon keeps its own
fingerprint → rip-dir table in SQLite and answers the already-ripped question in
milliseconds without Rails being up. Rails mirrors it for display. Same
fingerprint as v1 — `SHA-256(label ‖ title_count ‖ Σ sorted("secs:bytes"))` —
which is worth keeping unchanged so v1's existing rips are recognised.

---

## 5. slay

Rails 8, Postgres, Solid Queue, Hotwire. Bound to localhost. No authentication,
no CSRF fuss beyond the defaults, no multi-tenancy — one person, one machine.

### 5.1 The catalog

The schema exists to answer one question v1 could not: *what else do I have from
this set?* Six Still Game discs all labelled `STILL_GAME` are unidentifiable
individually and trivially identifiable as a group.

```
Series ──< Season ──< Episode          (from TMDB/TVDB — the truth)
                          ▲
                          │ episode_id
Disc ──< DiscTitle ───────┘
  │           │
  │           └──< Placement ──▶ library path + NFO
  └──< Candidate  (one per net, with confidence and reasoning)

Movie ◀── Disc                          (the simple case)
```

| Table | Holds |
|---|---|
| `discs` | fingerprint, label, type, rip_dir, ripped_at, drive, `status` (rough/needs_review/confirmed/filed) |
| `disc_titles` | index, duration, chapters, streams, rip path, encoded path, thumbnail |
| `candidates` | net name, proposed title/year/kind/season/disc, confidence, `why` |
| `series` / `seasons` / `episodes` | provider IDs, names, air dates, runtimes, order |
| `movies` | provider ID, title, year, runtime |
| `placements` | disc_title → final library path, NFO content, linked_at |
| `disc_sets` | the grouping that makes six identical labels tractable |
| `batches` | a declared box set: series, season range, expected disc count, open/closed (§5.5.1) |
| `episode_claims` | which disc title claimed which episode out of a batch's pool, releasable |
| `provider_cache` | every TMDB/TVDB response, keyed and durable |

`provider_cache` is not an optimisation. It means identification logic can be
rewritten and re-run against the whole collection offline, with no network and
no rate limit — the same reason v1 keeps `makemkv-info.txt` verbatim, applied
one layer up.

**Every judgment is revisable and every judgment records its reasoning.** A
wrong name in the library must be traceable to the evidence that produced it;
v1's `Candidate.Why` field had this right and it becomes a first-class column.

### 5.2 The rough / review / confirmed pipeline

The names are letterpress, because the metaphor holds: a **rough** is what came
off the press unproofed, a **galley** is a proof pulled for correction, and only
a corrected forme gets printed.

```
disc ripped & verified
        │
        ▼
  identification nets run (§5.3)
        │
        ├─ a film, named with confidence, and a provider id ──▶ CONFIRMED ──▶ filed
        │
        ├─ nets disagree, or confidence low ──────────────────▶ NEEDS REVIEW
        │                                                            │
        ├─ a film with no provider id ────────────────────────▶ NEEDS REVIEW
        │                                                            │
        ├─ any series disc ───────────────────────────────────▶ NEEDS REVIEW
        │                                                            │
        └─ no net had anything ───────────────────────────────▶ NEEDS REVIEW
                                                                     │
                                                           person corrects it
                                                                     ▼
                                                                CONFIRMED ──▶ filed
```

**Amended 2026-08-19, and now implemented in `Identify::Decision`.** The draft
above this line put "no net had anything" straight into CONFIRMED, which
contradicted its own table one paragraph later and was never what the code did.
The substantive change is the second row:

| Condition | Action |
|---|---|
| A film, confidence ≥ 0.7, **and a provider id** | File |
| A film with no provider id, at any confidence | Review |
| A proposal with no `kind` | Review |
| Any series disc | Review — until episode alignment exists |
| Any series disc where every title maps to a distinct episode with runtime error < 2% | File — *not built* |
| `Contested` (v1's ≤0.2 confidence gap between leaders) | Review |
| Nothing proposed | Review |

**Why a provider id rather than a higher confidence.** Confidence answers "how
sure are the nets about this name". Whether the name may be written into the
library is a different question, and bundling the two into one number is what
made the original 0.85 look arbitrary.

`DiscNameNet` reading *The Karate Kid* off the DVD text data manager scores
0.80 on its own, and that is a correct and well-evidenced answer to the first
question. Acting on it means filing `Movies/The Karate Kid/` with no year and
an NFO with no `<tmdbid>` — at which point Jellyfin re-matches the file by its
name, which is exactly what §5.4 exists to prevent and exactly the library
Phase F has to go back and clean up. So it is a missing field, and it is
checked as one.

Nothing is discarded when a disc is held back: it keeps the name the nets
found, and the review screen already shows the disc's own name beside the
volume label with somewhere to supply the id. One field, once.

**The accepted cost.** With no `TMDB_API_KEY` the provider net abstains, so no
film auto-files at all and the review queue holds everything. That is a real
throughput cost in the unconfigured case and it was taken deliberately with the
user on 2026-08-19: a personal key is free, and the alternative is silently
rebuilding the unnamed library that v2 exists to replace.

`CONFIRM_AT` stays at 0.7. In practice a film that has a provider id has been
corroborated by the provider net and sits at 0.85–0.95 anyway, so raising the
threshold as well would change almost nothing while adding a second knob for
one job.

Every auto-filed disc still appears in a "recently filed" list with one-click
undo, because the threshold will be wrong at first and finding out should be
cheap. Undo is a rename plus an NFO rewrite — no re-encode, no re-read.

### 5.3 Identification

v1's net architecture carries over wholesale and gains four nets. Each returns
zero or more candidates with a confidence and a reason; `Resolve` merges by
normalised title, raises confidence for independent agreement (+0.15 per extra
net, capped at 0.95), and flags `Contested`. That logic is good and needs no
change — it just gets better inputs.

| Net | Evidence | Strength |
|---|---|---|
| `LabelNet` | volume label, season/disc suffixes, format markers | Weak by design. Carried over unchanged. |
| `DiscNameNet` **new, built** | the name the disc gives itself: a DVD's Text Data Manager (`VIDEO_TS.IFO`, pointer at `0xD4`) or a Blu-ray's `BDMV/META/DL/bdmt_eng.xml` | **Strongest pre-rip evidence there is.** Prose a person typed, readable with no key, even on a disc that cannot be decrypted. |
| `ShapeNet` | v1's `Classify`: lopsided vs level | Kind only, never a title. |
| `MenuNet` **new** | tesseract over `-f dvdvideo -menu -pgc N` frames, paired to titles by printed runtime | Strong, and the only source of **episode names**. Must composite the subpicture layer — see Appendix A.5. |
| `CreditsNet` **new** | tesseract over credit frames sampled during the rip: studio, director, cast | Strong, and works where the label and menus give nothing. Identified a `DVD_VIDEO`-labelled disc on the first try — Appendix A.6. |
| `ProviderNet` **new** | TMDB / TVDB search on whatever the other nets proposed | Turns a guess into an ID, a year and a runtime list. |
| `SetNet` **new** | sibling discs already in the catalog, and any **open batch's unclaimed episode pool** (§5.5.1) | The one that solves Still Game. Strongest of all when a batch is declared, because it turns inference into consumption. |
| ~~`IfoNet`~~ | *merged into `DiscNameNet`.* The design split the DVD and Blu-ray cases into two nets; they are one kind of evidence and splitting them would have meant two copies of the same reasoning, free to drift. The guess that the text lived in the VMG provider ID was also wrong — see below. |

> **Built and verified 2026-08-09.** `DiscNameNet` reads
> `"The Karate Kid (Special Edition)"` off a disc whose volume label is
> `DVD_VIDEO`, in five seconds, before any byte is ripped, and resolves it to
> **"The Karate Kid"** at confidence 0.80 while `LabelNet` correctly abstains.
>
> The design guessed the DVD text lived in the 32-byte provider identifier at
> `0x40`, and the title being exactly 32 characters made that persuasive. **That
> field is all zeros on this disc.** The name is in the Text Data Manager
> reached by the pointer at `0xD4`, stored as a display title *and* a sort
> title:
>
> ```
> =The Karate Kid (Special Edition)\tKarate Kid, The (Special Edition)\t
> ```
>
> The sort title is a bonus worth having: it is precisely what tells
> *The Karate Kid* from *Karate Kid, The* in a provider search.
>
> A trailing parenthetical is dropped only when it is a year or a recognised
> edition. `(Special Edition)` goes because it describes the release rather than
> the work and costs a match; an unrecognised parenthetical stays, because
> removing it would break more than it fixed.

#### The episode ordering problem

This is the thing the user actually wants and the thing v1 refused to attempt.
Given a disc of N titles and a candidate season of M episodes, decide which
title is which episode. Four signals, strongest first:

1. **Menu names against episode names.** The disc menu prints "Doacters
   (28:12)"; `MenuNet` pairs that name to the 28:12 title on this disc; TMDB
   says S03E05 is "Doacters". Two independent sources agreeing on a *name*, with
   a runtime as the pairing key. This is close to certainty and it is available
   on a large fraction of TV DVDs, because episode-selection menus are the norm.

2. **Sequence alignment on runtime.** Disc titles are almost always in broadcast
   order. Align `[r1..rN]` against every contiguous window of the season's
   episode runtimes and take the window minimising total deviation. Report the
   margin between the best and second-best window as the confidence — a disc of
   four 24-minute episodes from a season of uniformly 24-minute episodes has no
   margin at all and must go to review, and saying so is the correct answer.

3. **Sibling discs.** If discs 1 and 2 of this season are already in the catalog
   holding four episodes each, disc 3 starts at episode 9. `SetNet` is what makes
   the sixth Still Game disc easy after the first five were done by hand — and
   it is the strongest single argument for Rails owning the catalog, because it
   requires reasoning across discs that the daemon never sees together.

4. **Disc number from the label.** `STNGD3` says disc 3. Weak on its own,
   valuable as a prior over the alignment windows in (2).

Where the signals disagree, the disc goes to review with all of them displayed.
It does not get a silent best guess.

> **Deliberately not attempted:** matching episode runtimes against a database
> *without* a per-disc pairing key. Every forty-five minute drama has the same
> runtime as every other; the reason (1) works is that both halves come from
> *this disc*. v1 states this exact reasoning in `menutext.go` and it remains
> the line between a sound inference and a coin toss recorded as fact.

#### PAL speedup — the one place region 2 matters

A region 2 PAL disc runs film at 25 fps instead of 24, making everything on it
**~4.17% shorter** than the runtime any provider lists. A 24:00 episode arrives
as 23:00. Runtime matching that ignores this will systematically mis-align every
region 2 disc, and it will do so *plausibly* — off by one episode, not off by
obvious nonsense.

Detect it from the stream rather than the region code: 25 fps or 576-line video
means PAL, so scale expected runtimes by 25/24 before matching. Flag it on the
disc record. **Assumed** — no region 2 disc has been through this system yet,
and this is the top item to validate once one has.

### 5.4 Filing

Rails writes the library, exactly as chosen:

```
Movies/Roman Holiday (1953)/
  Roman Holiday (1953).mkv
  Roman Holiday (1953).nfo
  extras/
    Roman Holiday (1953) - Restoring a Classic-featurette.mkv

TV/Still Game/
  tvshow.nfo
  Season 03/
    Still Game - S03E05 - Doacters.mkv
    Still Game - S03E05 - Doacters.nfo
```

- **Hardlinks**, so the library and the encoded tree are the same bytes. No
  space, no copy time, and a rename keeps pointing at the same data.
- **NFO sidecars** carrying `<tmdbid>` / `<tvdbid>`, air date, plot and episode
  title. Jellyfin reads what it is told rather than re-guessing, which is the
  entire point of choosing to be authoritative.
- **Never replace an existing file.** v1's rule, unchanged. A hardlink onto an
  occupied path is a conflict to surface, not a write to force.
- **Extras get Jellyfin's suffixes** — `-featurette`, `-deleted`, `-behindthescenes`
  — so they are typed rather than dumped in one `extras/` bucket. `MenuNet`
  names are what make this possible; where there is no name, fall back to v1's
  `extras/Title - t01.mkv`.
- **Reconcile on a schedule.** A recurring Solid Queue job walks `encoded/`
  against `placements` and files anything missing, which covers a disc filed
  while Rails was down and anything encoded before filing existed.
- **Tell Jellyfin.** v1's README lists "nothing tells Jellyfin to rescan" as
  outstanding. It is one authenticated POST to the local Jellyfin API after a
  filing batch completes. Do it.

### 5.5 The UI

Hotwire throughout; no SPA. **Decided with the user, 2026-08-08: no push
notifications of any kind, and no email.** The dashboard is the only signal
there is. Desktop is the working surface; a phone is for glancing.

Those two decisions together impose a hard constraint: **if the dashboard is the
only notification, it has to be worth opening, correct at a glance, and legible
on a phone.** Everything below follows from that.

#### The dashboard answers one question

Not "here are six tabs". At the read speeds Appendix A.4 measured — ~50 minutes
for a feature, a couple of hours for a full disc — nobody watches this work.
They check in. So the default view answers **"does anything need me?"** and
subordinates everything else:

```
┌─ NEEDS YOU ─────────────────────────────────── 2 ─┐
│  Still Game — disc 3 of 6      which episodes?    │
│  Sunshine (Blu-ray)            BD+ · MakeMKV path │
└───────────────────────────────────────────────────┘
┌─ WORKING ─────────────────────────────────────────┐
│  BD-RE The Karate Kid         title 4/12    38%   │
│                               ~46 min left        │
│  GPU   encoding · title 2     1.8×                │
└───────────────────────────────────────────────────┘
┌─ DONE TODAY ──────────────────────────── undo ────┐
│  Roman Holiday (1953)           auto-filed 14:02  │
└───────────────────────────────────────────────────┘
```

Three states: **needs you**, **working**, **done**. An empty first box means
close the tab. Library, failures, health and history are each one click away and
never compete for attention.

Two cheap things that are *not* notifications and should still be done, because
they cost nothing and make an open tab passively useful: the **live count in the
`<title>`**, so a background tab shows `(2) slay`, and a **favicon that changes
on state**. Both are passive — they never interrupt — which is what was asked
for.

**Progress must be a real commitment.** v1 put per-title progress and ETA in the
client and deliberately kept them out of the log; that rule holds. But at 1×
read speed a bare spinner for two hours is useless, so the working box carries
title *n* of *m*, percent within the title, and a wall-clock estimate derived
from measured throughput rather than from a guess.

#### Screens

| Screen | Job |
|---|---|
| **Dashboard** | The three boxes above. The default route. Phone-legible. |
| **Review queue** | The only place real work happens. See below. |
| **Batches** | Declare and track a box set (§5.5.1). |
| **Library** | What has been filed, searchable, each entry linked back to the rip it came from, one-click undo. |
| **Failures** | v1's ten groups with their advice and raw messages, counted. Retry from here — including the retry v1 left stubbed. |
| **Health** | Drive states **and both region counters**, free space per tree, KEYDB.cfg age, VAAPI availability, provider reachability, and **how many discs needed the MakeMKV path** (§5.5.2). |

#### The review queue

The screen that justifies the rewrite. Its job is to make a decision take
**seconds, not minutes**: a thumbnail per title (one `ffmpeg` frame ~10% in,
proven cheap in Phase A), runtimes, what each net proposed **and its `Why`**, a
provider search box, and drag-to-reorder episode mapping.

**"Apply to the rest of this set"** is the highest-value control on the page.

Per the device decision: reordering is drag-and-drop on desktop, with a plain
numeric-entry fallback on small screens rather than touch drag, which is fiddly
enough to be worse than typing. The queue is reachable on a phone but is not
optimised for it.

### 5.5.1 Batches — declaring a set before feeding it

**Chosen by the user over identify-after-the-fact.** Before a box set goes in,
you say what is coming:

```
Start a batch
  Series:  Still Game          [search TVDB]
  Covers:  Series 1 – 3
  Discs:   6 expected
```

The app pulls the episode list up front and holds it as a pool. Every disc
ripped while the batch is open is matched against the **remaining unclaimed
episodes**, and confirming a disc claims its episodes out of that pool.

This is the single largest simplification available to the hardest problem in
the project. It converts §5.3's episode ordering from *inference* into
*consumption*: instead of asking "what season is this and where does it start",
the question becomes "which four of these fourteen unclaimed episodes are these
four titles", which sequence alignment answers with a large margin. Combined
with the disc number from the label, most discs in a declared batch should
auto-file without review.

Four rules keep it from becoming a trap:

- **A batch is a prior, not a constraint.** A disc that does not fit — a film
  dropped in mid-batch, a bonus disc with no episodes — must say so and fall
  back to ordinary identification. It must never be crammed into the pool
  because a batch happened to be open.
- **Order does not matter.** Discs may be fed in any order; matching is against
  the unclaimed pool, not against a cursor.
- **The pool is visible and editable.** The batch screen shows which episodes
  are claimed, by which disc, and lets a claim be released. A mis-declared
  batch must be recoverable without unpicking the library by hand.
- **Batches expire deliberately, not silently.** A batch stays open until closed
  or until its expected disc count is met and confirmed. It is never
  auto-closed on a timer, because a box set can take days to work through.

Blind ingest remains fully supported — a batch is an accelerator for box sets,
never a prerequisite for putting a disc in a drive.

### 5.5.2 Surfacing the DRM path

Appendix B establishes that BD+ discs need MakeMKV. The user should not have
to know which of their discs are Fox or Sony pressings, and asking them to was
the wrong question. **The collection reports on itself instead.**

Every disc records which path read it — `native-dvd`, `native-bluray-aacs`,
`makemkv-bdplus`, `decrypt-copy` — and the health screen aggregates:

```
Blu-ray   14 read natively (AACS only)
           6 required MakeMKV (BD+)
MakeMKV key   valid, expires in ~19 days
```

That turns an unanswerable question into an empirical one after a dozen discs,
and — more importantly — makes the remaining exposure to MakeMKV key expiry a
number on a screen rather than a surprise outage.

### 5.6 Jobs

Solid Queue, distinct queues so a slow one cannot starve a fast one:

| Queue | Work |
|---|---|
| `ingest` | Absorb a daemon event, create/update disc + titles |
| `identify` | Run the nets; cheap and mostly local |
| `providers` | TMDB/TVDB calls; rate-limited, retried, cached |
| `ocr` | tesseract over menu frames; slow, CPU-bound |
| `file` | Hardlink + NFO write + Jellyfin notify |
| `reconcile` | Recurring sweep of `encoded/` vs `placements` |

---

## 6. The seam

hellboxd exposes plain JSON over HTTP on `127.0.0.1:9494`, plus one SSE stream.
The unix socket goes away — it existed to serve a local TUI, and HTTP serves
both Rails and any future client with less ceremony. Version header from day
one, as v1 had.

| | |
|---|---|
| `GET /v1/drives` | Every drive: state, disc, progress |
| `GET /v1/health` | Health check results |
| `GET /v1/discs/:fingerprint` | Enumeration, titles, streams, verification |
| `GET /v1/events` | **SSE.** All state changes and log lines, monotonic ids |
| `POST /v1/drives/:id/eject` · `/cancel` · `/rescan` | |
| `POST /v1/discs/:fingerprint/forget` | Drop the dedupe record |
| `POST /v1/discs/:fingerprint/thumbnails` | Grab a frame per title |
| `POST /v1/discs/:fingerprint/menus` | Extract + return menu frames for OCR |
| `POST /v1/encode` | Re-encode a title under a named profile |

**Event delivery.** Rails runs one long-lived bridge process holding the SSE
connection, writing durable facts to Postgres and broadcasting Turbo Streams.
hellboxd keeps a bounded ring buffer of recent events with monotonic ids; the
bridge reconnects with `Last-Event-ID` and catches up. That gives
at-least-once delivery across a Rails restart without inbound webhooks, retry
queues, or the browser needing to know two servers exist.

If the ring buffer is outrun — Rails down for longer than it holds — the bridge
falls back to a full reconcile against `GET /v1/discs` and the filesystem, which
is always correct because of invariant 2.

---

## 7. On disk

```
/srv/media/
  rips/       raw remuxes, immutable, one dir per disc     Go writes, nothing else
  work/       decrypt scratch, .dvdcss-cache               Go writes, transient
  encoded/    one encode per title, mirrors rips/ layout   Go writes
  library/    hardlinks + NFO, Jellyfin's naming           Rails writes
```

`rips/` keeps v1's layout exactly — `YYYY-MM-DD--slug--fingerprint12/` holding
`disc.json`, the verbatim enumerator output, `rip.log`, and `title_NN.mkv`. Two
properties are load-bearing and unchanged: **the description is written before a
single byte is ripped** (so an interrupted rip still says what the disc held),
and **titles are staged in a hidden `.rip-*` directory and moved only after they
verify** (so a crash cannot leave something that looks finished).

`disc.json` gains the fields v2 produces: playlist ids for Blu-ray, PGC and PTT
structure for DVD, detected frame rate and PAL flag, the raw enumerator output
whatever produced it.

---

## 8. Build order

Each phase leaves the system working. No phase requires the next one to exist.

**Phase A — prove the native path.** ✅ **Done, 2026-08-08.** Native CSS
decryption, enumeration, chapters and menu extraction all confirmed against a
real region 1/3/4 disc on the RPC-2 drive with no region set. Full results and
the corrections they forced are in [Appendix A](#appendix-a--phase-a-results).
Still outstanding from this phase, none of it blocking: a region 2 disc, a
Blu-ray, and the read-speed investigation in A.4.

> **Phase B status, 2026-08-09.** Largely built. `internal/bluray` (enumeration
> without decryption), `internal/dvd` (native enumeration, extraction, PAL
> correction), `internal/source` (path routing and blocked-disc handling),
> `internal/verify` (truncation detection), `internal/api` (HTTP + resumable
> SSE), and `internal/store` read-path recording are all committed with tests,
> and the daemon serves the API on `127.0.0.1:9494` beside the socket —
> verified against the running daemon, not only in tests.
>
> **Not yet done:** the drive worker still takes v1's MakeMKV-first path. The
> new packages exist and are tested but the worker does not call them, so
> nothing in the pipeline is native yet. That swap is the remaining Phase B
> work and it is the one that touches code known to work, so it wants doing
> deliberately rather than at the end of a long session.

**Phase B — hellboxd v2.** Native enumerate/extract, MakeMKV demoted to
fallback, Blu-ray playlist enumeration, `bdmt_eng.xml` reading, ffprobe
verification, HTTP+SSE replacing the socket, thumbnail and menu endpoints.
Delete nothing. At the end of this phase the daemon does everything v1 did,
without a key, and Blu-ray box sets produce one file per episode.

**Phase C — slay skeleton.** Rails app, Postgres schema, SSE bridge, drives
screen, health screen, failures screen. Ingest only; no identification. At the
end of this phase v1's TUI can be retired.

**Phase D — identification.** Port `identify` to Ruby or keep it in Go behind an
endpoint (*decide in C, when the data shapes are concrete*). TMDB/TVDB clients
with the durable cache. `MenuNet` with tesseract. `SetNet`. The episode
alignment algorithm with PAL compensation. Confidence thresholds wired to the
auto-file rules.

**Phase E — review queue and filing.** The screen that justifies the rewrite:
thumbnails, candidates with reasoning, provider search, drag-to-order,
apply-to-set. Filing with NFO. Jellyfin notify. Undo.

> **Phase E status, 2026-08-09.** The review queue is built — candidates with
> their reasoning, the disc's own name beside the label, apply-to-set, and a
> person's decision stored apart from what the nets proposed. Filing is built:
> hardlinks under Jellyfin's layout, NFO sidecars carrying the provider id,
> never-clobber, the Jellyfin rescan call, undo, and `bin/file-pending` as the
> reconcile sweep. `file_to_library` in hellboxd is now off by default so the
> library has one writer.
>
> **Not yet done in this phase:** thumbnails, provider search from the review
> screen, and drag-to-order. None of them block a disc reaching the library —
> they make the review itself faster, and are worth doing against a real
> backlog rather than in advance of one.

**Phase F — reconcile the back catalog.** Point identification at every rip
already on disk and let it work through the shelf with no disc in the drive.
This is what invariant 2 and the verbatim-output rule were saved up for, and it
is the moment the whole design pays out.

---

## 9. Risks

| Risk | Mitigation |
|---|---|
| ~~`dvdvideo` demuxer does not handle CSS transparently~~ | **Retired.** Confirmed working, Appendix A.1. |
| ~~`-preindex` chapters disagree with the IFO~~ | **Retired.** Chapters arrive without it; `-preindex` moves a mark by 124 ms and costs a second read. Off by default, Appendix A.3. |
| **DVD reads run at ~1× (1.3 MB/s)** | ~82 min to extract a full disc. Same for dvdbackup, so it is the drive, not the method. Biggest performance lever in the project; investigate block size, read-ahead, and `/dev/sr1`. Appendix A.4. |
| **MenuNet reads artwork with no text on it** | DVD button text often lives in the subpicture layer. Composite subpicture over video before OCR. `CreditsNet` (A.6) is the hedge — it worked on the first disc tried. |
| **`ClassifyFailure` mis-groups benign messages** | The demuxer logs `Error cracking CSS key` and `Probably not a DVD-ROM device` while succeeding. Both would be matched as failures today. Needs negative cases. Appendix A.1, A.8. |
| **BD+ Blu-rays need MakeMKV, so the key stays load-bearing** | Detect BD+ up front and route to MakeMKV; report the collection's DRM mix so exposure is visible (§5.5.2). Unavoidable — the BD+ VM data is shipped by nobody. Appendix B. |
| **A BD+ scan burns 30+ min of pinned CPU with no disc I/O** | Release the global `discSlot` during BD+ compute (it contends for CPU, not the bus), give BD+ its own timeout, and surface CPU-vs-`read_bytes` so "working" is distinguishable from "hung". Appendix B.3. |
| Auto-file thresholds are wrong | Every auto-file is listed with one-click undo. Undo is a rename, not a re-encode. |
| Episode alignment confidently mis-orders a season | Report the margin to the runner-up as confidence; a season of uniform runtimes has no margin and goes to review. Refuse to guess when the evidence cannot distinguish. |
| PAL speedup breaks region 2 matching | Detect from frame rate, scale by 25/24. **Unvalidated — first region 2 disc is the test.** |
| Two schemas drift | Go's SQLite holds only facts about bytes and never metadata. The seam is narrow on purpose. |
| TVDB v4 requires a paid key for some access | TMDB covers both film and television and is free for personal use. Build TMDB-first, TVDB as an optional second provider. |
| Postgres is another service to run | Real cost, accepted. Rails' queue, full-text search and JSON columns all want it, and Solid Queue on SQLite under a long-lived bridge is a worse trade. |

---

## 10. Open

1. **Where identification lives.** Porting `internal/identify` to Ruby duplicates
   tested code; keeping it in Go puts a judgment behind the facts boundary.
   Leaning Ruby — it is the part most likely to be rewritten repeatedly, and
   iteration speed matters more there than anywhere. Decide in Phase C.
2. **Whether `rips/` should keep Blu-ray raws for anything.** Currently no.
   Revisit if playlist selection turns out to be wrong often.
3. **Music CDs.** Out of scope, mentioned only so it stays out. The drive reads
   them and nothing in this design handles them.
4. **`/srv/media/arm`.** Still listed as unresolved in v1. Confirm it holds
   nothing wanted, then delete it deliberately, never from a setup script.
5. **Single drive. Settled 2026-08-19, and for a better reason than was
   recorded here.** *(Was "second drive"; the ASUS was removed 2026-08-09.)*

   This entry used to say the second drive went because it was the slower one
   and could not read Blu-ray, and that `discSlot` serialising disc work was
   "now free rather than a tradeoff — there is nothing to contend with". Both
   halves understate it.

   **The drives share a USB bus, and two of them working at once does not halve
   each one's throughput — it collapses both.** So one drive is the
   configuration, not an accident of which one happened to fail, and
   serialisation is load-bearing rather than vestigial. `discSlot` is what
   would protect the bus if a drive were ever plugged back in; it reads like
   caution with nothing to guard and it is not. Said again in
   `internal/daemon/worker.go`, where someone might otherwise delete it.

   This also settles a contradiction between this file and
   `where-things-stand.md`, which still described two drives as current.

   What one drive costs, unchanged:

   - **Throughput is bounded by the bus, not the backlog.** A shelf-sized
     queue runs strictly serially and a second drive would not fix it.
   - **A drive fault is unfalsifiable.** Every "is it the disc or the drive?"
     question in Phase A was answered by comparison against the other drive.
     That tool is gone; a failing drive now looks exactly like a shelf of bad
     discs.
   - **Region 2 has no fallback.** The remaining drive is region-locked to
     region 1 with four changes left, and is untested against region 2
     (invariant 5). The drive with the unspent counter is the one that left.
   - **The MakeMKV fallback does not work on this hardware.** MakeMKV hangs on
     every disc in the remaining drive — proven by the same disc, binary and
     key succeeding in the drive that has since been removed. `native_dvd =
     false` is therefore a switch to a path that hangs, not a way out of
     trouble. It costs nothing to keep and must not be relied on.

6. **Cross-platform hardware support. Closed 2026-08-19: no.** Raised because
   development moved to a Mac and the drives did not. hellboxd talks to drives
   through Linux ioctls — CDROM_DRIVE_STATUS, and SG_IO for region, protection
   and disc kind — and Docker Desktop cannot pass a USB device into a container
   on macOS at all, because containers run inside a VM with no sight of host
   USB. UTM or QEMU could, and would put SG_IO across a virtualised USB bus,
   which is the exact class of thing §2.1 is about: a wrong region read costs
   one of five permanent counter decrements.

   Containerising hellboxd on Linux *is* possible — ARM did it, and
   `/dev/dri` passthrough was verified working (Appendix, arm-qsv notes). It
   was still rejected, and the reason stands: device passthrough and group-id
   problems are what motivated leaving ARM.

   **Anything touching a physical drive is Linux, on the host, and that is a
   requirement rather than a limitation.** With one user and one machine,
   portability here buys nothing and costs complexity.

   What replaces it is better anyway. `libdvdread` takes a device, a directory
   holding `VIDEO_TS`, or an ISO; libbluray's tools take a device, a BDMV
   directory or an ISO. So the whole pipeline below the drive — enumeration,
   decryption, playlist selection, PAL detection, verification, identification
   — runs against an image, anywhere, in seconds rather than hours, and gives
   the same answer twice. That is where every bug so far has actually been.
   `HELLBOX_DVD_SOURCE` and `HELLBOX_BD_SOURCE` are what the hardware tests
   read; they were `_DEVICE` and the rename is the point.

---

## Appendix A — Phase A results

*Run 2026-08-08 against `The Karate Kid` (1984), Columbia, NTSC, CSS, disc
region mask `0xf2` (regions 1/3/4), in `/dev/sr0` (ASUS SDRW-08D2S-U, USB,
RPC-2, **no region set, 5 of 5 user changes remaining**). hellboxd stopped
throughout.*

### A.1 The headline: native CSS decryption works

```
$ ffmpeg -f dvdvideo -title 1 -i /dev/sr0 -t 20 -map 0:v -map 0:a:0 -c copy out.mkv
frame=488 ... size=11528KiB time=00:00:20.03 speed=3.94x
```

The extracted frames decode to clean, correct video — visually confirmed on a
Columbia Pictures logo and on the opening credits. **No MakeMKV, no registration
key, no dvdbackup copy, no region change spent, no stall.** The region counter
was re-read afterwards and is still 5 of 5.

`DVDCSS_METHOD` of `key`, `disc` and `title` all produced byte-identical
payloads, so no method override is needed — the default is correct. libdvdcss
falls back on its own when the drive refuses authentication, which is precisely
the behaviour §2.3 predicted but had only ever exercised through dvdbackup.

> One cosmetic caveat: the demuxer logs
> `libdvdnav: Error cracking CSS key for /VIDEO_TS/VTS_03_1.VOB` while opening,
> for a VTS unrelated to the requested title, and then reads the requested title
> perfectly. **This message is not a failure and must not be matched as one.**
> `ClassifyFailure` would currently group it under `region` on the `css`
> fragment — that is a live bug for v2 and the failure patterns need a negative
> case for it.

### A.2 Enumeration

Twelve real titles. Titles 13 and 14 return **duration 0 rather than an error**,
so end-of-enumeration is `duration <= 0`, not a non-zero exit.

| Title | Duration | Chapters | Audio | Subs | |
|---|---|---|---|---|---|
| 1 | 2:06:48 | 28 | 3 | 8 | the feature |
| 2 | 1:59 | 2 | 1 | 1 | |
| 3 | 3:45 | 2 | 1 | 0 | |
| 4 | 13:02 | 2 | 1 | 1 | |
| 5 | 8:17 | 2 | 1 | 1 | |
| 6 | 10:00 | 2 | 1 | 2 | |
| 7 | 23:59 | 2 | 1 | 1 | |
| 8 | 21:24 | 2 | 1 | 1 | |
| 9 | 2:06 | 2 | 1 | 0 | |
| 10 | 1:28 | 2 | 1 | 0 | |
| 11 | 1:04 | 2 | 1 | 0 | |
| 12 | 0:06 | 2 | 1 | **18** | a subtitle-setup stub |

Full stream metadata comes through: `mpeg2video 720x480 SAR 32:27 DAR 16:9
29.97fps top-first`, AC3 audio tagged `eng`/`fre`, and subtitles tagged
`eng`/`fre`/`spa`/`tha` each carrying a `VIEWPORT` metadata key of `Widescreen`
or `Letterbox`. That last one is new information v1 never had, and it is exactly
the marker `library.Name()` strips out of volume labels — worth keeping as a
stream attribute.

**v1's classifier gets this disc right.** Sorted durations give a median of 225s;
the feature is 7608s, comfortably past both `minFeatureSecs` and
`featureRatio × median`, and `looksLikeEpisodes` on the remainder correctly
returns false (its median of 225s is below `minEpisodeSecs`). `KindMovie`. ✔

Title 12 — six seconds, eighteen subtitle streams — is the kind of thing that
should be excluded by `min_title_seconds` and is worth a regression test.

### A.3 Chapters and `-preindex`

Payload is byte-identical with and without it (65,393,785 bytes either way), so
`-preindex` affects only chapter marks. On title 9 it moved the final mark from
`124.667` to `124.791` — 124 ms. Chapters are present **without** it, which the
design had assumed required it.

Given it costs a second read of the title, `-preindex` should be **off by
default and configurable**. 124 ms on a chapter boundary is not worth doubling
the read of a two-hour feature.

### A.4 Throughput — the real constraint

| Method | Throughput | vs realtime |
|---|---|---|
| Native `-f dvdvideo` from drive | 1.27–1.34 MB/s | 2.6–2.7× |
| `dvdbackup` sequential copy | 1.25 MB/s | — |

**They are the same speed, and both are roughly 1× DVD read speed.** This kills
the concern that per-title native reads would be slower than one sequential
copy: the drive is the bottleneck, not the access pattern.

It also removes most of the reason for the copy-then-extract detour. v1 needed
the copy because *MakeMKV* could not read the disc; ffmpeg can, so the copy buys
nothing on the primary path and costs 7 GB of scratch. Keep `internal/decrypt`
for retries on damaged discs and for discs the native path refuses, and stop
routing every region-blocked disc through it.

Extracting all twelve titles at this rate is ~82 minutes for this disc. **1×
is suspiciously slow for a drive rated far higher, and is the single biggest
performance lever in the project.** Worth investigating before Phase B is
called done: libdvdread's block size and read-ahead, whether the USB bridge is
negotiating a slow mode, and — the cheap experiment — whether `/dev/sr1`, the
newer Blu-ray drive, reads DVDs faster.

### A.5 Menus, and a correction to §5.3

Menu extraction works but **`-menu` requires `-pgc` to be non-zero** — with
`-pgc 0` (the default) it refuses with `Invalid argument`. So MenuNet must sweep
`menu_vts × pgc`, not just request "the menu". On this disc:

| `menu_vts` | `pgc` | Result |
|---|---|---|
| 0 | 1, 2 | 2 KB — effectively blank |
| 0 | 3 | 36 KB — a real frame |
| 1 | 2 | 2 KB |
| 1 | 1, 3 | no menu |

The 36 KB frame turned out to be the **near-black copyright warning**, and
tesseract returned nothing from it even after a contrast and unsharp pass.

Two lessons, both of which change the MenuNet design:

- **Frame size is a usable cheap filter, but not a sufficient one.** A blank menu
  is ~2 KB and a real one is much larger, so size prunes the sweep — but a
  36 KB frame can still be a legal warning rather than a menu.
- **DVD menu button text often lives in the subpicture layer, not the video.**
  Extracting the video stream alone can yield artwork with no text on it.
  MenuNet must composite the subpicture stream over the video before OCR, not
  OCR the video plane. This was not accounted for in §5.3 and is the main risk
  to that net.

### A.6 A new net: `CreditsNet`

Unplanned, and it worked on the first try. This disc's volume label is
`DVD_VIDEO` — literally zero information, the exact case §2.5 describes. OCR
over frames sampled from the first 160 seconds of the feature returned:

```
cred-03: Columbia Pictures Presents
cred-04: ... A John G. Avildsen Film ...
cred-11: Edited by Bud Smith  Walt Mulconery  John G. Avildsen
```

plus `Elisabeth Shue` from a frame at 90 s. A director, a cast member, a studio
and a 126-minute runtime identify this film unambiguously in TMDB, from a disc
whose label says nothing at all.

This is a strong net and it is cheap in the right place: **sample the credit
frames during the rip, when the bytes are already streaming past**, rather than
re-reading the disc for it. One `fps=1/12` output branch alongside the main
`-c copy` costs nothing and yields ~13 frames to OCR offline.

It also degrades well. Names are far more discriminating in a provider search
than a mangled title would be, and a net that reads `Picturés:Presents)*` — as
one frame did — simply contributes nothing rather than a fabrication, which is
the §2.5 rule holding.

Add to the §5.3 table:

| Net | Evidence | Strength |
|---|---|---|
| `CreditsNet` **new** | tesseract over credit frames sampled during the rip: studio, director, cast | Strong, and works when the label and menus give nothing. |

### A.7 Second drive, and a correction to invariant 5

`/dev/sr1` now exists — `HL-DT-ST BD-RE BT10N`, a Blu-ray writer. Its region
state is **not** the clean slate `/dev/sr0` has:

```
/dev/sr0  RPC-2  no region set   mask 0xff  5 user changes left
/dev/sr1  RPC-2  region set      mask 0xfe  4 user changes left   (plays region 1)
```

`sr1` has already spent one change and is locked to region 1. Invariant 5 was
written as though no drive had ever been set; it should read **"no further
region change is ever spent"**, and the health check should display the
counter so the remaining budget is visible rather than discovered.

This costs nothing in practice — libdvdcss makes the drive's region irrelevant,
which A.1 has now demonstrated on the drive with *no* region at all, the harder
case. It does mean a region 2 disc in `sr1` is a genuinely untested combination.

> **Superseded the next day.** The ASUS was physically removed on 2026-08-09
> and the BD-RE is now the only drive (invariant 5). The two-drive comparison
> above is kept because it is the record of what each drive reported, and
> because it is the evidence behind two things that outlived it: that this
> collection's remaining drive is region-locked, and that the pristine 5-of-5
> counter is no longer available as a fallback. Everything below that reads as
> a plan involving `sr0` is now history rather than intent.

### A.8 Still unverified

- ~~**The native demuxer against a directory.**~~ **Closed, and it works.**
  `-f dvdvideo -i /mnt` against the read-only mounted disc produced 62 MB of
  video that decoded with **zero errors** and rendered correctly. libdvdcss
  resolved the mount point back to the underlying block device and decrypted
  normally, in spite of logging
  `libdvdnav: Can't read name block. Probably not a DVD-ROM device` — **another
  benign message that must not be matched as a failure**, alongside the CSS one
  in A.1.

  A fully decrypted on-disk copy is the easier case still, since its VOBs are
  already plaintext and no CSS is involved at all; the only thing in doubt was
  whether the demuxer accepts a directory, and it does. The `internal/decrypt`
  fallback path is therefore sound as designed.
- **Any region 2 disc**, and therefore the PAL speedup compensation in §5.3.
- **Blu-ray playlist enumeration**, `bdmt_eng.xml`, and anything in `/dev/sr1`.
- **`-c copy` timestamp handling.** Remuxing a mid-GOP cut produced repeated
  non-monotonic DTS warnings from the muxer. Harmless on a 20-second sample and
  probably an artifact of the cut, but a full-title rip must be checked for it
  before verification tolerances are set.

---

## Appendix B — Blu-ray results, and a wrong assumption

*Run 2026-08-08 against `Sunshine` (2007, Fox Searchlight), in `/dev/sr1`
(HL-DT-ST BD-RE BT10N). SCSI GET CONFIGURATION reports current profile `0x40`,
BD-ROM. Volume label `SUNSHINE_US`.*

### B.1 The free stack cannot read a BD+ disc

```
aacs.c:   AACS/Unit_Key_RO.inf found. Disc seems to be AACS protected.
bdplus.c: BDSVM/00000.svm found. Disc seems to be BD+ protected.
aacs.c:   Loaded libaacs                    ← AACS is fine
bdplus.c: bdplus_init() failed!
dec.c:    bdplus_init() failed
[bluray]  Unable to decrypt BD+ encrypted media
bluray:/dev/sr1: Input/output error
```

**AACS is not the problem.** libaacs loaded and engaged against the 64 MB
`KEYDB.cfg`. **BD+ is the problem.** `libbdplus 0.2.0` is installed but the BD+
virtual machine data it requires — `vm0` and the conversion tables — is present
nowhere on the system: not in `~/.config/bdplus`, `~/.cache/bdplus`,
`/usr/share/libbdplus`, nor the package. No distribution ships it. Without it
`bdplus_init()` cannot start the VM, and the disc cannot be opened at all.

### B.2 This falsifies a load-bearing claim in §4.3

§4.3 states the Blu-ray path involves "no MakeMKV, no key". That was generalised
from v1's working Blu-ray path — but **every Blu-ray v1 read successfully must
have been AACS-only.** The first BD+ disc tested breaks it.

Corrections:

- **MakeMKV is required for BD+ discs**, not an opt-in rescue path.
- **The MakeMKV key auto-refresh stays load-bearing** for as long as any BD+
  disc is in the collection. §2.2's conclusion — that the key problem is
  designed away — holds for DVD and for AACS-only Blu-ray, and **not** for BD+.
- **BD+ must be detected early and cheaply.** `BDSVM/00000.svm` is visible in
  the disc structure before any decryption is attempted, so the daemon can route
  to MakeMKV immediately instead of discovering it minutes in.
- §5.5.2 exists because of this: the collection reports its own DRM mix, so the
  residual exposure to key expiry is a number on a screen.

**v1 predicted this and the v2 draft ignored it.** `internal/disc/failure.go`
already carries `FailureBDPlus` — *"a Blu-ray using BD+, whose virtual machine
libbdplus implements less completely than MakeMKV does"* — with correct advice.
The taxonomy was right. The process lesson is larger than the technical one:
§2 exists precisely so that a hard-won v1 finding is not re-lost in v2, and it
was re-lost anyway inside a single draft.

### B.3 MakeMKV on BD+ is expensive, and may not terminate

`makemkvcon -r --cache=1 info dev:/dev/sr1`, valid key installed:

| Elapsed | CPU | Bytes read from disc | Output |
|---|---|---|---|
| 14:13 | 853 s | 104 KiB | startup banner only |
| 28:22 | 1702 s | 104 KiB | startup banner only |
| 44:34 | 2674 s | 104 KiB | startup banner only — **killed** |

**Pinned at 100% of one core, with essentially no disc I/O.** `read_bytes` did
not move by a single byte across a 30-second sample. The process is computing,
not reading: this is BD+ processing, and its cost is CPU, not the drive.

**Control test — the drive and the disc are fine.** After killing MakeMKV, a raw
read of the same disc in the same drive:

```
$ dd if=/dev/sr1 of=/dev/null bs=1M count=64
67108864 bytes (67 MB) copied, 11.7861 s, 5.7 MB/s
```

So the 45 minutes were not an I/O problem, a dirty disc, or a wedged drive.
They were pure BD+ computation that never converged.

> **Two corrections, 2026-08-09. The second withdraws the first.**
>
> *First*, a test against `/dev/sr0` holding no disc hung for 100 seconds with no
> output, which was read as MakeMKV being non-functional for any drive access.
>
> *Second*, the same test against `/dev/sr1` holding no disc **completed in one
> second**, correctly enumerating the drive, with no key complaint:
>
> ```
> 02:38:43  MSG:1005 "MakeMKV v1.18.4 linux(x64-release) started"
> 02:38:44  DRV:0,1,999,0,"BD-RE HL-DT-ST BD-RE BT10N A103 ...","","/dev/sr1"
> ```
>
> **So MakeMKV is healthy, and the first correction was wrong.** What hung was
> `/dev/sr0` specifically — a drive independently suspect: `blkid` on it timed
> out at the very start of the session, and it read DVDs at roughly 1x (A.4).
> It has since been physically removed.
>
> **Net effect: the B.3 measurements below stand as recorded.** MakeMKV, working
> normally, burned 45 minutes of pinned CPU on a BD+ disc in `/dev/sr1` and
> produced no title list. Whether that is BD+ being expensive, MakeMKV hanging on
> that particular disc, or something else is **still unresolved** — but it can no
> longer be blamed on a broken installation.
>
> The methodological lesson is the same one §2.1 states and this session relearned
> twice: one negative result on one device is not a property of the software.
> Both hangs looked identical and had different causes.

### B.3a A second BD+ disc, on proven-healthy hardware

*2026-08-09, after `sr0` was removed and MakeMKV was confirmed working.*

The Sunshine run had two confounds: an unproven MakeMKV install, and a drive
(`sr0`) that later turned out to be faulty. Both are gone. MakeMKV enumerates
`/dev/sr1` in **one second**, and `sr1` reads raw sectors at 5.7 MB/s.

Firefly disc 1, `makemkvcon -r --cache=1 --progress=-same info dev:/dev/sr1`:

| Elapsed | CPU | Disc read | Output |
|---|---|---|---|
| 0:20 | 16 s | 0 KiB | one `PRGV:65536,65536,65536` line |
| 2:00 | 1:56 | 0 KiB | unchanged |
| 6:00 | 5:56 | 0 KiB | unchanged |
| 10:00 | 9:56 | 0 KiB | unchanged |
| **15:16** | **15:16** | **0 KiB** | **unchanged — killed at the cap** |

CPU tracks wall-clock exactly — one core, pinned, continuously — and the disc
is never read.

**The measurement was cross-checked before it was trusted.** `/proc/PID/io`
counts block-layer I/O, and MakeMKV reads optical media through SCSI
passthrough, which can bypass that accounting entirely — so zero there might
have meant nothing. `/sys/block/sr1/stat`, which is device-level and does see
passthrough, independently reported **0 reads completed and 0 sectors read**
across a 40-second window while CPU advanced by the full interval. Both
counters agree.

Immediately after the kill, the same drive read the same disc at **8.0 MB/s**,
so nothing about the drive or the disc was damaged or wedged by any of it. **Two BD+ discs, two different titles, the
same signature, on hardware now proven healthy in every other respect.**

The one difference from Sunshine is that Firefly emits a single progress line
at twenty seconds before going quiet, which Sunshine never did. Whatever
MakeMKV is doing, it gets marginally further here and then stops in the same
place.

This is no longer explicable by a broken install, a bad drive, or a bad disc.
The remaining possibilities are that BD+ processing in 1.18.4 does not converge
on these pressings, that the version has aged out of handling them, or that
something about this environment defeats it in a way that produces no
diagnostic at all.

**What it means for the design.** BD+ cannot currently be read by anything on
this machine — not the free stack, and not its designated fallback. The honest
position is therefore:

- **BD+ discs are enumerated, catalogued and marked unreadable.** Name, episode
  count, runtimes, chapter counts and artwork all survive (B.6), so the disc
  appears in the interface as a known thing that cannot be ripped rather than as
  a failure.
- **The MakeMKV fallback stays wired but is not relied upon.** It costs nothing
  to attempt and may start working with a newer release.
- **Buying a MakeMKV licence is not currently justified.** It would buy a
  permanent key for a path that has not been shown to work here. Revisit if a
  newer version changes the result — that test is cheap and repeatable now.
- **The untested case is the one that matters most.** No AACS-only Blu-ray has
  been through v2 at all, and that is the path v1 used successfully. It is
  expected to work and it is unverified.

> **Third correction, 2026-08-09, and this one explains all the others.**
>
> A plain CSS **DVD** — The Karate Kid, which the native path enumerates in 27
> seconds and extracts from at 2.23 MB/s — was given to `makemkvcon`. It behaved
> exactly as both BD+ discs did: one core pinned, **zero disc sectors read**
> across a 35-second window, nothing but the startup banner after five minutes.
>
> **So this was never about BD+.** `makemkvcon` cannot read *any* disc on this
> machine. The empty-drive test that made it look healthy proved only that it
> can enumerate a drive, which needs no disc reading at all — and that is why it
> returned in one second while every disc hangs forever.
>
> The cause is visible in the process tree: **`makemkvcon` has no children and
> `mmgplsrv` is never running.** mmgplsrv is the helper that reads the disc. It
> executes fine when run by hand and sits beside `makemkvcon` in `/usr/bin`, so
> it is present — it simply never gets started, and instead of failing,
> `makemkvcon` spins. v1's README already lists this install as unfixed on this
> machine, with two symlinks standing in for a matching-prefix install, and
> names a missing `mmgplsrv` as producing exactly "cannot open the disc".
>
> **What survives:** B.1 stands entirely — libbdplus cannot handle BD+, observed
> directly from libbdplus and nothing to do with MakeMKV.
>
> **What is withdrawn:** every inference in B.3 and B.6 about *what BD+ costs
> under MakeMKV*. We do not know. MakeMKV never reached either disc, so the
> 45-minute and 15-minute figures measure a broken helper, not a protection
> scheme.
>
> **What follows.** The A/B comparison against MakeMKV cannot be run until the
> install is repaired — `scripts/install-makemkv.sh --accept-eula --prefix /usr`,
> which needs root. Until then the native path is not merely preferable, it is
> **the only path that reads a disc on this machine at all**, which is a stronger
> argument for §4.2 than the one originally made for it.
>
> **Fourth correction, same day: the mmgplsrv explanation above is wrong.**
>
> A MakeMKV **snap** (1.18.3, devmode, therefore unconfined) was installed as a
> completely independent second copy, bundling its own helpers under its own
> prefix. It reported `MSG:5021` — version too old — and exited *cleanly in
> seconds*, which proves the binary and its helpers are fine. A current beta key
> was fetched from the forum and installed, at which point it got past the key
> check and **hung exactly like the other install**: no children, no `mmgplsrv`,
> zero disc sectors read, one core pinned.
>
> Two independent installations, different versions, different prefixes,
> identical behaviour. **The prefix/symlink hypothesis is disproven.**
>
> What remains consistent with every observation is the *drive*. MakeMKV ripped
> a DVD successfully on 2026-07-28 — in `sr0`, which no longer exists. Every
> failure since has been against `/dev/sr1`. The empty-drive test on `sr1`
> succeeded because enumerating a drive opens no disc.
>
> **Confirmed, once the second drive came back.** The ASUS was reconnected at
> `/dev/sr0` and the same disc put in it. `makemkvcon -r info dev:/dev/sr0`
> returned **exit 0 with 165 TINFO lines** and a complete title list, in under
> three minutes.
>
> **MakeMKV cannot read discs in the HL-DT-ST BD-RE BT10N, and works normally in
> the ASUS.** Not BD+, not the install, not the prefix, not the key, not the
> machine — the drive. Every hang in this session was `sr1`; the one success
> before it, and this one, are `sr0`.
>
> That also rehabilitates `sr0`. It was called faulty on the strength of a
> `blkid` timeout, a 1x read speed and a MakeMKV hang. The hang is now explained
> by nothing to do with it, and `blkid` answered instantly on reconnection, so
> the case against it reduces to being genuinely slow. **A slow drive that reads
> discs is worth more than a fast one that half the toolchain cannot open.**
>
> The A/B is unblocked, using `sr0`.

The methodological lesson lands four times in one session, the same each time: a
negative result explains itself only when the thing it is compared against is
known to work. Four hangs looked identical. The first explanation blamed BD+,
the second blamed a broken install, and both were confident and wrong.

**Stated fairly:** this is one disc, one MakeMKV version (1.18.4), one attempt.
It is *not* proof that MakeMKV cannot do BD+ in general — that is its headline
feature and it is widely reported to work. What is established is that **it did
not complete a BD+ scan here in 45 minutes of pinned CPU**, which is far outside
the seconds-to-minutes this step is supposed to take. A competing
`makemkvcon -r info` was running for roughly the first five minutes (the §2.12
health-check contention bug, reproduced live), but minutes 5–45 were
uncontended, so that does not explain it. Worth one clean retry and a version
check before the design commits to MakeMKV as the BD+ answer.

A scan that behaves this way has consequences regardless of how it ends:

- **`scan_timeout` (45m) is calibrated for a slow *read*, not a long compute.**
  A BD+ disc can plausibly exceed it while working correctly, and the existing
  stall detector watches MakeMKV's progress values — which are not being emitted
  here at all. Both need a BD+-aware path.
- **The one-disc-at-a-time `discSlot` is badly served by this.** A BD+ disc
  holding the global slot for half an hour blocks every other drive while using
  no drive bandwidth whatsoever. The slot exists to prevent *bus* contention;
  BD+ processing contends for CPU instead, so it should release the slot.

  > Moot in practice as of 2026-08-09: with one drive there is nothing for the
  > slot to block. Worth keeping, because the reasoning is about what the slot
  > is *for*, and it becomes live again the moment a second drive appears.
- **The UI must distinguish "working hard" from "hung".** They are visually
  identical here, and only `read_bytes` against CPU time separates them. That
  measurement should be exposed, not hidden.

### B.4 Still unverified for Blu-ray

Everything §4.3 claims beyond decryption remains untested, because no disc has
been opened:

- ~~**Playlist enumeration**~~ — **confirmed working, B.6.**
- ~~**`bdmt_eng.xml` / `BdmtNet`**~~ — **confirmed working, B.6.**
- **An AACS-only Blu-ray**, which is the case §4.3 was actually written for and
  which is expected to work — v1 read such discs successfully. **This is now the
  only major untested Blu-ray path**, and both discs to hand were BD+.
- **What the BD-RE actually manages on a DVD.** A.4's ~1× was measured on the
  ASUS, which no longer exists, so the figure the whole schedule rests on now
  describes a drive that is not in the machine. Re-measure on the first DVD;
  until then, treat ~82 minutes per disc as an upper bound of unknown
  tightness rather than a number. See B.5.

Installing `libbluray-bin` and one AACS-only Blu-ray would close most of this.

### B.6 Firefly disc 1 — the box-set case, and it works

*`FIREFLYUS_D1`, 20th Century Fox. Tested after `libbluray-bin` was installed.*

**Also BD+.** Two Blu-rays tested, two Fox pressings, two BD+ discs — a weak
sample, but a hint about this collection's exposure.

**The important result: everything except decryption works.** `bd_info` and
`bd_list_titles` read the disc completely without decrypting a single frame,
because playlists and metadata are not encrypted — only the `.m2ts` payload is.

```
AACS detected : yes    libaacs detected : yes    AACS handled : yes
BD+  detected : yes    libbdplus detected: yes   BD+  handled : no
Disc ID       : B0D76EF00DE4D8C949340F0D21E9AABB2ED9AC4F
AACS MKB version : 9
```

#### `BdmtNet` is confirmed, and it is as strong as hoped

```
Metadata file : bdmt_eng.xml
Disc name     : FIREFLY: DISC 1
Thumbnail     : Firefly_metadata_640.jpg
```

Prose a human typed, plus **cover art**, off a disc that cannot be played. Note
`Disc #: 1/1` is wrong — the disc is one of four — so **trust the name, not the
disc-number field**. The name itself carries "DISC 1", which is the usable
signal.

#### Playlist enumeration confirmed — and v1's box-set bug reproduced exactly

29 playlists. Filtering to real content:

| Playlist | Duration | Chapters | Audio | Subs | |
|---|---|---|---|---|---|
| `00001.mpls` | **1:26:42** | 21 | 5 | 6 | the pilot, *Serenity* (double length) |
| `00002.mpls` | **0:42:43** | 13 | 5 | 6 | episode |
| `00003.mpls` | **0:43:54** | 13 | 4 | 6 | episode |
| `00004.mpls` | **0:43:58** | 13 | 5 | 6 | episode |
| `00006.mpls` | 0:28:39 | 2 | 1 | 0 | an extra |
| 24 others | ≤ 1:11 | 1–8 | 0–1 | 0 | junk, stings, transitions |

**libbluray reports `Main title: 8` — the 87-minute pilot.** v1's Blu-ray path
takes the main playlist and nothing else, so on this disc it would have produced
**one file and silently dropped three episodes.** That is the §2.12 bug,
reproduced concretely on a real disc, and playlist enumeration fixes it.

#### The junk filter should key on streams, not just duration

Duration alone leaves the 28-minute extra indistinguishable from an episode. The
stream layout separates them cleanly: **every real episode carries 4–5 audio
tracks and 6 subtitle tracks; the extra carries 1 audio and no subtitles; the
junk carries no audio at all.** A predicate of *duration ≥ min_title_seconds AND
audio ≥ 1*, with subtitle count and chapter count as strong secondary evidence,
is far more discriminating than a duration threshold on its own. `bd_list_titles`
reports all of it, so it is free.

#### v1's classifier survives a disc built to break it

Durations `[5202, 2638, 2634, 2563, 1719]`. The feature-ratio test is
`5202 ≥ 2.0 × 2634 = 5268` → **false by 66 seconds**, a 1.3% margin. On the
ratio test alone this disc is a coin toss.

It classifies correctly anyway, because commit `a485595` asks the shape of the
*remainder* first: strip the pilot and `[2638, 2634, 2563, 1719]` is plainly
episodes. **`Classify` returns `KindTV`.** ✔

That fix was written for a hypothetical Star Trek disc with a double-length
two-parter. This is that disc, in the wild, and the fix earns its keep. Keep the
ordering of those two tests exactly as it is, and add this disc as a test case.

#### Firefly is also the canonical episode-order trap

Broadcast order and intended/DVD order differ famously for this series, and
providers disagree about which they list. Disc order here is
pilot → `00002` → `00003` → `00004`, i.e. production order. **The batch UI
(§5.5.1) must let the user choose which ordering a season is being filed in**,
and record that choice on the batch, because runtime alignment cannot possibly
distinguish two orderings of the same episodes. Defaulting silently to the
provider's order would mis-number a series that a lot of people care about
getting right.

#### What this changes in the design

**Identification and ripping are separable, and should be separated.** A BD+
disc can be fully catalogued — name, cover art, episode count, runtimes, chapter
counts, stream layouts — while being unrippable. So the daemon should always
enumerate before attempting decryption, and a disc that cannot be read should
land in the UI as *"Firefly: Disc 1 — 4 episodes, BD+, needs MakeMKV"* rather
than as an opaque failure. That is a far better experience than v1's
`aacs-key`/`bd-plus` group can give, and it costs one `bd_info` call.

### A.9 The native pipeline, end to end on real hardware

*2026-08-09, The Karate Kid in `/dev/sr1` — the BD-RE, and now the only drive.*

**Enumeration is drive-independent.** All twelve titles matched A.2 exactly:
every duration, chapter count and audio/subtitle count identical to what the
removed `sr0` reported. The A.2 table is therefore a valid baseline and does not
need re-qualifying now that the drive that produced it is gone. Classified
`movie`, PAL correctly false, and a second pass returned the same titles — which
matters because an unstable enumeration makes the fingerprint worthless and
every disc would be re-ripped on sight.

Enumeration took **27 seconds for twelve titles**, against roughly two minutes
for six on `sr0`. That is mostly IFO reading, so it flatters the drive.

**Extraction, which is the number that counts:**

```
extracting t10 (0:01:04)
  27% at 4.4x · 52% at 4.2x · 77% at 4.0x
wrote 38.9 MB in 17s (2.23 MB/s, 3.7x realtime)
verified: 64s measured against 64s claimed
```

| | `sr0` (removed) | `sr1` |
|---|---|---|
| Extraction throughput | 1.27–1.34 MB/s | **2.23 MB/s** |
| Against realtime | 2.6–2.7x | **3.7x** |

> **Withdrawn 2026-08-09.** Both figures below were measured while `sr0` was on
> a degraded USB connection — the same fault behind its `blkid` timeout. After
> reconnection, `sr0` extracts at **2.20 MB/s (3.6x realtime)** against `sr1`'s
> 2.23 MB/s (3.7x). **The drives are the same speed.** There is no throughput
> reason to prefer either, and the whole-disc figure is ~50 minutes on both.
>
> Three separate conclusions in this document rested on `sr0` being slow, and
> all three came from one bad cable. It is worth stating plainly: *every*
> measurement taken of that drive before it was reseated is suspect.

**About 1.7x faster, not the 4.5x B.5 guessed.** That guess came from comparing
a raw `dd` of unencrypted Blu-ray sectors against CSS-decrypted, demuxed DVD
reads, which was not like for like and said so at the time. 1.7x is the honest
figure. The whole-disc estimate falls from ~82 minutes to **roughly 50**.

**Verification agreed exactly** — 64 seconds measured against 64 claimed, with
no drift at all on a title extracted from its start. The ±2% tolerance in §4.4
is untested by this because nothing came close to needing it.

**The `-c copy` timestamp warnings from A.8 did not appear.** They were an
artifact of cutting a 20-second sample out of the middle of a title, not a
property of the demuxer. A full title extracted from its start remuxes cleanly.

Progress reporting works: the callback fired at sensible intervals with a
realtime multiple that tracked the drive.

**What this leaves.** The native path now has an end-to-end proof on real
hardware — enumerate, extract, verify, all agreeing. The remaining risk in the
worker swap is not whether the code works but whether it works on *every* disc,
which only a dozen discs can answer.

### B.5 An unplanned throughput lead

The control read in B.3 measured **`/dev/sr1` at 5.7 MB/s** on raw sectors,
against the **1.27–1.34 MB/s** `/dev/sr0` managed on DVD in A.4 — roughly
**4.5× faster**.

The comparison is not like-for-like: raw `dd` of an unencrypted BD does no CSS
key derivation and no demuxing, so some of the gap is workload rather than
hardware. But A.4 established that dvdbackup and the native demuxer hit the
*same* 1.25–1.34 MB/s ceiling on `sr0`, which points at the drive rather than
the method, and `sr0` is an older USB DVD writer while `sr1` is a newer BD-RE.

**Overtaken by events, 2026-08-09.** The ASUS was removed before the comparison
was run, so the 4.5× gap was never confirmed and now never can be — one of the
two drives no longer exists. What it prompted was the decision to standardise on
the BD-RE (invariant 5), which is now permanent rather than provisional.

Two consequences worth being explicit about, because a plausible number is
worse than a missing one:

- **A.4's ~1× describes a drive that is no longer in the machine.** Every
  schedule estimate derived from it — most visibly "~82 minutes per disc" —
  is now unanchored. It is not known to be pessimistic *or* optimistic.
- **Re-measure on the first DVD through the BD-RE**, and treat that as the real
  baseline. Until then the honest statement about DVD throughput is that it has
  not been measured on this hardware.

Drive selection is no longer a scheduling decision, because there is nothing to
select between.
