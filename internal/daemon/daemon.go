package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"hellbox/internal/api"
	"hellbox/internal/config"
	"hellbox/internal/decrypt"
	"hellbox/internal/drive"
	"hellbox/internal/makemkv"
	"hellbox/internal/proto"
	"hellbox/internal/store"
	"hellbox/internal/transcode"
	"hellbox/internal/version"
)

// Version is the hellbox release, re-exported so daemon code reads unchanged.
const Version = version.Version

// Daemon owns the drives, the state database, and the client socket.
type Daemon struct {
	cfg config.Config
	st  *store.Store
	mk  *makemkv.Runner
	dec *decrypt.Decrypter
	enc *transcode.Encoder

	startedAt time.Time

	mu      sync.RWMutex
	workers map[string]*Worker // keyed by drive stable id
	health  []proto.Health

	// keyMu serialises key refreshes: health checks run from a ticker and from
	// the rescan method, and two concurrent writes to settings.conf would race.
	keyMu           sync.Mutex
	lastKeyRefresh  time.Time
	keyRefreshTried bool

	// syncMu serialises drive discovery. It runs from a ticker and, since
	// rescan was implemented, from the socket handler too; without it two
	// concurrent calls could both find a drive unwatched and start two workers
	// for one device.
	syncMu sync.Mutex

	// wg is Run's wait group, held so workers started by a rescan are waited on
	// at shutdown like any other. Nil until Run is called.
	wg *sync.WaitGroup

	// transcodes is the queue's externally visible state, and transcodeWake
	// lets a finished rip start the next encode without waiting for the tick.
	transcodes    transcodeStatus
	transcodeWake chan struct{}

	// discSlot serialises disc work across every drive. Buffered to one: the
	// holder is whichever drive is currently reading a disc.
	discSlot chan struct{}

	// cancelTranscode stops the encode in flight, if any. Guarded because the
	// socket reads it while the queue runner writes it.
	transcodeMu     sync.Mutex
	cancelTranscode context.CancelFunc

	subs   map[chan proto.Push]struct{}
	subsMu sync.Mutex

	// hub carries the HTTP event stream. Separate from subs above because the
	// two have different contracts: a socket client receives a full status
	// snapshot, an HTTP client an identified delta it can resume from.
	hub *api.Hub
}

// New creates a daemon.
func New(cfg config.Config, st *store.Store) *Daemon {
	// Outbound requests identify the release making them.
	makemkv.SetUserAgentVersion(Version)

	mk := makemkv.New(cfg.MakeMKVBin, cfg.MakeMKVSettingsPath)
	mk.StallTimeout = cfg.RipStallTimeout.Duration

	return &Daemon{
		cfg:           cfg,
		st:            st,
		mk:            mk,
		dec:           decrypt.New(cfg.DVDBackupBin),
		enc:           transcode.New(cfg.FFmpegBin, transcode.ResolveDevice(cfg.VAAPIDevice)),
		startedAt:     time.Now(),
		workers:       map[string]*Worker{},
		transcodeWake: make(chan struct{}, 1),
		discSlot:      make(chan struct{}, 1),
		hub:           api.NewHub(),
		subs:          map[chan proto.Push]struct{}{},
	}
}

// Run starts the daemon and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.prepareDirs(); err != nil {
		return err
	}

	// Any job still mid-flight belongs to a process that no longer exists, and
	// would otherwise count against its disc forever. Done before the drives
	// start so a disc reinserted immediately is judged on its real history.
	if n, err := d.st.ReclaimUnfinishedJobs(ctx); err != nil {
		d.logEvent("warn", fmt.Sprintf("could not reclaim interrupted jobs: %v", err), nil, nil)
	} else if n > 0 {
		d.logEvent("info", fmt.Sprintf("%d job%s interrupted by a restart marked cancelled",
			n, plural(n)), nil, nil)
	}

	// Drives are registered before the checks run: one of the checks counts
	// them, and running it first reported "0 detected" at every startup while
	// the daemon was, in the same breath, announcing the drive it had found.
	var wg sync.WaitGroup
	d.wg = &wg
	if err := d.syncDrives(ctx, &wg); err != nil {
		return err
	}
	if len(d.snapshotWorkers()) == 0 {
		d.logEvent("warn", "no optical drives were found; hellbox will keep looking", nil, nil)
	}

	d.runHealthChecks(ctx)
	for _, h := range d.health {
		if !h.OK {
			level := "warn"
			if h.Fatal {
				level = "error"
			}
			d.logEvent(level, fmt.Sprintf("%s: %s", h.Name, h.Detail), nil, nil)
		}
	}

	// Re-scan for drives periodically so a USB drive plugged in later is picked
	// up without restarting the daemon.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := d.syncDrives(ctx, &wg); err != nil {
					d.logEvent("warn", fmt.Sprintf("drive discovery failed: %v", err), nil, nil)
				}
			}
		}
	}()

	// Health is re-checked periodically so a key that expires while the daemon
	// runs is noticed before the next disc goes in, not after it fails.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.runHealthChecks(ctx)
			}
		}
	}()

	// Push status to subscribed clients on a steady tick. Progress changes
	// continuously during a rip, so polling a snapshot is simpler and no less
	// responsive than trying to diff state transitions.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.broadcastStatus(ctx)
			}
		}
	}()

	// The transcode queue drains in the daemon rather than in a worker, so the
	// tray opens and the next disc goes in while the GPU is still busy.
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.runTranscodes(ctx)
	}()

	srvErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
			srvErr <- err
		}
	}()

	// The HTTP API runs beside the socket rather than replacing it, so slay
	// keeps working while the web client is built.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.serveHTTP(ctx, d.cfg.HTTPAddr); err != nil && !errors.Is(err, context.Canceled) {
			d.logEvent("error", "http api stopped: "+err.Error(), nil, nil)
		}
	}()

	d.logEvent("info", fmt.Sprintf("hellbox %s started", Version), nil, nil)

	select {
	case <-ctx.Done():
	case err := <-srvErr:
		wg.Wait()
		return err
	}

	wg.Wait()
	return nil
}

