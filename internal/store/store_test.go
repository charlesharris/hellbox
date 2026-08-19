package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"hellbox/internal/disc"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleDisc() disc.Disc {
	titles := []disc.Title{
		{
			Index: 0, DurationSecs: 1748, Chapters: 6, SizeBytes: 890109157,
			SourceFile: "VTS_01_1.VOB", OutputFile: "title_00.mkv",
			Streams: []disc.Stream{
				{Index: 0, Kind: "video", Codec: "MPEG2", Resolution: "720x576", FrameRate: "25"},
				{Index: 1, Kind: "audio", Codec: "AC3", Language: "eng", Channels: 2},
			},
		},
		{
			Index: 1, DurationSecs: 1744, Chapters: 6, SizeBytes: 932509440,
			SourceFile: "VTS_02_1.VOB", OutputFile: "title_01.mkv",
		},
	}
	return disc.Disc{
		Fingerprint: disc.ComputeFingerprint("STILL_GAME_S1D1", titles),
		VolumeLabel: "STILL_GAME_S1D1",
		Type:        disc.TypeDVD,
		TotalBytes:  1822618597,
		Titles:      titles,
	}
}

func TestSaveAndFindDisc(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	d := sampleDisc()

	if got, err := s.FindDisc(ctx, d.Fingerprint); err != nil || got != nil {
		t.Fatalf("FindDisc before save = %v, %v; want nil, nil", got, err)
	}

	id, err := s.SaveDisc(ctx, d, "raw output")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}

	got, err := s.FindDisc(ctx, d.Fingerprint)
	if err != nil {
		t.Fatalf("FindDisc: %v", err)
	}
	if got == nil {
		t.Fatal("FindDisc returned nil after save")
	}
	if got.ID != id {
		t.Errorf("id = %d, want %d", got.ID, id)
	}
	if got.VolumeLabel != "STILL_GAME_S1D1" {
		t.Errorf("volume label = %q", got.VolumeLabel)
	}
	if got.TitleCount != 2 {
		t.Errorf("title count = %d, want 2", got.TitleCount)
	}
}

func TestSaveDiscIsIdempotent(t *testing.T) {
	// Re-inserting a disc rescans it. That must refresh the existing record
	// rather than creating a second one, or dedupe would break.
	ctx := context.Background()
	s := newTestStore(t)
	d := sampleDisc()

	first, err := s.SaveDisc(ctx, d, "raw")
	if err != nil {
		t.Fatalf("first SaveDisc: %v", err)
	}
	second, err := s.SaveDisc(ctx, d, "raw again")
	if err != nil {
		t.Fatalf("second SaveDisc: %v", err)
	}
	if first != second {
		t.Errorf("re-saving produced a new disc id: %d then %d", first, second)
	}
}

func TestDiscIsNotRippedUntilRipDirIsSet(t *testing.T) {
	// A disc scanned but not fully ripped must not be recognised as done, or a
	// disc whose rip was interrupted would be skipped on reinsertion.
	ctx := context.Background()
	s := newTestStore(t)
	d := sampleDisc()

	id, err := s.SaveDisc(ctx, d, "raw")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}

	rec, _ := s.FindDisc(ctx, d.Fingerprint)
	if rec.Ripped() {
		t.Error("disc reported as ripped before any rip directory was recorded")
	}

	if err := s.SetDiscRipDir(ctx, id, "/srv/media/rips/somewhere"); err != nil {
		t.Fatalf("SetDiscRipDir: %v", err)
	}
	rec, _ = s.FindDisc(ctx, d.Fingerprint)
	if !rec.Ripped() {
		t.Error("disc not reported as ripped after the rip directory was recorded")
	}
	if rec.RipDir != "/srv/media/rips/somewhere" {
		t.Errorf("rip dir = %q", rec.RipDir)
	}
}

