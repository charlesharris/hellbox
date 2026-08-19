// Package store persists hellbox state in SQLite.
//
// SQLite rather than a database server: this is a single-user appliance that
// handles one disc at a time per drive and will accumulate a few thousand rows
// in its lifetime. A server process would add an operational failure mode
// without buying anything.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"hellbox/internal/proto"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"hellbox/internal/disc"
)

//go:embed schema.sql
var schemaSQL string

// schemaVersion is bumped whenever schema.sql changes in a way that needs
// migration. Phase 1 ships version 1.
const schemaVersion = 1

// Store is the database handle. The daemon is the only writer; clients read
// through the daemon's socket rather than opening this directly, which keeps
// the interface honest and leaves room for a remote client later.
type Store struct {
	db *sql.DB
}

// Open creates or opens the state database, applying the schema.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create state directory %s: %w", dir, err)
		}
	}

	// WAL keeps a reader from blocking the daemon mid-rip; busy_timeout covers
	// the brief contention when a client reads during a write.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // single writer; serialises everything through one conn

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.initVersion(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// migrate brings an existing database up to the current schema.
//
// schema.sql creates tables with IF NOT EXISTS, which does nothing for a table
// that already exists — so a column added there reaches new databases only.
// Columns added to existing ones go here.
//
// Each step is written to be safe to run against a database that already has
// it, so this needs no version bookkeeping of its own and cannot half-apply.
func (s *Store) migrate() error {
	columns := map[string]bool{}
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('discs')`)
	if err != nil {
		return fmt.Errorf("read discs columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if !columns["forgotten_after_job"] {
		if _, err := s.db.Exec(`ALTER TABLE discs ADD COLUMN forgotten_after_job INTEGER`); err != nil {
			return fmt.Errorf("add discs.forgotten_after_job: %w", err)
		}
	}
	for _, c := range []string{"read_path", "disc_name"} {
		if columns[c] {
			continue
		}
		if _, err := s.db.Exec(`ALTER TABLE discs ADD COLUMN ` + c + ` TEXT`); err != nil {
			return fmt.Errorf("add discs.%s: %w", c, err)
		}
	}
	return nil
}

// SetReadPath records which mechanism read a disc, and the disc's own name if
// it had one.
//
// Both are written after enumeration rather than after extraction, because a
// disc that cannot be extracted is exactly the one whose path is worth knowing:
// it is the BD+ disc that needs MakeMKV, and it should be countable without
// having been rippable.
func (s *Store) SetReadPath(ctx context.Context, discID int64, readPath, discName string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE discs SET read_path = ?, disc_name = COALESCE(NULLIF(?, ''), disc_name) WHERE id = ?`,
		readPath, discName, discID)
	if err != nil {
		return fmt.Errorf("record read path: %w", err)
	}
	return nil
}

// DiscName returns the disc's own name, or "" if it never had one.
func (s *Store) DiscName(ctx context.Context, discID int64) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(disc_name, '') FROM discs WHERE id = ?`, discID).Scan(&name)
	if err != nil {
		return "", fmt.Errorf("read disc name: %w", err)
	}
	return name, nil
}

// ReadPathMix counts how the collection was read, for the health view.
func (s *Store) ReadPathMix(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT read_path, COUNT(*) FROM discs WHERE read_path IS NOT NULL AND read_path != '' GROUP BY read_path`)
	if err != nil {
		return nil, fmt.Errorf("read path mix: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var path string
		var n int
		if err := rows.Scan(&path, &n); err != nil {
			return nil, err
		}
		out[path] = n
	}
	return out, rows.Err()
}

func (s *Store) initVersion() error {
	var v int
	err := s.db.QueryRow(`SELECT version FROM schema_version LIMIT 1`).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = s.db.Exec(`INSERT INTO schema_version(version) VALUES (?)`, schemaVersion)
		return err
	case err != nil:
		return fmt.Errorf("read schema version: %w", err)
	case v > schemaVersion:
		return fmt.Errorf("state database is version %d but this build understands %d", v, schemaVersion)
	}
	return nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// ---------- drives ----------

