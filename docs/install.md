# Installing hellbox

Everything here targets the machine hellbox actually runs on: Ubuntu 26.04 on an
Intel N100, one USB optical drive, Jellyfin in Docker alongside.

## 1. MakeMKV

No package exists for Ubuntu 26.04, so it is built from the official tarballs.

```
scripts/install-makemkv.sh --accept-eula
```

Needs sudo twice — once for build dependencies, once for `make install` — and
takes a few minutes to compile. It verifies `makemkvcon` is on PATH before
reporting success, so a clean finish can be trusted.

The script determines the current version from the download page. Note that page
links only the Windows and Mac builds and refers Linux users to a forum thread;
the version is read from the artifacts it *does* carry, and the Linux tarball
URLs are derived from it. They exist, they are simply unlinked. Pass
`--version X.Y.Z` to override.

### The registration key

MakeMKV will not start without a valid key. The free beta key expires roughly
monthly and lives in `~/.MakeMKV/settings.conf` of the user hellboxd runs as.

The daemon refreshes it automatically when it lapses — see §13 of the Phase 1
specification for what that does and does not promise. To manage it by hand
instead, set `auto_refresh_key = false` and copy the current key from the
[forum thread](https://forum.makemkv.com/forum/viewtopic.php?t=1053).

## 2. hellbox

```
sudo make install
```

Installs `hellboxd` to `/usr/local/bin`, seeds `/etc/hellbox/config.toml` if
absent (an existing one is never overwritten), and creates `/var/lib/hellbox`
and `/run/hellbox` owned by the invoking user.

`sudo make install` recompiles as root, leaving `bin/hellboxd` root-owned in the
working tree. Harmless, but it can block your next ordinary `make build`:

```
sudo chown -R $(id -un):$(id -gn) bin
```

> The install deliberately uses `RUN_USER`/`RUN_GROUP` rather than `USER`/`GROUP`.
> `USER` is already an environment variable, make's `?=` will not override one,
> and sudo sets it to root — so the obvious spelling silently installs
> everything root-owned and the daemon cannot open its own database.

## 3. Check

```
make check
```

Runs every startup health check against the real hardware without starting the
daemon or touching a disc. This is the first thing to reach for when a rip fails
for no apparent reason. A healthy board:

```
  drive  ASUS SDRW-08D2S-U (/dev/sr0)
         stable id: usb-ASUS_SDRW-08D2S-U_M2AP1AB5728-0:0
         state: disc present

  ok    makemkv          v1.18.4
  ok    makemkv key      accepted
  ok    rips directory   /srv/media/rips
  ok    free space       835.3 GB free
  ok    drives           1 detected
```

> `state:` comes from the drive and cannot be taken at face value on this
> hardware — see §4 of the specification. An empty ASUS SDRW-08D2S-U reports
> `disc present`.

## 4. Run under systemd

```
sudo make install-service
sudo systemctl enable --now hellboxd
```

hellboxd runs as an ordinary user in the `cdrom` group, not as root: it needs the
optical device and the media directories and nothing else. The unit grants write
access to `~/.MakeMKV` alone so key refresh works, and keeps the rest of the home
directory read-only. To confirm that confinement on a running service:

```
awk '$5 ~ /^\/home/ {print $5, $6}' /proc/$(systemctl show -p MainPID --value hellboxd)/mountinfo
```

`/home` should be `ro` and `~/.MakeMKV` `rw`. Note `systemd-run --user` cannot be
used to test this — it ignores `ProtectHome` and will report the home directory
writable regardless.

## 5. Watch it work

```
slay
```

The terminal client. It reads the socket path from the same config, or takes
`-socket`. Keys: `d` drives, `h` history, `l` log, `e` eject, `r` retry, `c` cancel,
`f` forget, `R` rescan, `q` quit; `↑`/`↓` select a drive when there is more than
one.

`f` forgets the disc in the selected drive so it is ripped again — the way to
re-rip something already done, or a disc that has used up its attempts.

It holds no state of its own and reconnects on its own, so it can be started
before the daemon, killed, or lost with an SSH session without touching a rip in
progress.

## Environment notes

- **Go** is at `~/.local/go`, not system-wide. `~/.profile` puts it on PATH, so a
  login shell has it and a non-login shell may not. The Makefile resolves it
  either way, including under sudo, where `secure_path` hides it.
- **Jellyfin** runs from `~/media-box-rebuilt` and serves `/srv/media/library`.
  It has no access to `/srv/media/rips`, and the two stacks are otherwise
  independent.
- **Media lives on** `/dev/mapper/ubuntu--vg-ubuntu--lv`, 913 GB.

## Uninstalling

```
sudo make uninstall
```

Leaves `/etc/hellbox` and `/var/lib/hellbox` in place; remove them by hand if
that is what you want. Rips are never touched.