func TestForgetDiscClearsRipDir(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	d := sampleDisc()

	id, _ := s.SaveDisc(ctx, d, "raw")
	_ = s.SetDiscRipDir(ctx, id, "/srv/media/rips/somewhere")

	if err := s.ForgetDisc(ctx, d.Fingerprint); err != nil {
		t.Fatalf("ForgetDisc: %v", err)
	}
	rec, _ := s.FindDisc(ctx, d.Fingerprint)
	if rec.Ripped() {
		t.Error("disc still reported as ripped after being forgotten")
	}

	if err := s.ForgetDisc(ctx, "nonexistent"); err == nil {
		t.Error("forgetting an unknown disc should report an error")
	}
}

func TestUpsertDriveKeepsOneRowPerStableID(t *testing.T) {
	// A drive's /dev/srN changes when drives are added or re-enumerated. The
	// stable id is the identity, so a moved drive must update, not duplicate.
	ctx := context.Background()
	s := newTestStore(t)

	first, err := s.UpsertDrive(ctx, "usb-ASUS_X-0:0", "/dev/sr0", "top", "ASUS SDRW")
	if err != nil {
		t.Fatalf("UpsertDrive: %v", err)
	}
	second, err := s.UpsertDrive(ctx, "usb-ASUS_X-0:0", "/dev/sr1", "top", "ASUS SDRW")
	if err != nil {
		t.Fatalf("UpsertDrive after move: %v", err)
	}
	if first != second {
		t.Errorf("drive id changed when the device path changed: %d then %d", first, second)
	}

	drives, err := s.Drives(ctx)
	if err != nil {
		t.Fatalf("Drives: %v", err)
	}
	if len(drives) != 1 {
		t.Fatalf("got %d drives, want 1", len(drives))
	}
	if drives[0].DevicePath != "/dev/sr1" {
		t.Errorf("device path = %q, want /dev/sr1", drives[0].DevicePath)
	}
}

func TestJobLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	d := sampleDisc()

	discID, _ := s.SaveDisc(ctx, d, "raw")
	driveID, _ := s.UpsertDrive(ctx, "usb-drive", "/dev/sr0", "top", "ASUS")

	if n, _ := s.AttemptsForDisc(ctx, discID); n != 0 {
		t.Errorf("attempts before any job = %d, want 0", n)
	}

	jobID, err := s.CreateJob(ctx, discID, driveID, 1)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if n, _ := s.AttemptsForDisc(ctx, discID); n != 1 {
		t.Errorf("attempts after one job = %d, want 1", n)
	}

	if err := s.SetJobProgress(ctx, jobID, 1, 2); err != nil {
		t.Fatalf("SetJobProgress: %v", err)
	}
	if err := s.SetJobState(ctx, jobID, JobComplete, ""); err != nil {
		t.Fatalf("SetJobState: %v", err)
	}

	jobs, err := s.RecentJobs(ctx, 10)
	if err != nil {
		t.Fatalf("RecentJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	j := jobs[0]
	if j.State != string(JobComplete) {
		t.Errorf("state = %q", j.State)
	}
	if j.EndedAt == nil {
		t.Error("a terminal state should stamp ended_at")
	}
	if j.TitlesDone != 1 || j.TitlesTotal != 2 {
		t.Errorf("progress = %d/%d, want 1/2", j.TitlesDone, j.TitlesTotal)
	}
	if j.VolumeLabel != "STILL_GAME_S1D1" {
		t.Errorf("job did not join the disc: label = %q", j.VolumeLabel)
	}

	if n, _ := s.CompletedCount(ctx); n != 1 {
		t.Errorf("completed count = %d, want 1", n)
	}
}

func TestNonTerminalStateDoesNotStampEndTime(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	discID, _ := s.SaveDisc(ctx, sampleDisc(), "raw")
	driveID, _ := s.UpsertDrive(ctx, "usb-drive", "/dev/sr0", "top", "ASUS")
	jobID, _ := s.CreateJob(ctx, discID, driveID, 1)

	if err := s.SetJobState(ctx, jobID, JobRipping, ""); err != nil {
		t.Fatalf("SetJobState: %v", err)
	}
	jobs, _ := s.RecentJobs(ctx, 1)
	if jobs[0].EndedAt != nil {
		t.Error("a job still ripping should not have an end time")
	}
}

func TestMarkTitleRipped(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	d := sampleDisc()
	discID, _ := s.SaveDisc(ctx, d, "raw")

	if err := s.MarkTitleRipped(ctx, discID, 0, 891000000, true); err != nil {
		t.Fatalf("MarkTitleRipped: %v", err)
	}
}

func TestEvents(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for _, msg := range []string{"first", "second", "third"} {
		if err := s.LogEvent(ctx, "info", msg, nil, nil); err != nil {
			t.Fatalf("LogEvent: %v", err)
		}
	}

	events, err := s.RecentEvents(ctx, 2)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Message != "third" {
		t.Errorf("newest event = %q, want %q", events[0].Message, "third")
	}
}

func TestStreamsArePersisted(t *testing.T) {
	// Phase 1 has no use for stream data. It is captured because identifying
	// what a disc contains later means reasoning over stream layouts, and the
	// disc will not be in the drive then.
	ctx := context.Background()
	s := newTestStore(t)
	d := sampleDisc()

	discID, err := s.SaveDisc(ctx, d, "raw")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}

	var n int
	err = s.db.QueryRowContext(ctx, `
        SELECT COUNT(*) FROM streams
        JOIN titles ON titles.id = streams.title_id
        WHERE titles.disc_id = ?`, discID).Scan(&n)
	if err != nil {
		t.Fatalf("count streams: %v", err)
	}
	if n != 2 {
		t.Errorf("persisted %d streams, want 2", n)
	}
}