// Drive is a persisted drive record.
type Drive struct {
	ID          int64
	StableID    string
	DevicePath  string
	Label       string
	VendorModel string
	FirstSeen   time.Time
	LastSeen    time.Time
}

// UpsertDrive records a drive, returning its id. Drives are keyed on stable id
// rather than device path, because /dev/srN is reassigned when drives are added
// or reordered.
func (s *Store) UpsertDrive(ctx context.Context, stableID, devicePath, label, vendorModel string) (int64, error) {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO drives (stable_id, device_path, label, vendor_model, first_seen, last_seen)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(stable_id) DO UPDATE SET
            device_path  = excluded.device_path,
            label        = excluded.label,
            vendor_model = excluded.vendor_model,
            last_seen    = excluded.last_seen`,
		stableID, devicePath, label, vendorModel, now, now)
	if err != nil {
		return 0, fmt.Errorf("upsert drive %s: %w", stableID, err)
	}

	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM drives WHERE stable_id = ?`, stableID).Scan(&id); err != nil {
		return 0, fmt.Errorf("read drive id for %s: %w", stableID, err)
	}
	return id, nil
}

// Drives lists every drive ever seen.
func (s *Store) Drives(ctx context.Context) ([]Drive, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, stable_id, COALESCE(device_path,''), COALESCE(label,''),
               COALESCE(vendor_model,''), first_seen, last_seen
        FROM drives ORDER BY stable_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Drive
	for rows.Next() {
		var d Drive
		var first, last int64
		if err := rows.Scan(&d.ID, &d.StableID, &d.DevicePath, &d.Label, &d.VendorModel, &first, &last); err != nil {
			return nil, err
		}
		d.FirstSeen, d.LastSeen = time.Unix(first, 0), time.Unix(last, 0)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---------- discs ----------

// Event and JobSummary are wire types: the socket serves them and clients
// render them. They live in proto so a client depends on the protocol alone
// rather than on the store and the SQLite driver behind it.
type (
	Event      = proto.Event
	JobSummary = proto.JobSummary
)

// DiscRecord is a persisted disc.
type DiscRecord struct {
	ID          int64
	Fingerprint string
	VolumeLabel string
	Type        string
	TitleCount  int
	TotalBytes  int64
	FirstSeen   time.Time
	RipDir      string
}

// Ripped reports whether this disc already has a completed rip on disk.
func (d DiscRecord) Ripped() bool { return d.RipDir != "" }

// FindDisc looks a disc up by fingerprint. It returns nil when the disc has
// never been seen.
func (s *Store) FindDisc(ctx context.Context, fingerprint string) (*DiscRecord, error) {
	var d DiscRecord
	var first int64
	err := s.db.QueryRowContext(ctx, `
        SELECT id, fingerprint, COALESCE(volume_label,''), COALESCE(disc_type,''),
               title_count, total_bytes, first_seen, COALESCE(rip_dir,'')
        FROM discs WHERE fingerprint = ?`, fingerprint).
		Scan(&d.ID, &d.Fingerprint, &d.VolumeLabel, &d.Type, &d.TitleCount, &d.TotalBytes, &first, &d.RipDir)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find disc %s: %w", fingerprint, err)
	}
	d.FirstSeen = time.Unix(first, 0)
	return &d, nil
}

// SaveDisc writes a scanned disc and its titles and streams, returning the disc
// id. Re-scanning a known disc refreshes its metadata without duplicating it.
func (s *Store) SaveDisc(ctx context.Context, d disc.Disc, rawInfo string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO discs (fingerprint, volume_label, disc_type, title_count, total_bytes, first_seen, makemkv_info)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(fingerprint) DO UPDATE SET
            volume_label = excluded.volume_label,
            disc_type    = excluded.disc_type,
            title_count  = excluded.title_count,
            total_bytes  = excluded.total_bytes,
            makemkv_info = excluded.makemkv_info`,
		d.Fingerprint, d.VolumeLabel, string(d.Type), len(d.Titles), d.TotalBytes, now, rawInfo); err != nil {
		return 0, fmt.Errorf("save disc: %w", err)
	}

	var discID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM discs WHERE fingerprint = ?`, d.Fingerprint).Scan(&discID); err != nil {
		return 0, fmt.Errorf("read disc id: %w", err)
	}

	for _, t := range d.Titles {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO titles (disc_id, title_index, duration_secs, chapters, size_bytes, source_file, output_file)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(disc_id, title_index) DO UPDATE SET
                duration_secs = excluded.duration_secs,
                chapters      = excluded.chapters,
                size_bytes    = excluded.size_bytes,
                source_file   = excluded.source_file,
                output_file   = excluded.output_file`,
			discID, t.Index, t.DurationSecs, t.Chapters, t.SizeBytes, t.SourceFile, t.OutputFile); err != nil {
			return 0, fmt.Errorf("save title %d: %w", t.Index, err)
		}

		var titleID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM titles WHERE disc_id = ? AND title_index = ?`, discID, t.Index).Scan(&titleID); err != nil {
			return 0, fmt.Errorf("read title id %d: %w", t.Index, err)
		}

		for _, st := range t.Streams {
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO streams (title_id, stream_index, kind, codec, language, channels, resolution, frame_rate, flags)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(title_id, stream_index) DO UPDATE SET
                    kind       = excluded.kind,
                    codec      = excluded.codec,
                    language   = excluded.language,
                    channels   = excluded.channels,
                    resolution = excluded.resolution,
                    frame_rate = excluded.frame_rate,
                    flags      = excluded.flags`,
				titleID, st.Index, st.Kind, st.Codec, st.Language, st.Channels, st.Resolution, st.FrameRate, st.Flags); err != nil {
				return 0, fmt.Errorf("save stream %d of title %d: %w", st.Index, t.Index, err)
			}
		}
	}

	return discID, tx.Commit()
}

// SetDiscRipDir records where a disc's rip landed. A disc is considered ripped
// once this is set.
func (s *Store) SetDiscRipDir(ctx context.Context, discID int64, dir string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE discs SET rip_dir = ? WHERE id = ?`, dir, discID)
	return err
}

