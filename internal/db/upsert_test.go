package db

import "testing"

// planUpsert decides what happens to a stored row when GOG offers the file
// again. These are the cases the downloader and the sync depend on.
func TestPlanUpsert(t *testing.T) {
	const local = "Game/windows/setup.exe"
	done := File{Active: true, Status: StatusDone, LocalPath: local, Size: 900, ManifestSize: 1000, Version: "1.0"}
	next := File{Size: 1000, Version: "1.0"}

	cases := []struct {
		name            string
		stored, next    File
		changed, onDisk bool
		want            fileUpdate
	}{
		{
			name:   "downloaded and unchanged stays done, keeping the measured size",
			stored: done, next: next, onDisk: true,
			want: fileUpdate{status: StatusDone, size: 900},
		},
		{
			name:   "a new build is fetched again and the old copy waits to be replaced",
			stored: done, next: File{Size: 1200, Version: "1.1"}, changed: true, onDisk: true,
			want: fileUpdate{status: StatusPending, prev: local, size: 1200, updated: true},
		},
		{
			name:   "a file that vanished from disk is queued again",
			stored: done, next: next, onDisk: false,
			want: fileUpdate{status: StatusPending, size: 900},
		},
		{
			name:   "a failed attempt at an old build is superseded without keeping a copy",
			stored: File{Active: true, Status: StatusError, Version: "1.0"}, next: File{Size: 1200, Version: "1.1"}, changed: true,
			want: fileUpdate{status: StatusPending, size: 1200, updated: true},
		},
		{
			name:   "a pending file is left alone",
			stored: File{Active: true, Status: StatusPending, Version: "1.0"}, next: next,
			want: fileUpdate{status: StatusPending, size: 1000},
		},
		{
			name:   "re-selecting a game keeps the copy that is still on disk",
			stored: File{Status: StatusInactive, LocalPath: local, Size: 900, ManifestSize: 1000, Version: "1.0"}, next: next, onDisk: true,
			want: fileUpdate{status: StatusDone, size: 900},
		},
		{
			name:   "re-selecting replaces a copy that has been superseded meanwhile",
			stored: File{Status: StatusInactive, LocalPath: local, Size: 900, ManifestSize: 1000, Version: "1.0"},
			next:   File{Size: 1200, Version: "1.1"}, changed: true, onDisk: true,
			want: fileUpdate{status: StatusPending, prev: local, size: 1200, updated: true},
		},
		{
			name:   "re-selecting with nothing on disk downloads again",
			stored: File{Status: StatusInactive, LocalPath: local, Version: "1.0"}, next: next,
			want: fileUpdate{status: StatusPending, size: 1000},
		},
		{
			name:   "a kept older build is not mistaken for this version",
			stored: File{Status: StatusInactive, LocalPath: local, PreviousPath: "Game/windows/setup_1.0.exe", Version: "1.1"},
			next:   File{Size: 1000, Version: "1.1"}, onDisk: true,
			want: fileUpdate{status: StatusPending, prev: "Game/windows/setup_1.0.exe", size: 1000},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := planUpsert(c.stored, c.next, c.changed, c.onDisk); got != c.want {
				t.Errorf("planUpsert() = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestMetadataChange(t *testing.T) {
	cases := []struct {
		name         string
		stored, next File
		want         bool
	}{
		{"same version and size", File{Version: "1.0", ManifestSize: 1000}, File{Version: "1.0", Size: 1000}, false},
		{"new version", File{Version: "1.0", ManifestSize: 1000}, File{Version: "1.1", Size: 1000}, true},
		{"new manifest size", File{Version: "1.0", ManifestSize: 1000}, File{Version: "1.0", Size: 1200}, true},
		{"unknown stored size never counts", File{Version: "1.0", ManifestSize: 0}, File{Version: "1.0", Size: 1200}, false},
		{"unknown incoming size never counts", File{Version: "1.0", ManifestSize: 1000}, File{Version: "1.0", Size: 0}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := metadataChange(c.stored, c.next); got != c.want {
				t.Errorf("metadataChange() = %v, want %v", got, c.want)
			}
		})
	}
}