// Forgetting a disc has to release the attempt cap. Without this the forget
// reported that the disc "will be ripped again" and the next insertion refused
// it with "already failed 2 times" — two statements from the same program
// contradicting each other, with the disc stuck between them.
func TestForgetReleasesTheAttemptCap(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	driveID, err := s.UpsertDrive(ctx, "usb-ASUS-1", "/dev/sr0", "top", "ASUS SDRW")
	if err != nil {
		t.Fatalf("UpsertDrive: %v", err)
	}
	d := disc.Disc{Fingerprint: "5d7fd5a1507a", VolumeLabel: "ROMAN_HOLIDAY"}
	discID, err := s.SaveDisc(ctx, d, "raw")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}

	for i := 1; i <= 2; i++ {
		jobID, err := s.CreateJob(ctx, discID, driveID, i)
		if err != nil {
			t.Fatalf("CreateJob: %v", err)
		}
		if err := s.SetJobState(ctx, jobID, JobFailed, "boom"); err != nil {
			t.Fatalf("SetJobState: %v", err)
		}
	}

	if n, err := s.AttemptsForDisc(ctx, discID); err != nil || n != 2 {
		t.Fatalf("AttemptsForDisc before forget = %d, %v; want 2", n, err)
	}

	if err := s.ForgetDisc(ctx, d.Fingerprint); err != nil {
		t.Fatalf("ForgetDisc: %v", err)
	}

	if n, err := s.AttemptsForDisc(ctx, discID); err != nil || n != 0 {
		t.Errorf("AttemptsForDisc after forget = %d, %v; want 0 so the disc is retried", n, err)
	}

	// The failures themselves are kept: they say why the disc needed forgetting.
	var jobs int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE disc_id = ?`, discID).Scan(&jobs); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobs != 2 {
		t.Errorf("%d jobs survived forget, want 2 — the record of what went wrong", jobs)
	}
}

// A new attempt after forgetting counts again, or the cap would never apply to
// a disc that had once been forgotten.
func TestAttemptsCountAgainAfterForget(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	driveID, _ := s.UpsertDrive(ctx, "usb-ASUS-1", "/dev/sr0", "top", "ASUS SDRW")
	d := disc.Disc{Fingerprint: "abc123", VolumeLabel: "X"}
	discID, _ := s.SaveDisc(ctx, d, "raw")

	if _, err := s.CreateJob(ctx, discID, driveID, 1); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.ForgetDisc(ctx, d.Fingerprint); err != nil {
		t.Fatalf("ForgetDisc: %v", err)
	}
	if _, err := s.CreateJob(ctx, discID, driveID, 1); err != nil {
		t.Fatalf("CreateJob after forget: %v", err)
	}

	if n, err := s.AttemptsForDisc(ctx, discID); err != nil || n != 1 {
		t.Errorf("AttemptsForDisc = %d, %v; want 1 (the attempt made since forgetting)", n, err)
	}
}

// Forgetting a disc that was never seen is an error, not a silent success.
func TestForgetUnknownDisc(t *testing.T) {
	if err := newTestStore(t).ForgetDisc(context.Background(), "nosuchfingerprint"); err == nil {
		t.Error("ForgetDisc on an unknown fingerprint returned no error")
	}
}

// Re-encoding needs to know which titles actually have a file. A title that was
// skipped or failed has nothing to transcode from, and queuing it would fail
// later, slowly, one title at a time.
func TestRippedTitlesListsOnlyWhatWasWritten(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	d := sampleDisc()
	discID, err := s.SaveDisc(ctx, d, "raw")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}

	// Two of the disc's titles written, the rest not.
	for _, idx := range []int{d.Titles[0].Index, d.Titles[1].Index} {
		if err := s.MarkTitleRipped(ctx, discID, idx, 1<<20, true); err != nil {
			t.Fatalf("MarkTitleRipped: %v", err)
		}
	}

	got, err := s.RippedTitles(ctx, discID)
	if err != nil {
		t.Fatalf("RippedTitles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("RippedTitles = %v, want the 2 titles that were written", got)
	}
	if got[0] != d.Titles[0].Index || got[1] != d.Titles[1].Index {
		t.Errorf("RippedTitles = %v, want %d and %d in order",
			got, d.Titles[0].Index, d.Titles[1].Index)
	}
}

// Re-queuing resets a finished job rather than adding a second one for the same
// file, which is what makes re-encoding a disc safe to ask for twice.
func TestQueueTranscodeResetsAnExistingJob(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	discID, err := s.SaveDisc(ctx, sampleDisc(), "raw")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}

	if err := s.QueueTranscode(ctx, discID, 0, "default", "/rips/x/title_00.mkv"); err != nil {
		t.Fatalf("QueueTranscode: %v", err)
	}
	job, err := s.NextTranscode(ctx, 2)
	if err != nil || job == nil {
		t.Fatalf("NextTranscode = %v, %v", job, err)
	}
	if err := s.FinishTranscode(ctx, job.ID, "/transcoded/x/title_00.mkv", 1<<20, true); err != nil {
		t.Fatalf("FinishTranscode: %v", err)
	}

	// Nothing pending once it is done.
	if next, _ := s.NextTranscode(ctx, 2); next != nil {
		t.Fatalf("a completed job was claimed again: %+v", next)
	}

	// Asking again re-runs it, and does not create a second row.
	if err := s.QueueTranscode(ctx, discID, 0, "default", "/rips/x/title_00.mkv"); err != nil {
		t.Fatalf("re-queue: %v", err)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM transcodes WHERE disc_id = ?`, discID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d transcode rows for one title, want 1", rows)
	}
	if next, err := s.NextTranscode(ctx, 2); err != nil || next == nil {
		t.Errorf("re-queued job was not claimable: %v, %v", next, err)
	}
}

