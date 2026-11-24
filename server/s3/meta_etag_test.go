package s3

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
)

func md5ETag(body string) string {
	sum := md5.Sum([]byte(body))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (s *e2eS3) put(t *testing.T, key, body string, meta map[string]*string, header map[string]string) (string, error) {
	t.Helper()
	req, out := s.client.PutObjectRequest(&awss3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader([]byte(body)),
		Metadata:    meta,
		ContentType: aws.String("application/x-test"),
	})
	for k, v := range header {
		req.HTTPRequest.Header.Set(k, v)
	}
	err := req.Send()
	return aws.StringValue(out.ETag), err
}

func (s *e2eS3) head(t *testing.T, key string) *awss3.HeadObjectOutput {
	t.Helper()
	out, err := s.client.HeadObject(&awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("HeadObject %s: %v", key, err)
	}
	return out
}

func statusCode(err error) int {
	var rf awserr.RequestFailure
	if errors.As(err, &rf) {
		return rf.StatusCode()
	}
	return 0
}

func TestETagIsContentMD5AcrossOperations(t *testing.T) {
	s := newE2ES3(t, "Local")
	const body = "pgbackrest backup.info contents"
	etag, err := s.put(t, "dir/a.txt", body, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if etag != md5ETag(body) {
		t.Fatalf("PUT ETag = %s, want %s", etag, md5ETag(body))
	}
	if got := aws.StringValue(s.head(t, "dir/a.txt").ETag); got != etag {
		t.Fatalf("HEAD ETag = %q, want %q", got, etag)
	}
	get, err := s.client.GetObject(&awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String("dir/a.txt")})
	if err != nil {
		t.Fatal(err)
	}
	_ = get.Body.Close()
	if got := aws.StringValue(get.ETag); got != etag {
		t.Fatalf("GET ETag = %q, want %q", got, etag)
	}
	list, err := s.client.ListObjectsV2(&awss3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String("dir/")})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Contents) != 1 || aws.StringValue(list.Contents[0].ETag) != etag {
		t.Fatalf("list contents = %v, want one entry with ETag %s", list.Contents, etag)
	}
}

func TestMultipartETagIsKeptForHead(t *testing.T) {
	s := newE2ES3(t, "Local")
	create, err := s.client.CreateMultipartUpload(&awss3.CreateMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String("big.bin")})
	if err != nil {
		t.Fatal(err)
	}
	var parts []*awss3.CompletedPart
	for i, chunk := range []string{strings.Repeat("a", 5<<20), "tail"} {
		up, err := s.client.UploadPart(&awss3.UploadPartInput{
			Bucket: aws.String(s.bucket), Key: aws.String("big.bin"), UploadId: create.UploadId,
			PartNumber: aws.Int64(int64(i + 1)), Body: bytes.NewReader([]byte(chunk)),
		})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, &awss3.CompletedPart{ETag: up.ETag, PartNumber: aws.Int64(int64(i + 1))})
	}
	done, err := s.client.CompleteMultipartUpload(&awss3.CompleteMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String("big.bin"), UploadId: create.UploadId,
		MultipartUpload: &awss3.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(aws.StringValue(done.ETag), `-2"`) {
		t.Fatalf("complete ETag = %s, want a -2 multipart ETag", aws.StringValue(done.ETag))
	}
	if got := aws.StringValue(s.head(t, "big.bin").ETag); got != aws.StringValue(done.ETag) {
		t.Fatalf("HEAD ETag = %s, want %s", got, aws.StringValue(done.ETag))
	}
}

func TestUserMetadataSurvivesServerRestart(t *testing.T) {
	s := newE2ES3(t, "Local")
	if _, err := s.put(t, "m.txt", "x", map[string]*string{"Color": aws.String("blue")}, nil); err != nil {
		t.Fatal(err)
	}
	// A second server over the same storage stands in for a restart: nothing but the
	// database is shared between the two.
	restarted := newE2ES3OnStorage(t, s)
	out := restarted.head(t, "m.txt")
	if got := aws.StringValue(out.Metadata["Color"]); got != "blue" {
		t.Fatalf("metadata after restart = %v, want Color=blue", out.Metadata)
	}
	if got := aws.StringValue(out.ContentType); got != "application/x-test" {
		t.Fatalf("Content-Type after restart = %q", got)
	}
	if _, ok := out.Metadata["Content-Sha256"]; ok {
		t.Fatalf("request signing headers must not be stored as metadata: %v", out.Metadata)
	}
}

