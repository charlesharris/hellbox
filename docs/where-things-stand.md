# Where things stand — 2026-08-09

A working note, not a design. [`design-v2.md`](design-v2.md) is the design; this
says what of it exists, what does not, and what the next person should pick up.

## Built and committed

| Package | What it does | Hardware-tested |
|---|---|---|
| `internal/bluray` | Enumerates a Blu-ray without decrypting it: `bdmt_eng.xml` name, cover art, AACS/BD+ status, playlists filtered by stream layout, ordered by playlist number | Yes — Firefly disc 1 |
| `internal/dvd` | Native title enumeration and extraction via ffmpeg's `dvdvideo` demuxer; PAL detection and the 25/24 runtime correction | Enumeration yes (Karate Kid); extraction **no** |
| `internal/source` | Decides which path reads a disc, keeps a blocked disc fully described, counts the collection's DRM mix | Unit only |
| `internal/verify` | Catches a truncated rip by comparing measured duration against what the disc said | Unit only, but against real ffmpeg output |
| `internal/api` | HTTP + SSE with monotonic ids and `Last-Event-ID` resume | Yes — against the running daemon |
| `internal/store` | Records which path read each disc, and the disc's own name | Unit only |

The daemon serves `127.0.0.1:9494` beside the unix socket. Health, drives,
404/405 behaviour and a nine-event replay were all confirmed against the running
daemon rather than only in tests.

## The worker swap is done

**Native is the default as of 2026-08-09.** A whole disc went through it: eleven
titles, 6.7 GB, every one verified against the duration the disc claimed, no
MakeMKV at any point. The 2h06 feature took 19 minutes at 6.6x realtime.

What it changed beyond dropping the licence:

- **A region-blocked disc is read in place.** v1 sent every such disc through a
  half-hour `dvdbackup` copy because MakeMKV could not read them. libdvdcss
  derives a title key from the disc itself, so the copy is now for discs that
  actually fail. The first test run caught this — it announced it was copying to
  disk before anyone had told it not to.
- **Every title is duration-checked** against what enumeration said, which is
  the check that catches a rip that exists, is large, is valid Matroska, and is
  three-quarters missing.
- **The rips tree is browsable.** A disc whose label is `DVD_VIDEO` now lands in
  `2026-08-09--the-karate-kid-special-edition--<fp>` instead of `unlabeled`,
  because the disc's own name is read from the IFO at scan time.

MakeMKV is still installed, still wired, and still works — `native_dvd = false`
goes back to it.

### The accepted cost

Native enumeration reports no per-title byte sizes, and the fingerprint is built
from durations **and** sizes. So a disc ripped under MakeMKV fingerprints
differently now and will be **ripped again** rather than recognised. This was a
deliberate decision, not an oversight. Nothing is lost — the old rip stays where
it is and the rips tree describes itself — but the time is spent again.

The clean fix, if it ever matters, is deriving real sizes from the IFO cell
address tables so both paths agree. Not worth blocking on.

## Hardware reality

**Two drives, and each is the only one that can do something.**

| | `/dev/sr0` — ASUS SDRW-08D2S-U | `/dev/sr1` — HL-DT-ST BD-RE BT10N |
|---|---|---|
| Reads Blu-ray | no | yes |
| DVD extraction | 1.27–1.34 MB/s (2.6x) | 2.23 MB/s (3.7x) |
| MakeMKV can use it | **yes** | **no — hangs on every disc** |
| Region counter | unset, 5 of 5 | locked to region 1, 4 left |

So "prefer `sr1` for DVDs because it is faster" is true only while nothing needs
the MakeMKV fallback. `sr0` is slower and cannot read Blu-ray, and it is the
only drive half the toolchain works with.

`sr0` was called faulty earlier in the project and it is not. That verdict rested
on a `blkid` timeout, a slow read, and a MakeMKV hang; the hang turned out to
have nothing to do with it, and `blkid` answered instantly on reconnection.

- **MakeMKV hangs on any disc in `sr1`** — not BD+, not the install, not the key.
  Proven by the same disc, binary and key succeeding in `sr0` (exit 0, full title
  list) and hanging in `sr1`. Four explanations were tried before this one and
  three were wrong; the discriminating test needed the second drive.
- **BD+ has no working path** in the free stack — that is libbdplus, observed
  directly, and unrelated to the above. Whether MakeMKV could handle BD+ is
  still unknown, because it has never been given a BD+ disc in a drive it can
  read, and only `sr1` reads Blu-ray. **That combination may be untestable on
  this hardware.**
