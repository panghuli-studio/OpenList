package s3

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/aws/aws-sdk-go/aws"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
)

const failingRemoveDriverName = "S3FailingRemoveLocal"

type failingRemoveDriver struct{ local.Local }

func (d *failingRemoveDriver) Config() driver.Config {
	c := d.Local.Config()
	c.Name = failingRemoveDriverName
	return c
}

func (d *failingRemoveDriver) Remove(context.Context, model.Obj) error {
	return errors.New("remote refused delete")
}

func init() {
	op.RegisterDriver(func() driver.Driver { return &failingRemoveDriver{} })
}

func TestDeleteObjectReportsStorageFailure(t *testing.T) {
	s := newE2ES3(t, failingRemoveDriverName)
	if err := os.WriteFile(filepath.Join(s.localRoot, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.client.DeleteObject(&awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String("keep.txt"),
	}); err == nil {
		t.Fatal("DeleteObject succeeded although the storage refused the delete")
	}

	out, err := s.client.DeleteObjects(&awss3.DeleteObjectsInput{
		Bucket: aws.String(s.bucket),
		Delete: &awss3.Delete{Objects: []*awss3.ObjectIdentifier{{Key: aws.String("keep.txt")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Deleted) != 0 || len(out.Errors) != 1 || aws.StringValue(out.Errors[0].Key) != "keep.txt" {
		t.Fatalf("DeleteObjects deleted=%v errors=%v, want one error for keep.txt", out.Deleted, out.Errors)
	}
	if _, err := os.Stat(filepath.Join(s.localRoot, "keep.txt")); err != nil {
		t.Fatalf("file should still exist: %v", err)
	}
}

func TestDeleteObjectMissingKeySucceeds(t *testing.T) {
	s := newE2ES3(t, "Local")
	if _, err := s.client.DeleteObject(&awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String("never-existed.txt"),
	}); err != nil {
		t.Fatalf("deleting a missing key must succeed like S3: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.localRoot, "gone.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := s.client.DeleteObjects(&awss3.DeleteObjectsInput{
		Bucket: aws.String(s.bucket),
		Delete: &awss3.Delete{Objects: []*awss3.ObjectIdentifier{{Key: aws.String("gone.txt")}, {Key: aws.String("missing.txt")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Deleted) != 2 || len(out.Errors) != 0 {
		t.Fatalf("deleted=%v errors=%v, want both deleted", out.Deleted, out.Errors)
	}
	if _, err := os.Stat(filepath.Join(s.localRoot, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("gone.txt should be removed, stat err=%v", err)
	}
}