// A cancellation says nothing about whether a disc can be read. Counting it as
// an attempt meant a disc stopped twice — once by a person, once by a daemon
// restart — locked itself out with "already failed 2 times", having never
// failed once.
func TestCancelledJobsDoNotCountAsAttempts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	driveID, err := s.UpsertDrive(ctx, "usb-ASUS-1", "/dev/sr0", "top", "ASUS SDRW")
	if err != nil {
		t.Fatalf("UpsertDrive: %v", err)
	}
	discID, err := s.SaveDisc(ctx, sampleDisc(), "raw")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}

	for _, state := range []JobState{JobCancelled, JobCancelled} {
		jobID, err := s.CreateJob(ctx, discID, driveID, 1)
		if err != nil {
			t.Fatalf("CreateJob: %v", err)
		}
		if err := s.SetJobState(ctx, jobID, state, "stopped"); err != nil {
			t.Fatalf("SetJobState: %v", err)
		}
	}

	if n, err := s.AttemptsForDisc(ctx, discID); err != nil || n != 0 {
		t.Errorf("AttemptsForDisc after two cancellations = %d, %v; want 0", n, err)
	}

	// A real failure still counts, or the cap would never apply.
	jobID, _ := s.CreateJob(ctx, discID, driveID, 1)
	if err := s.SetJobState(ctx, jobID, JobFailed, "boom"); err != nil {
		t.Fatalf("SetJobState: %v", err)
	}
	if n, err := s.AttemptsForDisc(ctx, discID); err != nil || n != 1 {
		t.Errorf("AttemptsForDisc after a real failure = %d, %v; want 1", n, err)
	}
}