- **A region 2 DVD** is untested. Put it in `sr0`, which has an unspent counter.
- **`www.makemkv.com` is down (525) but the forum is up (200)**, so v1's key
  refresh path still works when the main site does not.

## Rails (`slay/`) — booting and serving

`docker compose up -d` brings up Postgres and Rails. Verified working:

- Postgres healthy on **5433**, eleven catalog tables migrated.
- Rails on **3000**, `/up` returns 200, the dashboard renders at `/`.
- The container reaches hellboxd on host loopback and reads live drive state.
- `/srv/media` is visible inside the container at the same paths hellboxd uses.

Built: the catalog schema, `Disc`/`DiscTitle`/`Candidate` models, a hellboxd
client that refuses a protocol version it does not know, and the dashboard —
needs-you / working / done, plus health. Confirmed rendering correctly with the
daemon both up and down.

**The catalog fills itself.** The SSE bridge (`bin/bridge`) consumes the
daemon's event stream, writes discs and titles into Postgres, and resumes from a
stored cursor. Verified end to end: a disc goes in and lands in the catalog
correctly named.

**Identification runs, in Ruby.** Three nets — the disc's own name, the volume
label, the shape of the titles — resolved by agreement rather than by whichever
was most confident. A disc labelled `DVD_VIDEO` becomes "The Karate Kid",
classified as a film, status confirmed, with nobody involved. `bin/check-nets`
exercises all of it against this shelf's real discs; 24 checks.

**TMDB is wired.** Films and television, with every response cached durably so
identification can be rewritten and replayed offline. A film match is only
believed when the disc's own feature runtime agrees with the provider's —
without that the first search result always wins, which is how a disc becomes
a film of the same name and none of the same content. The 2010 Karate Kid is
demoted rather than accepted on exactly that test.

**It needs an API key.** Set `TMDB_API_KEY` in the environment; a personal key
is free. With none set, the provider net abstains and everything else still
works.

**TVDB is deliberately not built.** TMDB covers both films and television, and
v4 gates some TVDB access behind a paid key, so a second provider is additive
rather than necessary. Nothing assumes TMDB is alone — `provider` and
`provider_id` are columns, not constants — so adding it later is a client and a
net, not a migration. Worth doing if TMDB's episode ordering proves weak for
television.

**The review queue exists.** Every net's proposal with its reasoning, the
disc's own name beside the volume label, and "apply to the rest of this set" —
which turns six identically labelled Still Game discs into one decision. A
person's decision is stored apart from what the nets proposed, so re-running
identification can never overwrite it.

