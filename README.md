# hellbox

A disc ripping service for a home media server. Replaces Automatic Ripping
Machine with something narrower and more predictable.

Insert a disc, walk away, come back to a complete and verified set of raw MKVs
and an open tray.

| Binary | Role |
|---|---|
| `hellboxd` | Daemon. Owns the optical drives, the database, and the job queue. Runs under systemd on the host. |
| `slay` | Terminal UI client. Connects over a unix socket; can be killed and restarted without disturbing a rip. |

## Status

Phase 1 (ripping) — working. A disc has been through the whole path unattended:
inserted, scanned, decrypted, ripped, verified, and the tray opened on its own.

- Phase 1 — ripping — **working**
- Phase 2 — transcoding (ffmpeg + VAAPI) — **working**
- Phase 3 — library filing — **working, deliberately minimal**

A disc now goes from the tray to Jellyfin without intervention: detected,
scanned, decrypted if the drive cannot read it, ripped, verified, ejected,
transcoded, and hardlinked into the library.

Working: drive discovery, MakeMKV integration, scanning, fingerprinting and
dedupe, the rip pipeline, decrypt fallback for discs the drive cannot read, the
SQLite store, health checks with automatic MakeMKV key refresh, the client
socket, and `slay`. `make check` passes against the real hardware and the daemon
runs under systemd with every health check green.

First complete rip, 2026-07-28 — `ROMAN_HOLIDAY`, 7 titles, 7.5 GB. Thirty-six
minutes decrypting the disc to `work_dir`, then ninety-five seconds to write and
verify every title, because ripping from the local copy runs at disk speed
rather than drive speed.

Verified against the real machine rather than assumed:

- Disc detection for empty, tray-open, unreadable, **and readable** media.
- Key refresh recovering from both a missing and an invalid key.
- The service's confinement, which permits writing the MakeMKV key file and
  nothing else in the home directory.
- **The MakeMKV attribute codes** in `internal/makemkv/robot.go`, previously
  unverified guesses, now confirmed against a real DVD: `8` chapters, `9`
  duration, `10`/`11` size, `25`/`26` segments, `27` output filename, `32`
  volume name, `1` type, `2` name. Codes `24`, `30`, `33` and `49` also appear
  on DVDs and are not yet named; they are retained in the raw map regardless.
- **All four invariants**, on the disc that went through: the disc is recorded
  as read and will eject unread if reinserted; the rips directory carries
  `disc.json`, `makemkv-info.txt` and a 443 KB `rip.log` beside the titles; no
  step waited on a person; and the tray opened only after every title verified.
- **A rip directory is never clobbered.** The successful rip landed beside two
  earlier failed attempts rather than overwriting them.

Outstanding before Phase 1 is done:

- **`retry` is stubbed** (`internal/daemon/server.go`). Eject and reinsert works
  in the meantime, and forgetting a disc (`f` in `slay`) covers the case where
  the attempt cap is in the way.
- **Classification has met few discs.** A concert disc, an anthology, or a film
  with a feature-length documentary will be filed wrongly. Recoverable — a
  hardlink in the wrong directory — but it will happen.
- **Nothing in hellboxd tells Jellyfin to rescan.** Its scheduled scan finds new
  files eventually. The refresh call exists, but it lives in slay, which does
  the filing now — so a hellboxd filing on its own still leaves Jellyfin to
  notice in its own time. See
  [`docs/where-things-stand.md`](docs/where-things-stand.md).
- **The MakeMKV prefix bug is fixed in the script but not yet on this machine**,
  where two symlinks stand in for a matching-prefix install.

## Quick start

Assuming MakeMKV is already built — if not, start at
[`docs/install.md`](docs/install.md).

```
sudo make install          # binaries, config, state and run directories
sudo make install-service  # the systemd unit
sudo systemctl enable --now hellboxd
make check                 # confirm the board is green
slay                       # watch it work
```

Then put a disc in. Nothing else is required: the daemon polls the drive, and
everything from that point is automatic.