// Filing reacts to a transcode finishing, which strands everything encoded
// before filing existed. Reconciling from what is recorded lets the library
// catch up rather than needing every title encoded a second time to produce a
// file that is already sitting on disk.
func TestUnfiledTranscodesFindsWhatTheLibraryLacks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	discID, err := s.SaveDisc(ctx, sampleDisc(), "raw")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}

	// Three finished transcodes, one of them already filed.
	for i := 0; i < 3; i++ {
		if err := s.QueueTranscode(ctx, discID, i, "default", "/rips/x/title.mkv"); err != nil {
			t.Fatalf("QueueTranscode: %v", err)
		}
		job, err := s.NextTranscode(ctx, 2)
		if err != nil || job == nil {
			t.Fatalf("NextTranscode: %v, %v", job, err)
		}
		if err := s.FinishTranscode(ctx, job.ID, fmt.Sprintf("/transcoded/x/title_%02d.mkv", i), 1<<20, true); err != nil {
			t.Fatalf("FinishTranscode: %v", err)
		}
	}
	if err := s.RecordLibraryLink(ctx, discID, 1, "/lib/x.mkv"); err != nil {
		t.Fatalf("RecordLibraryLink: %v", err)
	}

	// A job still running has no output and must not be offered for filing.
	if err := s.QueueTranscode(ctx, discID, 9, "default", "/rips/x/title_09.mkv"); err != nil {
		t.Fatalf("QueueTranscode: %v", err)
	}
	if _, err := s.NextTranscode(ctx, 2); err != nil {
		t.Fatalf("NextTranscode: %v", err)
	}

	got, err := s.UnfiledTranscodes(ctx)
	if err != nil {
		t.Fatalf("UnfiledTranscodes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("UnfiledTranscodes returned %d, want the 2 finished-but-unfiled: %+v", len(got), got)
	}
	for _, u := range got {
		if u.TitleIndex == 1 {
			t.Error("a title already filed was offered again")
		}
		if u.TitleIndex == 9 {
			t.Error("a title still running was offered for filing")
		}
		if u.OutputPath == "" {
			t.Error("an unfiled title came back with no output path")
		}
	}
}

// An encode interrupted by a restart learned nothing about whether the file can
// be encoded. Counting it meant two restarts during one job exhausted its
// budget: it came to rest as pending, with no error, permanently unclaimable,
// while every other title finished around it.
func TestReclaimGivesBackTheAttempt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	discID, err := s.SaveDisc(ctx, sampleDisc(), "raw")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}
	if err := s.QueueTranscode(ctx, discID, 0, "default", "/rips/x/title_00.mkv"); err != nil {
		t.Fatalf("QueueTranscode: %v", err)
	}

	// Two restarts, each while the job was claimed.
	for i := 0; i < 2; i++ {
		job, err := s.NextTranscode(ctx, 2)
		if err != nil || job == nil {
			t.Fatalf("attempt %d was not claimable: %v, %v", i+1, job, err)
		}
		if _, err := s.ReclaimRunningTranscodes(ctx); err != nil {
			t.Fatalf("Reclaim: %v", err)
		}
	}

	// It must still be claimable: nothing has actually been attempted.
	job, err := s.NextTranscode(ctx, 2)
	if err != nil {
		t.Fatalf("NextTranscode: %v", err)
	}
	if job == nil {
		t.Fatal("job is unclaimable after two restarts, having never once been tried")
	}

	// A genuine failure still spends the budget, or the cap would never apply.
	if err := s.FailTranscode(ctx, job.ID, "boom"); err != nil {
		t.Fatalf("FailTranscode: %v", err)
	}
	if err := s.RequeueTranscode(ctx, job.ID); err != nil {
		t.Fatalf("RequeueTranscode: %v", err)
	}
}

