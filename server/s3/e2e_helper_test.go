package s3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
	"gorm.io/gorm"
)

var e2eMounts int

// e2eS3 is a real S3 server (NewServer) over one storage, driven by the AWS SDK.
type e2eS3 struct {
	client    *awss3.S3
	bucket    string
	mount     string
	localRoot string
	url       string
}

// newE2ES3 mounts a storage of the given driver (Local or a test wrapper around it) at a
// per-test path, points bucket "e2e" at it and starts NewServer without auth.
func newE2ES3(t *testing.T, driverName string) *e2eS3 {
	t.Helper()
	ctx := context.Background()
	localRoot := t.TempDir()
	addition, err := json.Marshal(struct {
		RootFolderPath string `json:"root_folder_path"`
	}{RootFolderPath: localRoot})
	if err != nil {
		t.Fatal(err)
	}
	e2eMounts++
	mount := fmt.Sprintf("/%s-%d", sanitizeTestName(t.Name()), e2eMounts)
	storageID, err := op.CreateStorage(ctx, model.Storage{
		Driver:    driverName,
		MountPath: mount,
		Addition:  string(addition),
	})
	if err != nil {
		t.Fatalf("create storage: %+v", err)
	}
	t.Cleanup(func() { _ = op.DeleteStorageById(ctx, storageID) })

	const bucket = "e2e"
	previous, previousErr := op.GetSettingItemByKey(conf.S3Buckets)
	if previousErr != nil && !errors.Is(previousErr, gorm.ErrRecordNotFound) {
		t.Fatal(previousErr)
	}
	if err := op.SaveSettingItem(&model.SettingItem{
		Key:   conf.S3Buckets,
		Value: `[{"name":"` + bucket + `","path":"` + mount + `"}]`,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if previousErr == nil {
			_ = op.SaveSettingItem(previous)
			return
		}
		_ = db.DeleteSettingItemByKey(conf.S3Buckets)
		op.SettingCacheUpdate()
	})

	h, err := NewServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	sess, err := session.NewSession(&aws.Config{
		Region:           aws.String("us-east-1"),
		Endpoint:         aws.String(srv.URL),
		S3ForcePathStyle: aws.Bool(true),
		Credentials:      credentials.AnonymousCredentials,
		MaxRetries:       aws.Int(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &e2eS3{client: awss3.New(sess), bucket: bucket, mount: mount, localRoot: localRoot, url: srv.URL}
}

// newE2ES3OnStorage starts another server over the storage and bucket of s, as a restart
// would: only the database and the files are shared.
func newE2ES3OnStorage(t *testing.T, s *e2eS3) *e2eS3 {
	t.Helper()
	op.Cache.ClearAll()
	h, err := NewServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	sess, err := session.NewSession(&aws.Config{
		Region:           aws.String("us-east-1"),
		Endpoint:         aws.String(srv.URL),
		S3ForcePathStyle: aws.Bool(true),
		Credentials:      credentials.AnonymousCredentials,
		MaxRetries:       aws.Int(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &e2eS3{client: awss3.New(sess), bucket: s.bucket, mount: s.mount, localRoot: s.localRoot, url: srv.URL}
}
