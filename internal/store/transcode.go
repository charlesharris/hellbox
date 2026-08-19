package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"hellbox/internal/disc"
	"hellbox/internal/proto"
)

// Transcode job states.
const (
	TranscodePending  = "pending"
	TranscodeRunning  = "running"
	TranscodeComplete = "complete"
	TranscodeFailed   = "failed"
)

// TranscodeJob is one title waiting to be, or having been, transcoded.
type TranscodeJob struct {
	ID          int64
	DiscID      int64
	TitleIndex  int
	State       string
	Profile     string
	SourcePath  string
	OutputPath  string
	SizeBytes   int64
	Attempt     int
	Hardware    bool
	Error       string
	VolumeLabel string
}

// QueueTranscode records a title as needing transcoding.
//
// Queuing the same title again resets it to pending rather than adding a second
// job for one file. That is what makes re-queuing a disc safe: a title already
// done is simply done again, and a title that failed gets another attempt,
// without either duplicating work or leaving orphaned rows.
func (s *Store) QueueTranscode(ctx context.Context, discID int64, titleIndex int, profile, sourcePath string) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO transcodes (disc_id, title_index, state, profile, source_path, created_at)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(disc_id, title_index) DO UPDATE SET
            state       = excluded.state,
            profile     = excluded.profile,
            source_path = excluded.source_path,
            created_at  = excluded.created_at,
            started_at  = NULL,
            ended_at    = NULL,
            error       = NULL`,
		discID, titleIndex, TranscodePending, profile, sourcePath, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("queue transcode for disc %d title %d: %w", discID, titleIndex, err)
	}
	return nil
}

// RecordTranscode records a transcode that never went through the queue,
// already finished.
//
// A Blu-ray is encoded straight from the disc rather than queued, because there
// is no raw rip to queue against. That left its output in no table at all:
// invisible to the queue view, and — the part that matters — invisible to the
// reconciliation that files transcodes the library is missing. Filing a Blu-ray
// got exactly one attempt, and a library that was unwritable at that moment
// lost the disc silently.
//
// Recorded as complete because it is: the row exists to be reconciled against,
// not to be run.
func (s *Store) RecordTranscode(ctx context.Context, discID int64, titleIndex int,
	profile, sourcePath, outputPath string, sizeBytes int64, hardware bool) error {

	hw := 0
	if hardware {
		hw = 1
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO transcodes (disc_id, title_index, state, profile, source_path,
                                output_path, size_bytes, hardware, created_at, started_at, ended_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(disc_id, title_index) DO UPDATE SET
            state       = excluded.state,
            profile     = excluded.profile,
            source_path = excluded.source_path,
            output_path = excluded.output_path,
            size_bytes  = excluded.size_bytes,
            hardware    = excluded.hardware,
            ended_at    = excluded.ended_at,
            error       = NULL`,
		discID, titleIndex, TranscodeComplete, profile, sourcePath,
		outputPath, sizeBytes, hw, now, now, now)
	if err != nil {
		return fmt.Errorf("record transcode for disc %d title %d: %w", discID, titleIndex, err)
	}
	return nil
}

// NextTranscode claims the oldest pending job and marks it running.
//
// Claiming and marking happen in one transaction so two runners cannot take the
// same job. There is only one runner today, but a queue that depends on that
// staying true is a queue that breaks the first time it does not.
func (s *Store) NextTranscode(ctx context.Context, maxAttempts int) (*TranscodeJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var j TranscodeJob
	err = tx.QueryRowContext(ctx, `
        SELECT t.id, t.disc_id, t.title_index, t.profile, t.source_path, t.attempt,
               COALESCE(d.volume_label, '')
        FROM transcodes t
        JOIN discs d ON d.id = t.disc_id
        WHERE t.state = ? AND t.attempt < ?
        ORDER BY t.created_at, t.title_index
        LIMIT 1`, TranscodePending, maxAttempts).
		Scan(&j.ID, &j.DiscID, &j.TitleIndex, &j.Profile, &j.SourcePath, &j.Attempt, &j.VolumeLabel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim a transcode: %w", err)
	}

	j.Attempt++
	j.State = TranscodeRunning
	if _, err := tx.ExecContext(ctx, `
        UPDATE transcodes SET state = ?, attempt = ?, started_at = ?, error = NULL
        WHERE id = ?`, TranscodeRunning, j.Attempt, time.Now().Unix(), j.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &j, nil
}

// FinishTranscode records a completed job.
func (s *Store) FinishTranscode(ctx context.Context, id int64, outputPath string, sizeBytes int64, hardware bool) error {
	hw := 0
	if hardware {
		hw = 1
	}
	_, err := s.db.ExecContext(ctx, `
        UPDATE transcodes
           SET state = ?, output_path = ?, size_bytes = ?, hardware = ?, ended_at = ?, error = NULL
         WHERE id = ?`,
		TranscodeComplete, outputPath, sizeBytes, hw, time.Now().Unix(), id)
	return err
}

// FailTranscode records a failed job. It stays failed rather than returning to
// pending: the runner re-queues it only if the attempt budget allows, so a job
// that cannot succeed does not spin.
func (s *Store) FailTranscode(ctx context.Context, id int64, reason string) error {
	_, err := s.db.ExecContext(ctx, `
        UPDATE transcodes SET state = ?, ended_at = ?, error = ? WHERE id = ?`,
		TranscodeFailed, time.Now().Unix(), reason, id)
	return err
}

// RequeueTranscode returns a cancelled job to the queue, giving back the
// attempt it had claimed.
//
// Cancelling teaches nothing about whether a file can be encoded, so it must
// not spend part of the budget that decides whether to keep trying.
func (s *Store) RequeueTranscode(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `
        UPDATE transcodes
           SET state = ?, started_at = NULL, ended_at = NULL, attempt = MAX(attempt - 1, 0)
         WHERE id = ?`, TranscodePending, id)
	return err
}