// Cancelling teaches nothing either, so it must not spend the budget that
// decides whether to keep trying.
func TestCancelGivesBackTheAttempt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	discID, _ := s.SaveDisc(ctx, sampleDisc(), "raw")
	if err := s.QueueTranscode(ctx, discID, 0, "default", "/rips/x/title_00.mkv"); err != nil {
		t.Fatalf("QueueTranscode: %v", err)
	}

	for i := 0; i < 3; i++ {
		job, err := s.NextTranscode(ctx, 2)
		if err != nil {
			t.Fatalf("NextTranscode: %v", err)
		}
		if job == nil {
			t.Fatalf("job became unclaimable after %d cancellations", i)
		}
		if err := s.RequeueTranscode(ctx, job.ID); err != nil {
			t.Fatalf("RequeueTranscode: %v", err)
		}
	}
}

// A Blu-ray never enters the transcode queue — it is encoded straight from the
// disc — so its output has to be recorded some other way or reconciliation
// cannot see it. Filing would then get exactly one attempt, which is the bug
// UnfiledTranscodes exists to prevent.
func TestRecordedTranscodesAreReconcilable(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	discID, err := s.SaveDisc(ctx, disc.Disc{
		Fingerprint: "fp-bluray", VolumeLabel: "Hackers", Type: disc.TypeBluRay,
		Titles: []disc.Title{{Index: 0, DurationSecs: 6322}},
	}, "")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}

	if err := s.RecordTranscode(ctx, discID, 0, "default",
		"bluray:/dev/sr1", "/srv/media/transcoded/hackers/title_00.mkv", 3_900_000_000, true); err != nil {
		t.Fatalf("RecordTranscode: %v", err)
	}

	got, err := s.UnfiledTranscodes(ctx)
	if err != nil {
		t.Fatalf("UnfiledTranscodes: %v", err)
	}
	if len(got) != 1 || got[0].DiscID != discID {
		t.Fatalf("reconciliation found %+v, want the recorded Blu-ray encode", got)
	}

	// And once filed it stops being pending, or reconciliation would re-file it
	// on every pass.
	if err := s.RecordLibraryLink(ctx, discID, 0, "/srv/media/library/Movies/Hackers/Hackers.mkv"); err != nil {
		t.Fatalf("RecordLibraryLink: %v", err)
	}
	got, err = s.UnfiledTranscodes(ctx)
	if err != nil {
		t.Fatalf("UnfiledTranscodes: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a filed encode is still listed as unfiled: %+v", got)
	}
}

// Three jobs were sitting pending on a live system when this was written, left
// by cancellations whose own bookkeeping could not be written. Each was a
// permanent phantom attempt against its disc, and max_rip_attempts is 2.
func TestUnfinishedJobsStopCountingAsAttempts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	discID, err := s.SaveDisc(ctx, sampleDisc(), "")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}
	driveID, err := s.UpsertDrive(ctx, "usb-1", "/dev/sr0", "top", "ASUS")
	if err != nil {
		t.Fatalf("UpsertDrive: %v", err)
	}
	for i := 1; i <= 2; i++ {
		if _, err := s.CreateJob(ctx, discID, driveID, i); err != nil {
			t.Fatalf("CreateJob: %v", err)
		}
	}

	if got, _ := s.AttemptsForDisc(ctx, discID); got != 2 {
		t.Fatalf("attempts before reclaim = %d, want 2", got)
	}

	n, err := s.ReclaimUnfinishedJobs(ctx)
	if err != nil {
		t.Fatalf("ReclaimUnfinishedJobs: %v", err)
	}
	if n != 2 {
		t.Errorf("reclaimed %d jobs, want 2", n)
	}
	if got, _ := s.AttemptsForDisc(ctx, discID); got != 0 {
		t.Errorf("attempts after reclaim = %d, want 0 — an interrupted job is not a failed one", got)
	}

	// A finished job is history and must survive untouched, or every restart
	// would erase the record of what actually happened.
	jobID, _ := s.CreateJob(ctx, discID, driveID, 3)
	if err := s.SetJobState(ctx, jobID, JobComplete, ""); err != nil {
		t.Fatalf("SetJobState: %v", err)
	}
	if n, _ := s.ReclaimUnfinishedJobs(ctx); n != 0 {
		t.Errorf("reclaim touched %d finished jobs, want 0", n)
	}
}