// ForgetDisc clears a disc's dedupe record so it will be ripped again. The
// files already on disk are left alone.
func (s *Store) ForgetDisc(ctx context.Context, fingerprint string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var discID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM discs WHERE fingerprint = ?`, fingerprint).Scan(&discID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no disc with fingerprint %s", fingerprint)
		}
		return err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE discs SET rip_dir = NULL WHERE id = ?`, discID); err != nil {
		return err
	}

	// The attempt cap has to be released too. A disc that had used up
	// max_rip_attempts stayed refused after being forgotten — "already failed N
	// times; not retrying automatically" — while the forget itself reported
	// that the disc would be ripped again. Forgetting has to mean forgetting
	// the attempts, or it does not mean anything.
	//
	// The jobs are kept and marked past rather than deleted. They carry why
	// each attempt failed, and the events explaining the failures reference
	// them; deleting the rows would throw away the record of the very problem
	// the person is retrying.
	if _, err := tx.ExecContext(ctx, `
        UPDATE discs
           SET forgotten_after_job = COALESCE(
                 (SELECT MAX(id) FROM jobs WHERE disc_id = ?), 0)
         WHERE id = ?`, discID, discID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkTitleRipped records that a title was written and verified.
func (s *Store) MarkTitleRipped(ctx context.Context, discID int64, titleIndex int, sizeBytes int64, verified bool) error {
	_, err := s.db.ExecContext(ctx, `
        UPDATE titles SET ripped = 1, verified = ?, size_bytes = ?
        WHERE disc_id = ? AND title_index = ?`,
		boolToInt(verified), sizeBytes, discID, titleIndex)
	return err
}

// ---------- jobs ----------

// JobState is a position in the rip lifecycle.
type JobState string

const (
	JobPending   JobState = "pending"
	JobScanning  JobState = "scanning"
	JobRipping   JobState = "ripping"
	JobVerifying JobState = "verifying"
	JobComplete  JobState = "complete"
	JobFailed    JobState = "failed"
	JobDuplicate JobState = "duplicate"
	JobCancelled JobState = "cancelled"
)

// Job is one rip attempt.
type Job struct {
	ID          int64
	DiscID      int64
	DriveID     int64
	State       JobState
	Attempt     int
	CreatedAt   time.Time
	StartedAt   *time.Time
	EndedAt     *time.Time
	Error       string
	TitlesTotal int
	TitlesDone  int
}

// CreateJob opens a new rip attempt.
func (s *Store) CreateJob(ctx context.Context, discID, driveID int64, attempt int) (int64, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
        INSERT INTO jobs (disc_id, drive_id, state, attempt, created_at, started_at)
        VALUES (?, ?, ?, ?, ?, ?)`,
		discID, driveID, string(JobPending), attempt, now, now)
	if err != nil {
		return 0, fmt.Errorf("create job: %w", err)
	}
	return res.LastInsertId()
}

// SetJobState moves a job to a new state, stamping the end time for terminal
// states.
func (s *Store) SetJobState(ctx context.Context, jobID int64, state JobState, errText string) error {
	terminal := state == JobComplete || state == JobFailed || state == JobDuplicate || state == JobCancelled
	if terminal {
		_, err := s.db.ExecContext(ctx,
			`UPDATE jobs SET state = ?, error = ?, ended_at = ? WHERE id = ?`,
			string(state), nullIfEmpty(errText), time.Now().Unix(), jobID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET state = ?, error = ? WHERE id = ?`,
		string(state), nullIfEmpty(errText), jobID)
	return err
}