// TranscodeQueue counts jobs waiting and jobs that have been given up on.
func (s *Store) TranscodeQueue(ctx context.Context, maxAttempts int) (pending, failed int, err error) {
	err = s.db.QueryRowContext(ctx, `
        SELECT
          (SELECT COUNT(*) FROM transcodes WHERE state = ? AND attempt < ?),
          (SELECT COUNT(*) FROM transcodes WHERE state = ? OR (state = ? AND attempt >= ?))`,
		TranscodePending, maxAttempts, TranscodeFailed, TranscodePending, maxAttempts).
		Scan(&pending, &failed)
	return pending, failed, err
}

// ReclaimRunningTranscodes returns jobs left running by a daemon that stopped
// mid-encode.
//
// Nothing else would ever move them: a running job is claimed, and the claim
// outlives the process that made it. Without this a restart during an encode
// leaves that title stuck forever while the rest of the queue drains around it.
//
// The attempt is given back too. An encode interrupted by a restart learned
// nothing about whether the file can be encoded, and counting it meant two
// restarts during one job exhausted its budget: it came to rest as pending,
// with no error, permanently unclaimable, while every other title finished
// around it.
func (s *Store) ReclaimRunningTranscodes(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `
        UPDATE transcodes
           SET state = ?, started_at = NULL, attempt = MAX(attempt - 1, 0)
         WHERE state = ?`,
		TranscodePending, TranscodeRunning)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// TitleDuration returns a title's runtime in seconds as the scan recorded it.
func (s *Store) TitleDuration(ctx context.Context, discID int64, titleIndex int) (int, error) {
	var secs int
	err := s.db.QueryRowContext(ctx,
		`SELECT duration_secs FROM titles WHERE disc_id = ? AND title_index = ?`,
		discID, titleIndex).Scan(&secs)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return secs, err
}

// RippedTitles lists the indices of a disc's titles that were written and
// verified, which are the only ones there is a file to transcode from.
func (s *Store) RippedTitles(ctx context.Context, discID int64) ([]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT title_index FROM titles WHERE disc_id = ? AND ripped = 1 ORDER BY title_index`, discID)
	if err != nil {
		return nil, fmt.Errorf("read titles for disc %d: %w", discID, err)
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// DiscWithTitles reads a disc and its titles, which is what deciding where a
// disc belongs needs: the label to name it by and the runtimes to classify it.
func (s *Store) DiscWithTitles(ctx context.Context, discID int64) (disc.Disc, error) {
	var d disc.Disc
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(volume_label,''), fingerprint FROM discs WHERE id = ?`, discID).
		Scan(&d.VolumeLabel, &d.Fingerprint)
	if err != nil {
		return d, fmt.Errorf("read disc %d: %w", discID, err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT title_index, duration_secs, size_bytes FROM titles
		 WHERE disc_id = ? AND ripped = 1 ORDER BY title_index`, discID)
	if err != nil {
		return d, fmt.Errorf("read titles for disc %d: %w", discID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var t disc.Title
		if err := rows.Scan(&t.Index, &t.DurationSecs, &t.SizeBytes); err != nil {
			return d, err
		}
		d.Titles = append(d.Titles, t)
	}
	return d, rows.Err()
}

// RecordLibraryLink notes that a title has been filed.
func (s *Store) RecordLibraryLink(ctx context.Context, discID int64, titleIndex int, path string) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO library_links (disc_id, title_index, path, created_at)
        VALUES (?, ?, ?, ?)
        ON CONFLICT(disc_id, title_index) DO UPDATE SET
            path = excluded.path, created_at = excluded.created_at`,
		discID, titleIndex, path, time.Now().Unix())
	return err
}