## Running it

### The normal path

You insert a disc and walk away. Which of the two paths below it takes depends
on the disc, and you do not have to say which — the drive is asked with SCSI
GET CONFIGURATION, rather than guessing from what is on the disc, which would
mean mounting it first.

#### A DVD

1. **Detects** the disc within `poll_interval` (2s by default), using SCSI TEST
   UNIT READY rather than the kernel's drive-status ioctl, which lies on this
   hardware.
2. **Scans** it with `makemkvcon info` to enumerate titles, retrying a failed
   scan up to `scan_attempts` times. A drive reports its disc ready before
   MakeMKV can necessarily open it, so a first-attempt failure is normal and not
   a verdict on the disc.
3. **Fingerprints** it and checks the database. A disc that has already been
   ripped is ejected immediately without being read again — the fingerprint
   exists so that losing track of which discs are done costs twenty seconds
   rather than an hour.
4. **Checks whether the drive can read the disc at all**, by reading its region
   state and the disc's protection over SCSI. A drive that cannot decrypt the
   disc gets it copied to `work_dir` and decrypted there first — see
   [Known problems](#known-problems).
5. **Rips** every title longer than `min_title_seconds`, one at a time, into a
   hidden staging directory.
6. **Verifies** each output file before accepting it, then moves it into place
   under its final name.
7. **Ejects.** An open tray means the disc is done and safe to reshelve.
8. **Transcodes** each title, in the daemon rather than the drive's worker — so
   the tray opens and the next disc can go in while the GPU is still busy.
9. **Files** each finished transcode into the library by hardlink, where
   Jellyfin picks it up.

#### A Blu-ray

Shorter, because most of what a DVD needs does not apply.

1. **Detects** it the same way, then reads its volume label with `blkid` — a
   Blu-ray keeps its name in the UDF descriptor, where a DVD keeps it in the
   ISO9660 one.
2. **Encodes it straight from the drive**, with no rip and no decrypt stage.
   libaacs decrypts in place as ffmpeg reads, so the disc is the input.
3. **Files** the result, then **ejects**.

No MakeMKV and no registration key are involved. A key is what gates Blu-ray in
MakeMKV, and the beta keys expire on a schedule of their own; nothing here needs
one. AACS also has no region enforcement, so none of the region machinery a DVD
needs applies either.

The trade is that no raw rip is kept, which breaks the rule that holds
everywhere else. A DVD title is about a gigabyte and keeping it is what makes a
re-encode cost minutes; a Blu-ray raw is eighteen, and a few dozen would fill
the machine. It costs less than it looks: with no decrypt stage, reading a
Blu-ray again costs minutes, where re-reading a DVD costs the decrypt it took
the first time.

Two things a Blu-ray does not get:

- **Chapters.** ffmpeg's `bluray:` is a protocol rather than a demuxer — a byte
  stream with no chapter marks in it — so Jellyfin has no chapter navigation for
  these files.
- **More than one title.** Only the main playlist is reachable without the
  playlist list, which needs the disc mounted. Films are fine. A disc of
  episodes yields one file rather than one per episode, which is visible
  immediately rather than silently wrong.

A failed disc stays in the drive with the tray shut. That asymmetry is
deliberate: a tray that opens on failure hands back a disc that did not rip,
indistinguishable from one that did.

### Watching it: `slay`

```
slay                       # socket path comes from the config
slay -socket /run/hellbox/hellbox.sock
```

| Key | |
|---|---|
| `d` | drives view |
| `h` | history |
| `l` | log |
| `e` | eject the selected drive |
| `r` | retry — not implemented for drives; in the queue view, re-queue the selected job |
| `c` | cancel — the rip on the selected drive, or the running transcode in the queue view |
| `f` | forget the disc in the selected drive, so it is ripped again |
| `t` | transcode queue — what is waiting, running, done, or failed |
| `x` | failures — discs that did not make it, grouped by why |
| `T` | transcode the disc in the selected drive again, from its rip |
| `R` | rescan — re-detect drives and re-run health checks |
| `↑`/`↓` or `k`/`j` | select a drive |
| `q`, `esc`, `ctrl+c` | quit |

`slay` holds no state and reconnects on its own. Start it before the daemon,
kill it, or lose it with an SSH session — none of that touches a rip in
progress. **Per-title progress and ETA exist only in `slay`**; the log
deliberately does not carry them.

### Watching it: the log

```
journalctl -u hellboxd -f
```

A whole disc, from insertion to open tray — this one needing the decrypt path:

```
15:25:46 INFO  disc detected
15:26:09 WARN  scan attempt 1 of 3 failed: no titles found on dev:/dev/sr0:
               Failed to open disc; retrying in 15s
15:28:24 INFO  scan succeeded on attempt 2 of 3
15:28:24 INFO  "ROMAN_HOLIDAY" — 7 titles, 7.6 GB, 2h50m22s
15:28:24 INFO  this disc is CSS, region [1] and the drive is RPC-2, no region
               set (5 changes available) — copying it to disk to decrypt it first
16:04:21 INFO  decrypted 7.7 GB to disk
16:04:52 INFO  title 1 of 7 written (5.1 GB in 31s)
16:05:05 INFO  title 2 of 7 written (1.2 GB in 13s)
...
16:05:56 INFO  title 7 of 7 written (649.9 MB in 12s)
16:05:56 INFO  complete — "ROMAN_HOLIDAY"
```

The first scan failing is routine, not a fault: the drive reports the disc ready
before MakeMKV can open it.

### Drive states

What `slay` shows, and what each state means:

| State | Meaning |
|---|---|
| `absent` | The drive could not be read at all — unplugged, or dropped by the kernel. |
| `empty` | Closed, no disc. |
| `tray_open` | Open and waiting to be fed. The resting state between discs. |
| `loading` | Tray closing, or disc spinning up. |
| `incompatible` | A disc this drive cannot read — a Blu-ray in a DVD drive, usually. Not a failure: nothing was attempted. Needs a person, so it is reported once and left alone. |
| `scanning` | Reading the disc structure. |
| `decrypting` | Copying the disc to `work_dir` and decrypting it, because the drive cannot. Slower than the rip that follows. |
| `cancelled` | Someone stopped the work. The disc is untouched and still in the drive. Not a failure — nothing is wrong with it. |
| `ripping` | Writing titles. |
| `verifying` | Checking output files. |
| `complete` | Finished and verified. |
| `duplicate` | Already ripped; nothing was done. |
| `failed` | Rip failed, disc retained, tray shut. |
| `ejecting` | Opening the tray. |

### Checking health without a disc

```
make check          # or: hellboxd -check
```

Runs every startup health check against the real hardware without starting the
daemon or touching a disc. The first thing to reach for when a rip fails for no
apparent reason.

```
  drive  ASUS SDRW-08D2S-U (/dev/sr0)
         stable id: usb-ASUS_SDRW-08D2S-U_M2AP1AB5728-0:0
         state: disc present

  ok    makemkv          v1.18.4
  ok    makemkv key      accepted
  ok    rips directory   /srv/media/rips
  ok    free space       834.9 GB free
  ok    drives           1 detected
```

> `state:` comes from the drive and cannot be taken at face value on this
> hardware — an empty ASUS SDRW-08D2S-U reports `disc present`. See §4 of the
> specification.

## Transcoding

Raw rips are the system of record and are never touched. Everything downstream
reads them and writes elsewhere, which is what makes a transcode cheap to redo:
after a settings change, or a bug, it costs minutes from a file already on disk
rather than another trip to the shelf.

**ffmpeg with VAAPI**, software as the fallback. Chosen by measuring both on
this hardware rather than by expectation — at matched output size they are
within 0.0013 SSIM of each other, and hardware is about five times faster. A
machine whose GPU is unavailable transcodes slowly rather than not at all, and
`hellboxd -check` says which is in use.

Nothing is scaled and nothing is resampled. The stack this replaces ran PAL
television through a "HQ 720p30 Surround" preset and produced 900 MB episodes
that looked worse than the disc.

**Interlaced sources are deinterlaced**, to one frame per field so the full 50 or
60 Hz motion survives. Detected by probing each file, not configured: it is a
property of the disc. On one disc here the feature is progressive film and an
extra is interlaced video, so a per-disc setting would have been wrong either
way.

**Quality is capped, not fixed.** `transcode_quality` is a quantizer, not a size
target, and what it costs depends entirely on the source:

| | Source | At quality 20 alone | With a 2500 kbps ceiling |
|---|---|---|---|
| 1953 film, clean | 5.4 GB | 1.78 GB | 1.5 GB — never near the cap |
| 2002 sitcom, noisy | 1.1 GB | **1.0 GB** — larger than its source | **475 MB** |

The same setting that compressed a film threefold spent its whole budget
preserving broadcast noise. The ceiling engages only on material that would
otherwise balloon, so one setting serves both without hellbox needing to know
what it is looking at.

Audio and subtitles are copied rather than re-encoded, and chapters are carried
across. The video is the only part worth compressing.

## Filing into the library

**Off by default since 2026-08-09: slay files the library now** (§5.4 of the v2
design). What follows describes hellboxd's own filing, which is still here,
still works, and is the whole library with Rails switched off — set
`file_to_library = true` when nothing else is writing `library_dir`. Running
both puts two entries in Jellyfin for every disc, because this path names a
film from its shape and slay names it from a provider.

Deliberately minimal. Jellyfin matches titles against real metadata providers
and does it far better than hellbox could, so hellbox produces names Jellyfin
can match and contributes the one thing Jellyfin cannot know: which disc a file
came from.

A disc is classified by the shape of its titles, because a disc says nothing
about what it holds. A film disc is lopsided — one title dwarfing the rest. An
episode disc is level — several of much the same length.

```
Movies/Roman Holiday/Roman Holiday.mkv               the longest title
Movies/Roman Holiday/extras/Roman Holiday - t01.mkv  everything else
TV/Still Game/Still Game - c887d54d5276 - t00.mkv    rename these yourself
```

Films get Jellyfin's exact convention and should match unaided. Extras go in
`extras/`, which is what stops a featurette being offered as an alternative
version of the film.

**Episodes are filed unnumbered**, because a disc carries no episode numbers and
none can be inferred: every disc of a series is typically labelled identically,
with nothing to say which is which. Each file carries the fingerprint of the rip
it came from, so it traces straight back to a directory holding `disc.json` and
the original. Renaming them in Jellyfin is the intended workflow.

Filed by **hardlink**, so the library and the transcoded tree are the same bytes
— no space, no copy time, and a file renamed in the library still points at the
same data. **A file already in place is never replaced**: it may be one you
renamed, and hellbox must not lose work it did not do.

Filing also reconciles rather than only reacting. Anything transcoded while
filing was off, or before it existed, is picked up on the next pass instead of
needing to be encoded again.

> Classification is a heuristic over runtimes and has met few discs. A concert
> disc, an anthology, or a film with a feature-length documentary will land in
> the wrong place eventually. It fails recoverably — a hardlink in the wrong
> directory — but it will happen.

## What lands on disk

Three trees, each with a different job.

**`rips_dir`** holds the raw rip, one directory per disc, named
`YYYY-MM-DD--slug--fingerprint`:

```
/srv/media/rips/2026-07-27--roman-holiday--5d7fd5a1507a/
├── disc.json          the parsed disc: titles, streams, durations, sizes
├── makemkv-info.txt   verbatim makemkvcon output from the scan
├── rip.log            verbatim makemkvcon output from each title rip
├── title_00.mkv       one file per title, numbered by MakeMKV title index
├── title_01.mkv
└── …
```

A disc with a generic or empty volume label slugs to `unlabeled`. If a directory
for the same disc already exists and is not empty, the new one gets a `-2`
suffix rather than clobbering it.

Three things about this layout are load-bearing:

- **`disc.json` and `makemkv-info.txt` are written before a single byte is
  ripped.** If the rip is interrupted, what the disc contained is still on disk
  — which is what lets later phases work without the disc, and what let the
  attribute codes be confirmed after the fact.
- **Titles are written to a hidden `.rip-*` staging directory** and moved into
  place only after they verify. A crash cannot leave something that looks
  finished.
- **The disc is marked ripped only once every title has been written and
  verified.** A partially ripped disc must not be recognised as done when it is
  reinserted.

This is invariant 2 in practice: the database is an index, not the system of
record. Delete `state.db` and the rips tree still describes itself.

**`transcoded_dir`** mirrors that layout, one encoded file per title under the
same disc directory name — so the two trees walk side by side and any file
traces back to the rip it came from.

**`library_dir`** holds hardlinks to those files under Jellyfin's naming. Same
bytes, no extra space. Jellyfin mounts it read-only.

## Configuration

`/etc/hellbox/config.toml`. Every value is a default, so the file only needs to
contain what differs — and a missing file is not an error. `make install` never
overwrites an existing one, and settings added by a later version default
cleanly into an older file.

See [`config.example.toml`](config.example.toml) for the fully commented set.
The ones worth knowing:

| Setting | Default | |
|---|---|---|
| `rips_dir` | `/srv/media/rips` | Where finished rips land. |
| `min_title_seconds` | `60` | Titles shorter than this are not ripped. Excludes menu loops and stills. |
| `min_output_bytes` | `10485760` | Output below this is treated as a failed rip, not a short title. |
| `max_rip_attempts` | `2` | Rip attempts per disc before it is retained for inspection. |
| `scan_attempts` | `3` | Scan attempts before the drive gives up on a disc. |
| `scan_retry_delay` | `"15s"` | Pause between scan attempts. |
| `rip_stall_timeout` | `"10m"` | Stop a title that has made no progress for this long. `0` waits indefinitely. |
| `transcode` | `true` | Transcode a rip once it verifies. |
| `transcode_quality` | `20` | Quantizer — qp for VAAPI, crf for software. Lower is better. |
| `transcode_max_kbps` | `2500` | Ceiling on output bitrate. `0` for none. |
| `vaapi_device` | `"auto"` | Render node, or software when absent. |
| `transcoded_dir` | `/srv/media/transcoded` | Where transcodes land. |
| `file_to_library` | `true` | Hardlink finished transcodes into `library_dir`. |
| `decrypt_fallback` | `true` | Decrypt a disc the drive cannot read to `work_dir` and rip the copy. |
| `eject_on_success` | `true` | An open tray means the disc is done. |
| `eject_on_failure` | `false` | Leave a failed disc in the drive, distinguishable from a successful one. |
| `poll_interval` | `"2s"` | How often each drive is polled. |
| `auto_refresh_key` | `true` | Install MakeMKV's published beta key when the one in place has expired. |

> **`min_title_seconds` is not just a filter.** MakeMKV numbers titles within its
> *filtered* list, so a disc scanned under one value and ripped under another
> would rip the wrong titles. hellbox always uses the same value for both;
> changing it between a scan and a rip of the same disc is not safe.

Drives are discovered automatically. The optional `[[drives]]` blocks exist only
to give a drive a friendly name or to stop hellbox using it:

```toml
[[drives]]
stable_id = "usb-ASUS_SDRW-08D2S-U_M2AP1AB5728-0:0"
label     = "top"
disabled  = false
```

Find the stable id with `hellboxd -check`.

## Troubleshooting

**Start with `make check`.** It exercises MakeMKV, the key, the rips directory,
free space and drive detection without needing a disc.

### A scan fails with `Failed to open disc`

Usually transient — the drive answers TEST UNIT READY before MakeMKV can open
the disc. `scan_attempts` now covers this; the log will show which attempt
succeeded. If all attempts fail on a disc that reads fine elsewhere, check that
`mmgplsrv` is where `makemkvcon` expects it (below).

### `Failed to execute external program 'mmgplsrv'` or `'mmccextr'`

`makemkvcon` resolves its helpers relative to its own location, so both halves of
MakeMKV must install under the same prefix. When they do not:

| Missing | Symptom |
|---|---|
| `mmgplsrv` | `Failed to open disc` — it is what reads the disc |
| `mmccextr` | Rips fail **only on titles carrying closed captions**, which looks like a disc fault |

Reinstall with one prefix for both:

```
scripts/install-makemkv.sh --accept-eula --prefix /usr
```

The install verifies afterwards that both helpers sit beside `makemkvcon` and
names the mismatch if they do not. If makemkv.com is unreachable, pass
`--version` and `--tarball-dir` to build from tarballs already on disk.

### A rip stalls, or fails with `no progress for 10m0s`

Almost certainly the drive's region, not the disc. Check it:

```
python3 scripts/region.py          # drive RPC state and the disc's region mask
```

A `type code` of 0 means no region has ever been set, and an RPC-2 drive in that
state cannot decrypt CSS. See [Known problems](#known-problems).

### A disc says `incompatible`

A Blu-ray in a DVD-only drive, almost always — hellbox reads Blu-rays, but only
in a drive that can. Nothing was attempted and nothing is retried, so swap the
disc, or move it to the Blu-ray drive.

### A Blu-ray produced one file and you expected episodes

Expected, for now. Only the main playlist is reachable without mounting the
disc, so a box set yields its longest playlist and nothing else. Films are
unaffected.

### A disc was already ripped and you want it ripped again

Put it in a drive and press **`f`** in `slay`. That forgets the disc in front of
you, with no fingerprint to copy.

Over the socket instead, if you are scripting it — note that `forget` matches
the **full** fingerprint, while the rip directory name carries only its first 12
characters, so take it from `disc.json` rather than the directory name:

```
jq -r .fingerprint /srv/media/rips/2026-07-28--roman-holiday--5d7fd5a1507a/disc.json
```

Forgetting clears the recorded rip directory and releases the attempt cap, so a
disc that had used up `max_rip_attempts` is tried again rather than refused with
`already failed N times`. The failed jobs themselves are kept — they say why the
disc needed forgetting — and simply stop counting.

The files on disk are never touched, so the next rip lands beside the original
rather than replacing it.

### `retry is not implemented yet`

Known. Eject and reinsert the disc.

### The daemon will not start after an upgrade

Check `journalctl -u hellboxd` for a config parse or validation error. An
existing `/etc/hellbox/config.toml` is never rewritten by an install, and new
settings default into old files — a regression test pins that — so this should
not happen.

## Known problems

**The drive has no region set, and cannot decrypt CSS until it has.** hellbox
works around this without spending a region change, and the workaround has now
carried a disc end to end. The region counter is still untouched at 5 of 5.

Read from the drive with a SCSI `REPORT KEY` (key format `08h`):

```
REPORT KEY raw : 00 06 00 00 25 ff 01 00
type code      : 0     no region set
region_mask    : 0xff  nothing playable
user changes   : 5 of 5 remaining
vendor resets  : 4 remaining
rpc_scheme     : 1     RPC-2, enforced in firmware
```

An RPC-2 drive with no region set will not perform CSS authentication. That
splits cleanly:

- **Scanning works.** The IFO structures are not encrypted, so all 7 titles of
  `ROMAN_HOLIDAY` (a Region 1 disc — `VMG_CATEGORY` `0x00fe0000`) enumerate
  correctly with sizes and durations.
- **Ripping does not.** The VOB payload needs CSS. MakeMKV logs
  `Region setting of drive ... does not match ... trying to work around...`
  thirty times, then `LIBMKV_TRACE: Exception: Error while reading input`, and
  stops reading without exiting.

**hellbox works around this rather than spending a region change.** libdvdcss
does not need the drive's authentication: when the drive refuses to hand over a
title key it derives the key from the disc's own data. So a disc the drive
cannot read is copied out with dvdbackup and ripped from the copy, and the
region counter stays untouched. `decrypt_fallback` controls this; it costs about
half an hour and a disc's worth of `work_dir` space.

Verified interchangeable: one disc scanned from the drive and from the decrypted
copy produced byte-identical title indices, durations, sizes and output names.
The copy is kept if the rip fails and reused by the next attempt, so a retry
does not pay the decrypt twice.

Setting a region is the faster alternative but permanent: five changes and the
drive locks forever, and a mixed-region collection cannot be served by one RPC-2
drive at all.

> A folder source reports **no volume label** — `CINFO` 2, 30 and 32 are absent.
> hellbox therefore always scans the drive and only ever *rips* from the copy;
> scanning the copy would change the disc's fingerprint and name its directory
> `unlabeled`.

> Do not read the region through the kernel's `DVD_AUTH` ioctl. On this drive it
> returns values that decode to a plausible but wrong answer; the SCSI
> `REPORT KEY` above is authoritative.

**A stalled rip is now stopped rather than waited on.** `rip_stall_timeout`
(default 10m) fails a title that stops making progress. Progress means MakeMKV's
own progress values changing, not output arriving — the rip that hung kept
emitting messages while reading nothing, so a timer reset by any output would
never have fired. Set it to `0` for the old behaviour.

**`retry` is stubbed.** `internal/daemon/server.go:247`.

**The health check overlaps the scan.** `runHealthChecks` shells out to
`makemkvcon -r info disc:9999`, which enumerates every drive, and at startup it
runs concurrently with a disc scan on the same device. Two `makemkvcon`
processes contending for one drive is not intended. It does not explain the
stall above — the 15-minute health-check tick does not line up with the observed
failures — but it should be serialized.

## Documentation

| Document | What it is |
|---|---|
| [`docs/design-v2.md`](docs/design-v2.md) | The v2 design: native DVD reading without MakeMKV, Blu-ray playlist enumeration, a Postgres catalog and a Rails frontend that identifies and files. Supersedes the Phase 1 spec for everything it covers. |
| [`docs/phase1-spec.md`](docs/phase1-spec.md) | The Phase 1 design. Describes what is built today. |
| [`docs/install.md`](docs/install.md) | Setting the system up from scratch. |
| [`docs/pipeline-design-v1.md`](docs/pipeline-design-v1.md) | Historical. The original whole-pipeline sketch, from when ARM was still in the design. |
| [`docs/arm-qsv-investigation.md`](docs/arm-qsv-investigation.md) | Historical. Why ARM and HandBrake QSV were abandoned; the evidence behind separating ripping from transcoding. |

Four invariants hold across all phases:

1. A disc is never read twice. Raw rips are permanent and immutable; every later
   stage reads from them and writes elsewhere.
2. The rips directory is self-describing. The database is an index, not the
   system of record.
3. The daemon never blocks on a human. Unattended operation is the default.
4. An open tray means success, and only success.

## Development

```
make build     # both binaries into bin/
make test      # go test ./...
make vet
make fmt
make check     # build, then run the health checks against real hardware
```

Tests do not invoke `makemkvcon` and do not need a disc or a drive. The suite
passes under `-race`, which covers the socket reading status snapshots while a
worker mutates them.

`sudo make install` recompiles as root and leaves `bin/` root-owned, which
blocks the next ordinary `make build`:

```
sudo chown -R $(id -un):$(id -gn) bin
```

## Related

Jellyfin runs separately as a Docker Compose stack in `~/media-box-rebuilt`.
Hellbox writes to the library that stack serves; the two are otherwise
independent.
