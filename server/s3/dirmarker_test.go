package s3

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
)

func (s *e2eS3) keys(t *testing.T, prefix, delimiter string) (keys, prefixes []string) {
	t.Helper()
	in := &awss3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix)}
	if delimiter != "" {
		in.Delimiter = aws.String(delimiter)
	}
	out, err := s.client.ListObjectsV2(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range out.Contents {
		keys = append(keys, aws.StringValue(c.Key))
	}
	for _, p := range out.CommonPrefixes {
		prefixes = append(prefixes, aws.StringValue(p.Prefix))
	}
	return keys, prefixes
}

func TestEmptyDirectoryIsListedAsMarkerNotPlaceholder(t *testing.T) {
	s := newE2ES3(t, "Local")
	if err := os.MkdirAll(filepath.Join(s.localRoot, "a", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.localRoot, "a", "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.keys(t, "", "")
	if len(keys) != 2 || keys[0] != "a/empty/" || keys[1] != "a/f.txt" {
		t.Fatalf("keys = %v, want [a/empty/ a/f.txt]", keys)
	}
	first, err := s.client.ListObjectsV2(&awss3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String("a/empty/")})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Contents) != 1 || aws.StringValue(first.Contents[0].Key) != "a/empty/" || aws.Int64Value(first.Contents[0].Size) != 0 {
		t.Fatalf("listing the empty dir = %v, want the marker a/empty/", first.Contents)
	}
	second, _ := s.client.ListObjectsV2(&awss3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String("a/empty/")})
	if !aws.TimeValue(first.Contents[0].LastModified).Equal(aws.TimeValue(second.Contents[0].LastModified)) {
		t.Fatal("marker LastModified changes between listings")
	}
	// An empty bucket lists nothing.
	empty := newE2ES3(t, "Local")
	if keys, _ := empty.keys(t, "", ""); len(keys) != 0 {
		t.Fatalf("empty bucket keys = %v", keys)
	}
}

func TestTrailingSlashMarkerLifecycle(t *testing.T) {
	s := newE2ES3(t, "Local")
	if _, err := s.client.PutObject(&awss3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String("folder/"), Body: bytes.NewReader(nil),
		Metadata: map[string]*string{"Owner": aws.String("rclone")},
	}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(s.localRoot, "folder")); err != nil || !fi.IsDir() {
		t.Fatalf("PUT folder/ should create a directory: %v", err)
	}
	head := s.head(t, "folder/")
	if aws.Int64Value(head.ContentLength) != 0 || aws.StringValue(head.ETag) != emptyETag || aws.StringValue(head.Metadata["Owner"]) != "rclone" {
		t.Fatalf("HEAD folder/ = len %d etag %s meta %v", aws.Int64Value(head.ContentLength), aws.StringValue(head.ETag), head.Metadata)
	}
	get, err := s.client.GetObject(&awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String("folder/")})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(get.Body)
	_ = get.Body.Close()
	if len(b) != 0 {
		t.Fatalf("GET folder/ body = %q", b)
	}
	if keys, _ := s.keys(t, "", ""); len(keys) != 1 || keys[0] != "folder/" {
		t.Fatalf("keys = %v, want [folder/]", keys)
	}
	// "folder" without the slash is a different key and does not exist.
	if _, err := s.client.HeadObject(&awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String("folder")}); statusCode(err) != http.StatusNotFound {
		t.Fatalf("HEAD folder: %v, want 404", err)
	}
	if _, err := s.client.DeleteObject(&awss3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String("folder/")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.localRoot, "folder")); !os.IsNotExist(err) {
		t.Fatalf("DELETE folder/ should remove the empty directory: %v", err)
	}
}

func TestDeletingMarkerOrDirNameKeepsObjectsAndFiles(t *testing.T) {
	s := newE2ES3(t, "Local")
	if _, err := s.put(t, "d/inner.txt", "keep", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.put(t, "file", "a file", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"d/", "d", "file/"} {
		if _, err := s.client.DeleteObject(&awss3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
			t.Fatalf("DELETE %s: %v", key, err)
		}
	}
	out, err := s.client.DeleteObjects(&awss3.DeleteObjectsInput{
		Bucket: aws.String(s.bucket),
		Delete: &awss3.Delete{Objects: []*awss3.ObjectIdentifier{{Key: aws.String("d/")}, {Key: aws.String("d")}, {Key: aws.String("file/")}}},
	})
	if err != nil || len(out.Errors) != 0 {
		t.Fatalf("DeleteObjects: %v %v", err, out.Errors)
	}
	for _, f := range []string{"d/inner.txt", "file"} {
		if _, err := os.Stat(filepath.Join(s.localRoot, filepath.FromSlash(f))); err != nil {
			t.Fatalf("%s must survive deleting a marker or a directory name: %v", f, err)
		}
	}
	// HEAD/GET file/ must not resolve to the file "file".
	if _, err := s.client.HeadObject(&awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String("file/")}); statusCode(err) != http.StatusNotFound {
		t.Fatalf("HEAD file/: %v, want 404", err)
	}
	if _, err := s.client.GetObject(&awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String("file/")}); statusCode(err) != http.StatusNotFound {
		t.Fatalf("GET file/: %v, want 404", err)
	}
}

func TestDeletingLastObjectLeavesListableMarkerThatCanBeDeleted(t *testing.T) {
	s := newE2ES3(t, "Local")
	if _, err := s.put(t, "wal/0001/seg", "x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.client.DeleteObject(&awss3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String("wal/0001/seg")}); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.keys(t, "wal/", "")
	if len(keys) != 1 || keys[0] != "wal/0001/" {
		t.Fatalf("keys = %v, want the leftover directory as marker wal/0001/", keys)
	}
	// A recursive delete of everything listed (what pgBackRest does for a path) cleans up.
	if _, err := s.client.DeleteObjects(&awss3.DeleteObjectsInput{
		Bucket: aws.String(s.bucket),
		Delete: &awss3.Delete{Objects: []*awss3.ObjectIdentifier{{Key: aws.String("wal/0001/")}}},
	}); err != nil {
		t.Fatal(err)
	}
	if keys, _ := s.keys(t, "wal/", ""); len(keys) != 1 || keys[0] != "wal/" {
		t.Fatalf("keys = %v, want [wal/]", keys)
	}
}

func TestDirRemovalWaitsForUploadsBelowIt(t *testing.T) {
	var l pathLocks
	unlockUpload := l.rlockAncestors("/m/a/b", "/m")
	removed := make(chan struct{})
	go func() {
		unlock := l.lock("/m/a")
		close(removed)
		unlock()
	}()
	select {
	case <-removed:
		t.Fatal("removing /m/a did not wait for the upload into /m/a/b")
	case <-time.After(50 * time.Millisecond):
	}
	unlockUpload()
	select {
	case <-removed:
	case <-time.After(time.Second):
		t.Fatal("removal still blocked after the upload finished")
	}
	// Unrelated directories do not block each other, and locks are released.
	u1 := l.rlockAncestors("/m/x", "/m")
	u2 := l.lock("/m/y")
	u2()
	u1()
	if len(l.m) != 0 {
		t.Fatalf("leaked locks: %v", l.m)
	}
}
