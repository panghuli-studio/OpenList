package s3

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

const noOverwriteDriverName = "S3NoOverwriteLocal"

// noOverwriteDriver refuses to upload over an existing name, like cloud drives whose
// upload API cannot replace a file, and declares NoOverwriteUpload as such drivers must.
type noOverwriteDriver struct{ local.Local }

func (d *noOverwriteDriver) Config() driver.Config {
	c := d.Local.Config()
	c.Name = noOverwriteDriverName
	c.NoOverwriteUpload = true
	return c
}

func (d *noOverwriteDriver) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	if _, err := os.Stat(filepath.Join(dstDir.GetPath(), file.GetName())); err == nil {
		return errs.ObjectAlreadyExists
	}
	return d.Local.Put(ctx, dstDir, file, up)
}

func init() {
	op.RegisterDriver(func() driver.Driver { return &noOverwriteDriver{} })
}

func TestPutReplacesObjectOnStorageThatCannotOverwrite(t *testing.T) {
	s := newE2ES3(t, noOverwriteDriverName)
	if _, err := s.put(t, "backup.info", "version 1", nil, nil); err != nil {
		t.Fatal(err)
	}
	etag, err := s.put(t, "backup.info", "version 2", nil, nil)
	if err != nil {
		t.Fatalf("second PUT of the same key: %v", err)
	}
	entries, err := os.ReadDir(s.localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "backup.info" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("files = %v, want only backup.info", names)
	}
	b, _ := os.ReadFile(filepath.Join(s.localRoot, "backup.info"))
	if string(b) != "version 2" {
		t.Fatalf("content = %q, want version 2", b)
	}
	op.Cache.ClearAll()
	if got := *s.head(t, "backup.info").ETag; got != etag {
		t.Fatalf("HEAD ETag = %s, want %s", got, etag)
	}
}
