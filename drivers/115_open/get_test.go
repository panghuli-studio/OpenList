package _115_open

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"
)

// fake115 answers the two endpoints Get uses: the path lookup reports the file with
// incomplete metadata (utime 0, size 0), and the parent listing shows it only after
// listsUntilVisible calls, like 115 right after an upload.
type fake115 struct {
	listsUntilVisible int32
	lists             atomic.Int32
}

func (f *fake115) RoundTrip(r *http.Request) (*http.Response, error) {
	var data any
	switch {
	case strings.HasSuffix(r.URL.Path, "/open/folder/get_info"):
		_ = r.ParseForm()
		switch r.PostForm.Get("path") {
		case "/backup":
			data = map[string]any{"file_id": "10", "file_name": "backup", "file_category": "0", "utime": "1700000000"}
		case "/backup/backup.info":
			data = map[string]any{"file_id": "11", "file_name": "backup.info", "file_category": "1", "pick_code": "pc11", "utime": "0", "size_byte": 0}
		default:
			data = []any{}
		}
		return jsonResponse(map[string]any{"state": true, "code": 0, "data": data}), nil
	case strings.HasSuffix(r.URL.Path, "/open/ufile/files"):
		files := []any{}
		if f.lists.Add(1) > f.listsUntilVisible {
			files = append(files, map[string]any{"fid": "11", "pid": "10", "fc": "1", "fn": "backup.info", "pc": "pc11", "fs": 1234, "upt": 1700000100, "sha1": "abc"})
		}
		return jsonResponse(map[string]any{"state": true, "code": 0, "data": files, "count": len(files)}), nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
}

func jsonResponse(v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(b))),
	}
}

func newFakeDriver(t *testing.T, f *fake115) *Open115 {
	t.Helper()
	prev := parentListRetryDelays
	parentListRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { parentListRetryDelays = prev })
	d := &Open115{parentPath: "/"}
	d.RootID.RootFolderID = "0"
	d.PageSize = 200
	d.client = sdk.New(sdk.WithAccessToken("test"))
	d.client.SetHttpClient(&http.Client{Transport: f})
	return d
}

func TestGetWaitsForParentListingAfterUpload(t *testing.T) {
	f := &fake115{listsUntilVisible: 2}
	obj, err := newFakeDriver(t, f).Get(context.Background(), "/backup/backup.info")
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetSize() != 1234 || obj.ModTime().Unix() != 1700000100 {
		t.Fatalf("got size %d mtime %d, want the listed metadata", obj.GetSize(), obj.ModTime().Unix())
	}
	if n := f.lists.Load(); n != 3 {
		t.Fatalf("parent listed %d times, want 3", n)
	}
}

func TestGetReturnsLookedUpFileWhenListingNeverCatchesUp(t *testing.T) {
	f := &fake115{listsUntilVisible: 1000}
	obj, err := newFakeDriver(t, f).Get(context.Background(), "/backup/backup.info")
	if err != nil {
		t.Fatalf("a file the path lookup found must not be reported missing: %v", err)
	}
	if obj.GetID() != "11" {
		t.Fatalf("got %+v, want the looked-up file", obj)
	}
	if n := f.lists.Load(); n != int32(len(parentListRetryDelays)+1) {
		t.Fatalf("parent listed %d times, want %d", n, len(parentListRetryDelays)+1)
	}
}

// Overwriting a file through S3 or WebDAV must replace it, so op.Put has to move the old
// file away first.
func TestConfigReplacesExistingFileOnUpload(t *testing.T) {
	if !(&Open115{}).Config().NoOverwriteUpload {
		t.Fatal("115 Open must set NoOverwriteUpload")
	}
}
