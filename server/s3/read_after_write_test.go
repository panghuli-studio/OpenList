package s3

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/aws/aws-sdk-go/aws"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
)

const laggyDriverName = "S3LaggyLocal"

// laggyListing makes the laggy driver behave like an eventually consistent cloud drive:
// hidden names are missing from listings, zeroed names are listed with size 0.
var laggyListing = struct {
	sync.Mutex
	hidden, zeroed map[string]bool
}{hidden: map[string]bool{}, zeroed: map[string]bool{}}

func setLag(t *testing.T, hidden, zeroed []string) {
	laggyListing.Lock()
	defer laggyListing.Unlock()
	laggyListing.hidden, laggyListing.zeroed = map[string]bool{}, map[string]bool{}
	for _, n := range hidden {
		laggyListing.hidden[n] = true
	}
	for _, n := range zeroed {
		laggyListing.zeroed[n] = true
	}
	t.Cleanup(func() { setLag(&testing.T{}, nil, nil) })
}

type laggyDriver struct{ local.Local }

func (d *laggyDriver) Config() driver.Config {
	c := d.Local.Config()
	c.Name = laggyDriverName
	return c
}

// Get is not implemented, so op.Get answers from listings like drivers without a Getter.
func (d *laggyDriver) Get(context.Context, string) (model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *laggyDriver) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	objs, err := d.Local.List(ctx, dir, args)
	if err != nil {
		return nil, err
	}
	laggyListing.Lock()
	defer laggyListing.Unlock()
	res := objs[:0]
	for _, o := range objs {
		switch {
		case laggyListing.hidden[o.GetName()]:
			continue
		case laggyListing.zeroed[o.GetName()]:
			res = append(res, &model.Object{Name: o.GetName(), Path: o.GetPath(), Modified: time.Unix(0, 0)})
		default:
			res = append(res, o)
		}
	}
	return res, nil
}

func init() {
	op.RegisterDriver(func() driver.Driver { return &laggyDriver{} })
}

func TestHeadRightAfterPutWhileStorageListingLags(t *testing.T) {
	s := newE2ES3(t, laggyDriverName)
	const body = "complete.json"
	etag, err := s.put(t, "backup/complete.json", body, map[string]*string{"Color": aws.String("blue")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	setLag(t, []string{"complete.json"}, nil)
	op.Cache.ClearAll() // a List that ran during the upload refilled the cache without it

	out := s.head(t, "backup/complete.json")
	if aws.Int64Value(out.ContentLength) != int64(len(body)) || aws.StringValue(out.ETag) != etag ||
		aws.StringValue(out.Metadata["Color"]) != "blue" {
		t.Fatalf("HEAD = len %d etag %s meta %v, want len %d etag %s Color=blue",
			aws.Int64Value(out.ContentLength), aws.StringValue(out.ETag), out.Metadata, len(body), etag)
	}

	_, err = s.client.GetObject(&awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String("backup/complete.json")})
	if code := statusCode(err); code != http.StatusServiceUnavailable {
		t.Fatalf("GET while not readable: status %d (%v), want 503 so the client retries", code, err)
	}

	setLag(t, nil, nil)
	get, err := s.client.GetObject(&awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String("backup/complete.json")})
	if err != nil {
		t.Fatalf("GET once the storage lists the object: %v", err)
	}
	b, _ := io.ReadAll(get.Body)
	_ = get.Body.Close()
	if string(b) != body {
		t.Fatalf("GET body = %q", b)
	}
}

func TestHeadAndGetRightAfterPutWhileStorageReportsSizeZero(t *testing.T) {
	s := newE2ES3(t, laggyDriverName)
	const body = "archive segment"
	etag, err := s.put(t, "wal/seg", body, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	setLag(t, nil, []string{"seg"})
	op.Cache.ClearAll()

	out := s.head(t, "wal/seg")
	if aws.Int64Value(out.ContentLength) != int64(len(body)) || aws.StringValue(out.ETag) != etag {
		t.Fatalf("HEAD = len %d etag %s, want len %d etag %s",
			aws.Int64Value(out.ContentLength), aws.StringValue(out.ETag), len(body), etag)
	}
}

func TestStorageLagFallbackExpires(t *testing.T) {
	s := newE2ES3(t, laggyDriverName)
	if _, err := s.put(t, "old.txt", "x", nil, nil); err != nil {
		t.Fatal(err)
	}
	rec, err := db.GetS3ObjectMeta(s.mount + "/old.txt")
	if err != nil || rec == nil {
		t.Fatalf("record: %+v %v", rec, err)
	}
	rec.UpdatedAt = time.Now().Add(-2 * recentWriteWindow)
	if err := db.SaveS3ObjectMeta(rec); err != nil {
		t.Fatal(err)
	}
	setLag(t, []string{"old.txt"}, nil)
	op.Cache.ClearAll()
	_, err = s.client.HeadObject(&awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String("old.txt")})
	if code := statusCode(err); code != http.StatusNotFound {
		t.Fatalf("HEAD of an old write the storage does not show: status %d, want 404", code)
	}
}
