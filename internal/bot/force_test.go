package bot

import (
	"testing"
	"time"

	"amdl/internal/app"
)

func TestSplitForceFlag(t *testing.T) {
	const u = "https://music.apple.com/us/album/x/1"
	for _, tc := range []struct {
		in    string
		rest  string
		force bool
	}{
		{u, u, false},
		{"-f " + u, u, true},
		{u + " --force", u, true},
		{"FORCE   " + u, u, true},
		{"  " + u + "  ", u, false},
		{"", "", false},
		{"-f", "", true},
	} {
		rest, force := splitForceFlag(tc.in)
		if rest != tc.rest || force != tc.force {
			t.Errorf("splitForceFlag(%q) = %q, %v; want %q, %v", tc.in, rest, force, tc.rest, tc.force)
		}
	}
}

// A force job must not read the cache, but it still lists the link so the quota
// and the progress message know how many tracks to expect.
func TestServeFromCacheForceIgnoresCache(t *testing.T) {
	stubPause(t, func(time.Duration) {})
	f := newFakeTG(t)
	b := newTestBot(t, f)
	b.fileIDCache.Set("1234567890", ModeALAC, audioEntry("FID1"))

	job := newJob(1, 100, 5, songURL, ModeALAC, "u")
	job.Force = true
	plan := b.serveFromCache(job, func(string) {})

	if plan.Delivered != 0 || len(plan.Skip) != 0 || plan.fullyServed() {
		t.Fatalf("force must deliver nothing from cache: %+v", plan)
	}
	if plan.Total != 1 {
		t.Fatalf("plan.Total = %d, want 1 so the quota stays accurate", plan.Total)
	}
	if n := len(f.callsTo("sendAudio")); n != 0 {
		t.Fatalf("got %d sendAudio calls, want 0", n)
	}
	if _, ok := b.fileIDCache.Get("1234567890", ModeALAC); !ok {
		t.Fatal("force must not delete the cached entry")
	}
}

// With force, a downloaded track is uploaded even when its file_id is cached.
func TestSendFilesForceUploadsDespiteCache(t *testing.T) {
	f := newFakeTG(t)
	b := newTestBot(t, f)
	b.fileIDCache.Set("555", ModeALAC, audioEntry("CACHED"))
	path := writeTrackFile(t, "01. Song.m4a", "audio")

	job := newJob(1, 100, 5, songURL, ModeALAC, "u")
	job.Force = true
	b.sendFiles(job, []app.AddedTrack{{Path: path, TrackID: "555", Artist: "Artist", Song: "Song"}})

	calls := f.callsTo("sendAudio")
	if len(calls) != 1 {
		t.Fatalf("got %d sendAudio calls, want 1", len(calls))
	}
	if calls[0].Form.Get("audio") == "CACHED" || len(calls[0].Files) != 1 {
		t.Fatalf("force must upload the file, not reuse the file_id: %+v", calls[0])
	}
}
