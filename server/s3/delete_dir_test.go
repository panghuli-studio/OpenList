package s3

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
)

func TestDeleteObjectKeepsDirectory(t *testing.T) {
	s := newE2ES3(t, "Local")
	file := filepath.Join(s.localRoot, "dir", "sub", "file.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"dir", "dir/", "dir/sub", "dir/sub/"} {
		if _, err := s.client.DeleteObject(&awss3.DeleteObjectInput{
			Bucket: aws.String(s.bucket), Key: aws.String(key),
		}); err != nil {
			t.Fatalf("DeleteObject %q: %v", key, err)
		}
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("DeleteObject %q removed the directory: %v", key, err)
		}
	}

	out, err := s.client.DeleteObjects(&awss3.DeleteObjectsInput{
		Bucket: aws.String(s.bucket),
		Delete: &awss3.Delete{Objects: []*awss3.ObjectIdentifier{{Key: aws.String("dir")}, {Key: aws.String("dir/")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Errors) != 0 {
		t.Fatalf("DeleteObjects errors=%v", out.Errors)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("DeleteObjects removed the directory: %v", err)
	}

	if _, err := s.client.DeleteObject(&awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String("dir/sub/file.txt"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("file should be removed, stat err=%v", err)
	}
}