func TestCopyObjectMetadataDirectives(t *testing.T) {
	s := newE2ES3(t, "Local")
	etag, err := s.put(t, "src.txt", "copy me", map[string]*string{"Color": aws.String("blue")}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Default COPY directive keeps the source metadata.
	cp, err := s.client.CopyObject(&awss3.CopyObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String("dst.txt"), CopySource: aws.String(s.bucket + "/src.txt"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := aws.StringValue(cp.CopyObjectResult.ETag); got != etag {
		t.Fatalf("copy ETag = %s, want %s", got, etag)
	}
	if got := aws.StringValue(s.head(t, "dst.txt").Metadata["Color"]); got != "blue" {
		t.Fatalf("copied metadata Color = %q, want blue", got)
	}

	// Copying onto itself with REPLACE changes the metadata of an existing object.
	if _, err := s.client.CopyObject(&awss3.CopyObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String("src.txt"), CopySource: aws.String(s.bucket + "/src.txt"),
		MetadataDirective: aws.String("REPLACE"), Metadata: map[string]*string{"Color": aws.String("red")},
	}); err != nil {
		t.Fatal(err)
	}
	out := s.head(t, "src.txt")
	if got := aws.StringValue(out.Metadata["Color"]); got != "red" {
		t.Fatalf("metadata after REPLACE = %v, want Color=red", out.Metadata)
	}
	if got := aws.StringValue(out.ETag); got != etag {
		t.Fatalf("ETag changed by a metadata-only copy: %s, want %s", got, etag)
	}
}

func TestStaleRecordIgnoredWhenFileReplacedOutsideS3(t *testing.T) {
	s := newE2ES3(t, "Local")
	etag, err := s.put(t, "f.txt", "old", map[string]*string{"Color": aws.String("blue")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.localRoot, "f.txt"), []byte("replaced outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := newE2ES3OnStorage(t, s) // fresh directory cache
	out := restarted.head(t, "f.txt")
	got := aws.StringValue(out.ETag)
	if got == etag || !strings.HasSuffix(got, `-1"`) {
		t.Fatalf("ETag = %s, want a fallback (-1) ETag different from %s", got, etag)
	}
	if _, ok := out.Metadata["Color"]; ok {
		t.Fatalf("stale metadata returned: %v", out.Metadata)
	}
	if again := aws.StringValue(restarted.head(t, "f.txt").ETag); again != got {
		t.Fatalf("fallback ETag not stable: %s then %s", got, again)
	}
}

func TestConditionalRequests(t *testing.T) {
	s := newE2ES3(t, "Local")
	etag, err := s.put(t, "c.txt", "v1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	get := func(header map[string]string) error {
		req, out := s.client.GetObjectRequest(&awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String("c.txt")})
		for k, v := range header {
			req.HTTPRequest.Header.Set(k, v)
		}
		err := req.Send()
		if err == nil {
			_, _ = io.Copy(io.Discard, out.Body)
			_ = out.Body.Close()
		}
		return err
	}
	if err := get(map[string]string{"If-Match": etag}); err != nil {
		t.Fatalf("If-Match current ETag: %v", err)
	}
	if code := statusCode(get(map[string]string{"If-Match": `"0000"`})); code != http.StatusPreconditionFailed {
		t.Fatalf("If-Match stale ETag: status %d, want 412", code)
	}
	if code := statusCode(get(map[string]string{"If-None-Match": etag})); code != http.StatusNotModified {
		t.Fatalf("If-None-Match current ETag: status %d, want 304", code)
	}
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	if code := statusCode(get(map[string]string{"If-Modified-Since": future})); code != http.StatusNotModified {
		t.Fatalf("If-Modified-Since future: status %d, want 304", code)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if code := statusCode(get(map[string]string{"If-Unmodified-Since": past})); code != http.StatusPreconditionFailed {
		t.Fatalf("If-Unmodified-Since past: status %d, want 412", code)
	}

	if _, err := s.put(t, "c.txt", "clobber", nil, map[string]string{"If-None-Match": "*"}); statusCode(err) != http.StatusPreconditionFailed {
		t.Fatalf("PUT If-None-Match:* on existing object: %v, want 412", err)
	}
	etag2, err := s.put(t, "c.txt", "v2", nil, map[string]string{"If-Match": etag})
	if err != nil {
		t.Fatalf("PUT If-Match current ETag: %v", err)
	}
	if _, err := s.put(t, "c.txt", "v3", nil, map[string]string{"If-Match": etag}); statusCode(err) != http.StatusPreconditionFailed {
		t.Fatalf("PUT If-Match stale ETag: %v, want 412", err)
	}
	if _, err := s.put(t, "new.txt", "x", nil, map[string]string{"If-Match": etag2}); statusCode(err) == 0 {
		t.Fatal("PUT If-Match on a missing object must fail")
	}
	if _, err := s.put(t, "new2.txt", "x", nil, map[string]string{"If-None-Match": "*"}); err != nil {
		t.Fatalf("PUT If-None-Match:* on a missing object: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(s.localRoot, "c.txt"))
	if err != nil || string(b) != "v2" {
		t.Fatalf("c.txt = %q (%v), want v2", b, err)
	}
}