// prepareDirs creates the directories the daemon writes into.
func (d *Daemon) prepareDirs() error {
	dirs := []string{d.cfg.RipsDir, filepath.Dir(d.cfg.StatePath), filepath.Dir(d.cfg.SocketPath)}
	if d.cfg.Transcode {
		dirs = append(dirs, d.cfg.WorkDir, d.cfg.TranscodedDir)
	}
	if d.cfg.FileToLibrary {
		dirs = append(dirs, d.cfg.LibraryDir)
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// syncDrives brings the set of workers into line with the drives present.
func (d *Daemon) syncDrives(ctx context.Context, wg *sync.WaitGroup) error {
	d.syncMu.Lock()
	defer d.syncMu.Unlock()

	found, err := drive.Discover()
	if err != nil {
		return err
	}

	for _, dr := range found {
		cfgDrive, hasCfg := d.cfg.DriveFor(dr.StableID)
		if hasCfg && cfgDrive.Disabled {
			continue
		}

		d.mu.RLock()
		existing, running := d.workers[dr.StableID]
		d.mu.RUnlock()
		if running {
			// The device path can change when drives are re-enumerated; the
			// stable id is what identifies the drive, so just note the move.
			if existing.drv.DevicePath != dr.DevicePath {
				d.logEvent("info", fmt.Sprintf("%s moved from %s to %s",
					existing.label, existing.drv.DevicePath, dr.DevicePath), nil, nil)
			}
			continue
		}

		label := cfgDrive.Label
		if label == "" {
			label = defaultLabel(dr)
		}

		driveID, err := d.st.UpsertDrive(ctx, dr.StableID, dr.DevicePath, label,
			strings.TrimSpace(dr.Vendor+" "+dr.Model))
		if err != nil {
			return err
		}

		w := NewWorker(dr, label, d.cfg, d.st, d.mk, d.dec, d.enc, driveID, d.discSlot, d.logEvent, d.queueTranscodes, d.fileTitle)
		w.publish = d.publishEvent

		d.mu.Lock()
		d.workers[dr.StableID] = w
		d.mu.Unlock()

		d.logEvent("info", fmt.Sprintf("watching %s as %q", dr.Describe(), label), &driveID, nil)

		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Run(ctx)
		}()
	}
	return nil
}

// defaultLabel names an unconfigured drive after its model, falling back to the
// stable id.
func defaultLabel(dr drive.Drive) string {
	if m := strings.TrimSpace(dr.Model); m != "" {
		return m
	}
	return dr.StableID
}

func (d *Daemon) snapshotWorkers() []*Worker {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]*Worker, 0, len(d.workers))
	for _, w := range d.workers {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label < out[j].label })
	return out
}

// workerFor resolves a drive by stable id or label.
func (d *Daemon) workerFor(name string) (*Worker, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if name == "" && len(d.workers) == 1 {
		for _, w := range d.workers {
			return w, nil
		}
	}
	if w, ok := d.workers[name]; ok {
		return w, nil
	}
	for _, w := range d.workers {
		if strings.EqualFold(w.label, name) {
			return w, nil
		}
	}
	return nil, fmt.Errorf("no drive named %q", name)
}