// The DRM mix is what lets the collection answer a question its owner cannot:
// how many discs need MakeMKV, and therefore how much stops working when a
// registration key expires.
func TestReadPathIsRecordedAndCounted(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	mk := func(label string, path, name string) {
		t.Helper()
		d := disc.Disc{
			VolumeLabel: label,
			Type:        disc.TypeBluRay,
			Titles:      []disc.Title{{Index: 0, DurationSecs: 100}},
		}
		d.Fingerprint = disc.ComputeFingerprint(label, d.Titles)
		id, err := st.SaveDisc(ctx, d, "")
		if err != nil {
			t.Fatalf("SaveDisc: %v", err)
		}
		if err := st.SetReadPath(ctx, id, path, name); err != nil {
			t.Fatalf("SetReadPath: %v", err)
		}
	}

	mk("A", "native-bluray-aacs", "")
	mk("B", "native-bluray-aacs", "")
	mk("C", "makemkv", "FIREFLY: DISC 1")
	mk("D", "native-dvd", "")

	mix, err := st.ReadPathMix(ctx)
	if err != nil {
		t.Fatalf("ReadPathMix: %v", err)
	}
	if mix["native-bluray-aacs"] != 2 {
		t.Errorf("aacs count = %d, want 2", mix["native-bluray-aacs"])
	}
	if mix["makemkv"] != 1 {
		t.Errorf("makemkv count = %d, want 1", mix["makemkv"])
	}
	if mix["native-dvd"] != 1 {
		t.Errorf("dvd count = %d, want 1", mix["native-dvd"])
	}
}

// A disc that could not be ripped is exactly the one whose path matters, so
// recording must not depend on extraction having happened.
func TestReadPathSurvivesADiscThatWasNeverRipped(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	d := disc.Disc{VolumeLabel: "FIREFLYUS_D1", Type: disc.TypeBluRay,
		Titles: []disc.Title{{Index: 0, DurationSecs: 5202}}}
	d.Fingerprint = disc.ComputeFingerprint(d.VolumeLabel, d.Titles)
	id, err := st.SaveDisc(ctx, d, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetReadPath(ctx, id, "makemkv", "FIREFLY: DISC 1"); err != nil {
		t.Fatal(err)
	}

	mix, err := st.ReadPathMix(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mix["makemkv"] != 1 {
		t.Error("an unrippable disc must still be counted")
	}
}

// An empty name must not wipe one already recorded.
func TestSetReadPathDoesNotClobberAnExistingName(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	d := disc.Disc{VolumeLabel: "X", Type: disc.TypeBluRay,
		Titles: []disc.Title{{Index: 0, DurationSecs: 10}}}
	d.Fingerprint = disc.ComputeFingerprint("X", d.Titles)
	id, _ := st.SaveDisc(ctx, d, "")

	if err := st.SetReadPath(ctx, id, "makemkv", "REAL NAME"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReadPath(ctx, id, "makemkv", ""); err != nil {
		t.Fatal(err)
	}

	name, err := st.DiscName(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if name != "REAL NAME" {
		t.Errorf("disc_name = %q, want it preserved", name)
	}
}