**Filing works, and slay owns the library.** A confirmed disc is hardlinked
into `Movies/` and `TV/` under Jellyfin's naming, with an NFO beside each file,
and Jellyfin is told to rescan. `bin/check-filing` covers the naming, the
sidecars and a real hardlink cycle — 46 checks, including undo. Details in
[Filing](#filing) below.

**Still missing:** Turbo Stream broadcasts (the dashboard reads per request),
batches, `MenuNet`/`SetNet`, and a reconcile pass for when the bridge falls
behind its ring buffer.

## Filing

A confirmed disc becomes library files. `FileDisc` resolves each title's
encoded file, hardlinks it under Jellyfin's layout, writes an NFO beside it and
records a `placements` row; `UnfileDisc` takes it all back out. The library
screen is at `/library`.

```
Movies/Roman Holiday (1953)/Roman Holiday (1953).mkv
                           /Roman Holiday (1953).nfo
                           /extras/Roman Holiday (1953) - Restoring a Classic.mkv
TV/Still Game/tvshow.nfo
             /Season 03/Still Game - S03E05 - Doacters.mkv
                       /Still Game - S03E05 - Doacters.nfo
```

Four properties are load-bearing:

- **Never replace an existing file.** An occupied path is a conflict reported
  to a person, not a write. The unique index on `placements.path` says the same
  thing in the database, so two filing runs cannot race into one path.
- **Safe to run again, always.** Titles finish encoding minutes after a disc is
  confirmed, so filing files what is ready and reports the rest as pending. A
  second run picks up exactly what was missing, and an already-correct link is
  recognised by inode rather than relinked.
- **Undo is a click.** The auto-file thresholds are guesses and the only way to
  tune them is to let them run and cheaply reverse what they get wrong. Nothing
  is re-encoded: the bytes live in `transcoded/` and the library is only ever
  links onto them.
- **Deletion is guarded.** Unfiling touches a path only if it is inside
  `library_dir`, recorded in `placements`, and a regular file. A wrong path in
  the database is otherwise a wrong file off the disk.

`bin/file-pending` is the reconcile sweep — it files anything confirmed and
waiting, and relinks anything recorded as filed whose library file has gone.
Run it with `--every 60` to leave it running beside `bin/bridge`.

### The library has one writer now

**`file_to_library` now defaults to false.** hellboxd's own filing and slay's
both write `library_dir`, and hellboxd names a film from the shape of its
titles alone — so it files `Movies/Roman Holiday/` beside slay's
`Movies/Roman Holiday (1953)/` and Jellyfin shows the same film twice.

hellboxd's filing is kept and still works. It is the entire library with Rails
switched off, which is worth being able to fall back to. **An existing
`/etc/hellbox/config.toml` that sets `file_to_library = true` explicitly still
needs editing by hand** — the new default only applies where the key is absent,
which is the case on this machine.

### Two gaps this found in identification

Both would have made filing useless on exactly the discs it is meant for.

- **An auto-confirmed disc had no name.** `IdentifyDisc` moved a confident disc
  to `confirmed` but never wrote the winning candidate into the disc's own
  `confirmed_*` fields, which are the only thing filing reads. Every disc the
  nets settled on their own was confirmed and anonymous. It now adopts the
  winner, guarded so a re-run can never overwrite a person's decision.
- **A provider id was prose, not data.** TMDB's id existed only inside a
  candidate's `why` text. `candidates` gains `provider` and `provider_id`, the
  resolver carries them onto the merged winner — which is usually the net that
  read the title off the disc, not the one that looked it up — and the review
  form shows the id so a person can supply or correct one.

### What is verified and what is not

`bin/check-filing` runs 46 checks against real hardlinks in a temporary tree
and a rolled-back transaction: the layout, escaping, the sidecars, the
never-clobber rule, a partially encoded disc, an unnumbered episode, undo and
its directory pruning.

**Not verified: a disc going through the real pipeline into the library.**
hellboxd is not running on this machine at the moment and nothing has been
encoded since the native swap, so the Karate Kid disc is confirmed with no
encoded files to link. Filing reports it as waiting, correctly, which is as far
as real data currently goes.

**`/srv/media/library` still holds v1's output** — `Roman Holiday` with no
year, `Spaceballs Lb`, `Still Game - c887d54d5276 - t00.mkv`, no NFOs anywhere.
Nothing has been deleted. Those files are what Phase F is for: point
identification at the rips already on disk and let it refile the shelf. Until
then, expect duplicates in Jellyfin for anything filed under both schemes.

## Rails, how it is set up

Scaffolded as a Docker Compose service. **Ruby is not installed on the host and
there is no passwordless sudo**, so everything Ruby happens in a container.
Docker is already in use for Jellyfin, so this matches the house style rather
than adding a dependency.

`docker-compose.yml` at the repo root runs Postgres and Rails, both on the host
network so Rails reaches hellboxd at the same `127.0.0.1:9494` it would use
outside a container.

One thing in there is load-bearing and easy to break: **`/srv/media` is a single
bind mount, not three.** Filing is by hardlink, and a hardlink needs its source
and destination inside the same mount as the container sees it. Bind-mounting
`rips`, `transcoded` and `library` separately would make every link fail with
`EXDEV`.


## Decisions taken without asking

Recorded so they can be overruled rather than discovered.

- **Rails runs in Docker**, because the host has no Ruby and no sudo.
- **Postgres on port 5433**, not 5432, so a host Postgres appearing later cannot
  collide.
- **The HTTP API binds loopback and that is not really configurable.** Nothing
  authenticates, so the bind address is the only boundary there is; the setting
  is documented as a way to turn the API off, not a place to move it.
- **hellboxd stops filing the library**, because `library_dir` needs exactly one
  writer and slay is the one that knows what a disc actually is. hellboxd keeps
  the code and the config key.
- **Filing runs in the web request, not a job.** Solid Queue is in the Gemfile
  and nothing runs a worker, so a job would be a job that never ran. Filing is
  a handful of hardlinks — the encoding it waits on already happened.
- **NFOs carry no plot or air date**, because nothing yet writes the `episodes`
  table. An empty `<plot/>` tells Jellyfin there is no plot, which is worse
  than staying quiet.
- **A licence for MakeMKV is not worth buying today.** It would buy reliable
  access to a path that has not worked here once. Revisit if a newer release
  changes the BD+ result — that test is now cheap and repeatable.

## Not possible from here

- **Pushing to GitHub.** No remote, no SSH key, no token, no `gh`. Everything is
  committed locally on `master`; there is no `main` branch. Add a remote and it
  can go up in one command.
