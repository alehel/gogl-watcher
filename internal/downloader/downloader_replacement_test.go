package downloader

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
	"github.com/alehel/gogl-watcher/internal/library"
)

type replacementAPI struct {
	*gog.Mock
	failReopen   bool
	failChecksum bool
	opens        int
}

func (a *replacementAPI) OpenDownload(ctx context.Context, url string, offset int64) (*gog.Download, error) {
	a.opens++
	if a.failReopen && a.opens > 1 {
		return nil, errors.New("network unavailable")
	}
	return a.Mock.OpenDownload(ctx, url, offset)
}

func (a *replacementAPI) FetchChecksum(ctx context.Context, url string) (*gog.Checksum, error) {
	if a.failChecksum {
		return nil, errors.New("checksum unavailable")
	}
	return a.Mock.FetchChecksum(ctx, url)
}

func TestSamePathSameSizeReplacement(t *testing.T) {
	for _, tc := range []struct {
		name         string
		checksum     bool
		failReopen   bool
		failChecksum bool
		badChecksum  bool
		wantError    bool
	}{
		{name: "network failure preserves old installer", checksum: true, failReopen: true, wantError: true},
		{name: "verification failure preserves old installer", checksum: true, badChecksum: true, wantError: true},
		{name: "verified replacement", checksum: true},
		{name: "replacement without checksum"},
		{name: "replacement when checksum request fails", checksum: true, failChecksum: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, mock, paths, syncer := setup(t)
			ctx := context.Background()
			settings := db.DefaultSettings()
			settings.Platforms = []string{"windows"}
			settings.ContentChosen = true
			settings.DownloadMode = db.DownloadAll
			if err := d.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			if err := syncer.SyncAll(ctx); err != nil {
				t.Fatal(err)
			}
			files, err := d.NextPendingFiles(ctx, 1, nil)
			if err != nil || len(files) != 1 {
				t.Fatalf("pending files: %v, %v", files, err)
			}
			f := files[0]
			game, err := d.GetGame(ctx, f.GameID)
			if err != nil || game == nil {
				t.Fatalf("game: %v", err)
			}
			rel := library.LocalRelPath(game.Folder, f.RelDir, "file_"+f.GogID+".bin")
			old := bytes.Repeat([]byte("x"), int(f.Size))
			if err := os.MkdirAll(filepath.Dir(paths.Abs(rel)), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.Abs(rel), old, 0644); err != nil {
				t.Fatal(err)
			}
			if err := d.SetFileDone(ctx, f.ID, rel, f.Size); err != nil {
				t.Fatal(err)
			}
			f.Version = "new-version"
			if _, err := d.UpsertDesiredFile(ctx, f, paths.Exists); err != nil {
				t.Fatal(err)
			}
			pending, err := d.GetFile(ctx, f.ID)
			if err != nil || pending == nil {
				t.Fatalf("pending row: %v", err)
			}
			if pending.PreviousPath != rel {
				t.Fatalf("previous path = %q", pending.PreviousPath)
			}
			// The mock streams this byte pattern, distinct from the old installer.
			replacement := make([]byte, f.Size)
			for i := range replacement {
				replacement[i] = byte(int64(i) * 31 % 251)
			}
			if tc.checksum {
				mock.Checksums[f.Downlink] = fmt.Sprintf("%x", md5.Sum(replacement))
				if tc.badChecksum {
					mock.Checksums[f.Downlink] = strings.Repeat("0", 32)
				}
			}
			api := &replacementAPI{Mock: mock, failReopen: tc.failReopen, failChecksum: tc.failChecksum}
			mgr := New(d, api, paths, slog.Default())
			err = mgr.download(ctx, &transfer{file: *pending}, *game)
			if (err != nil) != tc.wantError {
				t.Fatalf("download error = %v, want error = %v", err, tc.wantError)
			}
			got, err := os.ReadFile(paths.Abs(rel))
			if err != nil {
				t.Fatal(err)
			}
			row, err := d.GetFile(ctx, f.ID)
			if err != nil || row == nil {
				t.Fatalf("result row: %v", err)
			}
			if tc.wantError {
				if !bytes.Equal(got, old) {
					t.Fatal("failed replacement changed the existing installer")
				}
				if row.Status != db.StatusPending || row.PreviousPath != rel {
					t.Fatalf("lost pending replacement: %+v", row)
				}
			} else {
				if !bytes.Equal(got, replacement) {
					t.Fatal("new version has old or incorrect bytes")
				}
				if row.Status != db.StatusDone || row.PreviousPath != "" || row.Version != f.Version {
					t.Fatalf("replacement not completed: %+v", row)
				}
			}
		})
	}
}

func TestResolvePreservesLongMultipartFilenames(t *testing.T) {
	paths := library.Paths{Root: t.TempDir()}
	prefix := "setup_" + strings.Repeat("a", 120)
	for _, name := range []string{prefix + ".exe", prefix + "-1.bin", prefix + "-2.bin"} {
		for _, source := range []string{"url or checksum", "header"} {
			t.Run(name+"/"+source, func(t *testing.T) {
				tgt := target{}
				dl := &gog.Download{}
				if source == "header" {
					dl.Filename = name
				} else {
					tgt.name = name
				}
				got := tgt.resolve(db.File{RelDir: "windows"}, db.Game{Folder: "Game"}, dl, paths)
				if got.name != name {
					t.Fatalf("filename = %q, want %q", got.name, name)
				}
				want := filepath.Join(paths.Root, "Game", "windows", name)
				if got.abs != want || got.part != want+".part" {
					t.Fatalf("incorrect destination: %+v", got)
				}
			})
		}
	}
}