// SetJobProgress records how many titles of the total are done.
func (s *Store) SetJobProgress(ctx context.Context, jobID int64, done, total int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET titles_done = ?, titles_total = ? WHERE id = ?`, done, total, jobID)
	return err
}

// AttemptsForDisc counts prior rip attempts, so a disc that keeps failing is
// not retried forever.
//
// Attempts made before the disc was last forgotten do not count. Forgetting a
// disc is a person saying to try it again, and a cap that outlived that would
// refuse the retry it had just promised.
//
// Cancelled jobs do not count either. A cancellation is a person stopping the
// work, or a daemon restarting mid-rip — neither says anything about whether
// the disc can be read. Counting them meant a disc stopped twice locked itself
// out with "already failed 2 times", having never actually failed once.
func (s *Store) AttemptsForDisc(ctx context.Context, discID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
        SELECT COUNT(*) FROM jobs
        WHERE disc_id = ?
          AND state <> ?
          AND id > COALESCE(
                (SELECT forgotten_after_job FROM discs WHERE id = ?), 0)`,
		discID, string(JobCancelled), discID).Scan(&n)
	return n, err
}

// ReclaimUnfinishedJobs marks jobs left mid-flight as cancelled, and returns
// how many.
//
// A rip job is only ever active while the daemon that owns it is running, so on
// startup any job still pending, scanning, ripping or verifying is by
// definition abandoned — its process is gone. Nothing else would ever move it.
//
// This matters more than tidiness. AttemptsForDisc counts every job that is not
// cancelled, so each abandoned row is a permanent phantom attempt against its
// disc, and enough of them refuse a disc that has never actually failed. Three
// such rows existed when this was written, from cancellations whose own
// bookkeeping could not be written.
//
// Cancelled rather than failed, because that is what happened: the work was
// interrupted, and nothing about the disc went wrong.
func (s *Store) ReclaimUnfinishedJobs(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `
        UPDATE jobs
           SET state = ?, ended_at = COALESCE(ended_at, ?), error = COALESCE(error, ?)
         WHERE state IN (?, ?, ?, ?)`,
		string(JobCancelled), time.Now().Unix(), "interrupted by a restart",
		string(JobPending), string(JobScanning), string(JobRipping), string(JobVerifying))
	if err != nil {
		return 0, fmt.Errorf("reclaim unfinished jobs: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RecentJobs returns the most recent jobs with their disc details.
func (s *Store) RecentJobs(ctx context.Context, limit int) ([]JobSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT j.id, j.state, j.attempt, j.created_at, j.ended_at,
               COALESCE(j.error,''), j.titles_done, j.titles_total,
               COALESCE(d.volume_label,''), d.fingerprint, COALESCE(d.rip_dir,''),
               COALESCE(dr.label,''), COALESCE(dr.stable_id,'')
        FROM jobs j
        JOIN discs  d  ON d.id  = j.disc_id
        JOIN drives dr ON dr.id = j.drive_id
        ORDER BY j.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []JobSummary
	for rows.Next() {
		var j JobSummary
		var created int64
		var ended sql.NullInt64
		if err := rows.Scan(&j.ID, &j.State, &j.Attempt, &created, &ended, &j.Error,
			&j.TitlesDone, &j.TitlesTotal, &j.VolumeLabel, &j.Fingerprint, &j.RipDir,
			&j.DriveLabel, &j.DriveStableID); err != nil {
			return nil, err
		}
		j.CreatedAt = time.Unix(created, 0)
		if ended.Valid {
			t := time.Unix(ended.Int64, 0)
			j.EndedAt = &t
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// CompletedCount is the number of discs successfully ripped, for display.
func (s *Store) CompletedCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT disc_id) FROM jobs WHERE state = ?`, string(JobComplete)).Scan(&n)
	return n, err
}

// ---------- events ----------

// LogEvent appends to the activity log.
func (s *Store) LogEvent(ctx context.Context, level, message string, jobID, driveID *int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events (ts, job_id, drive_id, level, message) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Unix(), jobID, driveID, level, message)
	return err
}

// RecentEvents returns the newest log entries first.
func (s *Store) RecentEvents(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, level, message, job_id, drive_id FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var ts int64
		var jobID, driveID sql.NullInt64
		if err := rows.Scan(&e.ID, &ts, &e.Level, &e.Message, &jobID, &driveID); err != nil {
			return nil, err
		}
		e.At = time.Unix(ts, 0)
		if jobID.Valid {
			e.JobID = &jobID.Int64
		}
		if driveID.Valid {
			e.DriveID = &driveID.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Failures returns discs that did not make it through, newest first.
//
// Cancellations are included rather than filtered out. A disc stopped by hand
// looks identical to one that failed until its reason is read, and leaving them
// out would make the list quietly incomplete — the caller groups them and can
// see at a glance which were interruptions.
func (s *Store) Failures(ctx context.Context, limit int) ([]proto.Failure, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT j.created_at, COALESCE(d.volume_label,''), d.fingerprint,
               COALESCE(j.error,''), j.attempt
        FROM jobs j
        JOIN discs d ON d.id = j.disc_id
        WHERE j.state IN (?, ?) AND COALESCE(j.error,'') <> ''
        ORDER BY j.created_at DESC
        LIMIT ?`, string(JobFailed), string(JobCancelled), limit)
	if err != nil {
		return nil, fmt.Errorf("read failures: %w", err)
	}
	defer rows.Close()

	var out []proto.Failure
	for rows.Next() {
		var f proto.Failure
		var created int64
		if err := rows.Scan(&created, &f.VolumeLabel, &f.Fingerprint, &f.Error, &f.Attempt); err != nil {
			return nil, err
		}
		f.When = time.Unix(created, 0)
		kind := disc.ClassifyFailure(f.Error)
		f.Kind, f.Advice = string(kind), disc.FailureAdvice(kind)
		out = append(out, f)
	}
	return out, rows.Err()
}
