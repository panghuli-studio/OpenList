package _115_open

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// fake115Put records the calls Put makes. Upload init answers status 2 (instant upload) unless
// failUpload is set; rename (ufile/update) applies 115's rule of keeping the old extension.
type fake115Put struct {
	mu         sync.Mutex
	calls      []string
	failUpload bool
}

func (f *fake115Put) RoundTrip(r *http.Request) (*http.Response, error) {
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/open/ufile/update"):
		f.calls = append(f.calls, "rename "+r.PostForm.Get("file_id")+" "+r.PostForm.Get("file_name"))
		return jsonResponse(map[string]any{"state": true, "code": 0, "data": map[string]any{"file_name": r.PostForm.Get("file_name")}}), nil
	case strings.HasSuffix(r.URL.Path, "/open/ufile/delete"):
		f.calls = append(f.calls, "delete "+r.PostForm.Get("file_ids")+" in "+r.PostForm.Get("parent_id"))
		return jsonResponse(map[string]any{"state": true, "code": 0, "data": []string{}}), nil
	case strings.HasSuffix(r.URL.Path, "/open/upload/init"):
		f.calls = append(f.calls, "upload "+r.PostForm.Get("file_name"))
		if f.failUpload {
			return jsonResponse(map[string]any{"state": false, "code": 990001, "message": "upload refused"}), nil
		}
		return jsonResponse(map[string]any{"state": true, "code": 0, "data": map[string]any{"status": 2, "file_id": "99"}}), nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
}

func putDriver(f *fake115Put) *Open115 {
	d := &Open115{parentPath: "/"}
	d.client = sdk.New(sdk.WithAccessToken("test"))
	d.client.SetHttpClient(&http.Client{Transport: f})
	return d
}

func putStream(name string, content []byte, exist model.Obj) *stream.FileStream {
	sum := sha1.Sum(content)
	fs := &stream.FileStream{
		Obj: &model.Object{
			Name:     name,
			Size:     int64(len(content)),
			HashInfo: utils.NewHashInfo(utils.SHA1, hex.EncodeToString(sum[:])),
		},
		Reader: io.NopCloser(bytes.NewReader(content)),
	}
	if exist != nil {
		fs.SetExist(exist)
	}
	return fs
}

func TestPutReplacesExistingFileByID(t *testing.T) {
	f := &fake115Put{}
	old := &Obj{Fid: "11", Pid: "10", Fn: "backup.info", Fc: "1", FS: 9}
	// op.Put hands over the existing object wrapped in its display name.
	exist := &model.ObjWrapName{Name: "backup.info", Obj: old}
	err := putDriver(f).Put(context.Background(), &Obj{Fid: "10", Fc: "0"}, putStream("backup.info", []byte("version 2"), exist), func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"rename 11 backup.openlist_to_delete_11.info",
		"upload backup.info",
		"delete 11 in 10",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	if old.Fn != "backup.info" {
		t.Fatalf("cached object renamed to %q", old.Fn)
	}
}

func TestPutRestoresExistingFileWhenUploadFails(t *testing.T) {
	f := &fake115Put{failUpload: true}
	old := &Obj{Fid: "11", Pid: "10", Fn: "noext", Fc: "1", FS: 9}
	err := putDriver(f).Put(context.Background(), &Obj{Fid: "10", Fc: "0"}, putStream("noext", []byte("version 2"), old), func(float64) {})
	if err == nil {
		t.Fatal("want the upload error")
	}
	want := []string{
		"rename 11 noext.openlist_to_delete_11",
		"upload noext",
		"rename 11 noext",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestPutNewFileTouchesNothingElse(t *testing.T) {
	f := &fake115Put{}
	err := putDriver(f).Put(context.Background(), &Obj{Fid: "10", Fc: "0"}, putStream("new.bin", []byte("x"), nil), func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "upload new.bin" {
		t.Fatalf("calls = %v", f.calls)
	}
}

func jsonResponse(v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}