// LibraryLink returns where a title was filed, and whether it ever was.
func (s *Store) LibraryLink(ctx context.Context, discID int64, titleIndex int) (string, bool, error) {
	var path string
	err := s.db.QueryRowContext(ctx,
		`SELECT path FROM library_links WHERE disc_id = ? AND title_index = ?`,
		discID, titleIndex).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return path, err == nil, err
}

// Unfiled is a finished transcode that has not been put in the library.
type Unfiled struct {
	DiscID     int64
	TitleIndex int
	OutputPath string
}

// UnfiledTranscodes lists finished transcodes with no library link.
//
// Filing reacts to a transcode finishing, which leaves every transcode that
// finished before filing existed — or during a spell when it was switched off,
// or while the library was unwritable — sitting on disk with nothing pointing
// at it. Reconciling from what is recorded rather than only from what just
// happened means the library catches up by itself instead of needing every
// title encoded a second time.
func (s *Store) UnfiledTranscodes(ctx context.Context) ([]Unfiled, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT t.disc_id, t.title_index, t.output_path
        FROM transcodes t
        LEFT JOIN library_links l
               ON l.disc_id = t.disc_id AND l.title_index = t.title_index
        WHERE t.state = ? AND t.output_path IS NOT NULL AND l.id IS NULL
        ORDER BY t.disc_id, t.title_index`, TranscodeComplete)
	if err != nil {
		return nil, fmt.Errorf("read unfiled transcodes: %w", err)
	}
	defer rows.Close()

	var out []Unfiled
	for rows.Next() {
		var u Unfiled
		if err := rows.Scan(&u.DiscID, &u.TitleIndex, &u.OutputPath); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// TranscodeJobs lists the queue, newest activity first.
//
// Everything is returned, not only what is waiting: a queue view that hides
// what has finished cannot answer "did that disc actually get done", which is
// the question most often asked of it.
func (s *Store) TranscodeJobs(ctx context.Context, limit int) ([]proto.TranscodeJobSummary, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT t.id, t.disc_id, COALESCE(d.volume_label,''), t.title_index, t.state,
               t.size_bytes, t.attempt, t.hardware, COALESCE(t.error,''),
               COALESCE(t.output_path,''), CASE WHEN l.id IS NULL THEN 0 ELSE 1 END
        FROM transcodes t
        JOIN discs d ON d.id = t.disc_id
        LEFT JOIN library_links l
               ON l.disc_id = t.disc_id AND l.title_index = t.title_index
        ORDER BY
          -- Anything still to happen first: that is what a queue is for.
          CASE t.state WHEN ? THEN 0 WHEN ? THEN 1 WHEN ? THEN 2 ELSE 3 END,
          t.created_at, t.title_index
        LIMIT ?`,
		TranscodeRunning, TranscodePending, TranscodeFailed, limit)
	if err != nil {
		return nil, fmt.Errorf("read the transcode queue: %w", err)
	}
	defer rows.Close()

	var out []proto.TranscodeJobSummary
	for rows.Next() {
		var j proto.TranscodeJobSummary
		var hw, filed int
		if err := rows.Scan(&j.ID, &j.DiscID, &j.Disc, &j.TitleIndex, &j.State,
			&j.SizeBytes, &j.Attempt, &hw, &j.Error, &j.OutputPath, &filed); err != nil {
			return nil, err
		}
		j.Hardware, j.Filed = hw != 0, filed != 0
		out = append(out, j)
	}
	return out, rows.Err()
}

// TranscodeJob reads one job.
func (s *Store) TranscodeJobByID(ctx context.Context, id int64) (*TranscodeJob, error) {
	var j TranscodeJob
	err := s.db.QueryRowContext(ctx,
		`SELECT id, disc_id, title_index, state, attempt FROM transcodes WHERE id = ?`, id).
		Scan(&j.ID, &j.DiscID, &j.TitleIndex, &j.State, &j.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &j, err
}

// ResetTranscodeAttempts returns a job to pending and clears its attempts, so a
// job that has used up its budget can be tried again on request.
func (s *Store) ResetTranscodeAttempts(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `
        UPDATE transcodes
           SET state = ?, attempt = 0, started_at = NULL, ended_at = NULL, error = NULL
         WHERE id = ?`, TranscodePending, id)
	return err
}