// haveBluRayDrive reports whether any drive could read a Blu-ray, which decides
// whether a missing key matters.
func (d *Daemon) haveBluRayDrive() bool {
	for _, w := range d.snapshotWorkers() {
		if w.drv.ReadsBluRay() {
			return true
		}
	}
	return false
}

// encoderHealth reports whether transcoding can actually run.
//
// The GPU is checked by using it, not by looking for its device node. The node
// exists whenever the kernel driver is loaded, while encoding also needs a VA
// driver for the chip — and without one every encode fails at the first frame,
// hours after the disc went in. This machine ran for a day with a render node
// and no driver behind it.
func (d *Daemon) encoderHealth(ctx context.Context) []proto.Health {
	if !d.cfg.Transcode {
		return nil
	}
	if err := d.enc.Available(); err != nil {
		return []proto.Health{{
			Name: "ffmpeg", OK: false, Fatal: true,
			Detail: err.Error() + " — install ffmpeg, or set transcode = false",
		}}
	}
	out := []proto.Health{{Name: "ffmpeg", OK: true, Detail: "present"}}

	if !d.enc.Hardware() {
		return append(out, proto.Health{
			Name: "encoder", OK: true,
			Detail: "software — no VAAPI device configured; transcoding will be several times slower",
		})
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := d.enc.CheckDevice(cctx)
	cancel()
	if err != nil {
		// Not fatal. Software encoding still works, and a disc ripped slowly is
		// better than a disc not ripped — but this is the difference between
		// four minutes a film and twenty-three, so it is said plainly.
		return append(out, proto.Health{
			Name: "encoder", OK: false,
			Detail: err.Error() + " — transcoding will fall back to software",
		})
	}
	return append(out, proto.Health{Name: "encoder", OK: true, Detail: "vaapi " + d.enc.Device})
}

// ---------- key refresh ----------

// settingsPath is where makemkvcon reads its registration key from. Taken from
// the runner so the file hellbox writes is always the file makemkvcon reads.
func (d *Daemon) settingsPath() string { return d.mk.SettingsPath }

// refreshKey installs MakeMKV's published beta key, returning a health entry
// describing what happened and whether an attempt was made at all.
//
// The beta key expires roughly monthly and MakeMKV will not run without a valid
// one, so a disc inserted the morning after an expiry would otherwise fail for a
// reason that has nothing to do with the disc. Refreshing is only ever attempted
// after a check has already found the key bad — never speculatively, and never
// often enough to hammer the forum.
func (d *Daemon) refreshKey(ctx context.Context) (proto.Health, bool) {
	d.keyMu.Lock()
	defer d.keyMu.Unlock()

	if d.keyRefreshTried && time.Since(d.lastKeyRefresh) < d.cfg.KeyRefreshInterval.Duration {
		return proto.Health{}, false
	}
	d.keyRefreshTried = true
	d.lastKeyRefresh = time.Now()

	path := d.settingsPath()
	if path == "" {
		return proto.Health{
			Name: "key refresh", OK: false,
			Detail: "cannot locate MakeMKV's settings.conf; set makemkv_settings_path in the config",
		}, true
	}

	rctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	res, err := makemkv.RefreshBetaKey(rctx, d.cfg.BetaKeyURL, path)
	switch {
	case err != nil:
		d.logEvent("warn", "could not refresh the MakeMKV key: "+err.Error(), nil, nil)
		return proto.Health{Name: "key refresh", OK: false, Detail: err.Error()}, true

	case !res.Changed:
		// The published key is the one already installed, so MakeMKV has not
		// issued a replacement for the expired one yet. Nothing here can fix
		// that, and saying so plainly is the only useful response.
		msg := "the published key is already installed; MakeMKV has not issued a replacement yet"
		d.logEvent("warn", "MakeMKV key refresh: "+msg, nil, nil)
		return proto.Health{Name: "key refresh", OK: false, Detail: msg}, true

	default:
		msg := fmt.Sprintf("installed the published beta key %s", res.Fetched)
		d.logEvent("info", "MakeMKV key refresh: "+msg, nil, nil)
		return proto.Health{Name: "key refresh", OK: true, Detail: msg}, true
	}
}

// ---------- health ----------

func (d *Daemon) runHealthChecks(ctx context.Context) {
	checks := make([]proto.Health, 0, 8)

	checks = append(checks, d.encoderHealth(ctx)...)

	// makemkvcon present
	if err := d.mk.Available(); err != nil {
		checks = append(checks, proto.Health{
			Name: "makemkv", OK: false, Fatal: true,
			Detail: err.Error() + " — install MakeMKV, or set makemkv_bin in the config",
		})
	} else {
		vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		version, err := d.mk.Version(vctx)
		cancel()
		if err != nil {
			checks = append(checks, proto.Health{Name: "makemkv", OK: false, Fatal: true, Detail: err.Error()})
		} else {
			checks = append(checks, proto.Health{Name: "makemkv", OK: true, Detail: "v" + version})
		}

		kctx, kcancel := context.WithTimeout(ctx, 30*time.Second)
		key, kerr := d.mk.CheckKey(kctx)
		kcancel()

		// A bad key is the one health failure the daemon can plausibly repair on
		// its own, and the one most likely to strand an unattended appliance. Try
		// once before reporting it, then re-check so the reported state reflects
		// what MakeMKV thinks after the new key is in place.
		//
		// Not when MakeMKV itself is too old, though: no key helps, and trying
		// anyway means fetching the same key every few hours for as long as the
		// problem lasts while reporting the wrong cause.
		if kerr == nil && !key.VersionTooOld && (key.Expired || !key.Present) && d.cfg.AutoRefreshKey {
			if note, attempted := d.refreshKey(ctx); attempted {
				checks = append(checks, note)
				rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
				key, kerr = d.mk.CheckKey(rctx)
				rcancel()
			}
		}

		switch {
		case kerr != nil:
			checks = append(checks, proto.Health{Name: "makemkv key", OK: false, Fatal: false, Detail: kerr.Error()})
		// None of these are fatal, because DVD ripping does not need a key.
		// Tested directly: with settings.conf holding no key at all, MakeMKV
		// ripped a title and reported "Copy complete". The specification said
		// the opposite — that MakeMKV "will not run at all" without one — and a
		// fatal check plus a refresh loop were built on that. What a key
		// actually gates is Blu-ray, which this machine has no drive for.
		//
		// Reported, but no longer fatal for anything. A key gates Blu-ray in
		// MakeMKV, and Blu-ray no longer goes through MakeMKV: libaacs decrypts
		// it and ffmpeg reads it straight from the drive. What is left of
		// MakeMKV here is the DVD scan, which a key does not gate. The check
		// stays because a machine with an expired key looks broken otherwise,
		// and an explanation is cheaper than the question.
		case key.Expired, !key.Present, key.VersionTooOld:
			checks = append(checks, proto.Health{
				Name: "makemkv key", OK: false, Fatal: false,
				Detail: "no valid registration key. Nothing here needs one: " +
					"DVD scanning works without it, and Blu-ray uses libaacs rather than MakeMKV. " +
					key.Detail,
			})
		default:
			checks = append(checks, proto.Health{Name: "makemkv key", OK: true, Detail: "accepted"})
		}
	}

	// rips directory writable
	if err := writableDir(d.cfg.RipsDir); err != nil {
		checks = append(checks, proto.Health{Name: "rips directory", OK: false, Fatal: true, Detail: err.Error()})
	} else {
		checks = append(checks, proto.Health{Name: "rips directory", OK: true, Detail: d.cfg.RipsDir})
	}

	// free space
	if free, err := freeBytes(d.cfg.RipsDir); err != nil {
		checks = append(checks, proto.Health{Name: "free space", OK: false, Detail: err.Error()})
	} else {
		// A dual-layer DVD is about 8 GB and a Blu-ray far more; below roughly
		// two discs' worth, a rip is likely to fail partway.
		const warnBelow = 20 << 30
		checks = append(checks, proto.Health{
			Name:   "free space",
			OK:     free >= warnBelow,
			Detail: humanBytes(int64(free)) + " free",
		})
	}

	// at least one drive
	n := len(d.snapshotWorkers())
	checks = append(checks, proto.Health{
		Name:   "drives",
		OK:     n > 0,
		Fatal:  false,
		Detail: fmt.Sprintf("%d detected", n),
	})

	d.mu.Lock()
	d.health = checks
	d.mu.Unlock()
}

func writableDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	probe := filepath.Join(path, ".hellbox-write-test")
	f, err := os.Create(probe)
	if err != nil {
		return fmt.Errorf("%s is not writable: %w", path, err)
	}
	f.Close()
	return os.Remove(probe)
}

func freeBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// ---------- events ----------

func (d *Daemon) logEvent(level, message string, driveID, jobID *int64) {
	// Mirrored onto the HTTP stream as well as the socket and the database.
	// Publishing here rather than at each call site means a log line added
	// anywhere reaches the web client without anyone remembering to wire it.
	d.publishEvent(api.EventLog, map[string]any{"level": level, "message": message})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.st.LogEvent(ctx, level, message, jobID, driveID); err != nil {
		fmt.Fprintf(os.Stderr, "hellbox: could not record event: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "%s %-5s %s\n", time.Now().Format("15:04:05"), strings.ToUpper(level), message)
	d.publish(proto.EventLog, map[string]any{
		"at": time.Now(), "level": level, "message": message,
	})
}
