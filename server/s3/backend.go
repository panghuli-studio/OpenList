// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/gofakes3"
	"github.com/ncw/swift/v2"
	log "github.com/sirupsen/logrus"
)

var (
	emptyPrefix = &gofakes3.Prefix{}
	timeFormat  = "Mon, 2 Jan 2006 15:04:05 GMT"
)

// s3Backend implements the gofakes3.Backend interface to make an S3
// backend for gofakes3. It also implements gofakes3.MultipartBackend so that
// multipart uploads are streamed to local temp files part-by-part and
// assembled into storage on completion, instead of being buffered in memory.
type s3Backend struct {
	listDir func(context.Context, string) ([]model.Obj, error)
	// statDir returns a directory's own entry; only used for the mtime of an empty
	// directory's marker. May be nil.
	statDir  func(context.Context, string) (model.Obj, error)
	dirLocks pathLocks
	uploads  *sync.Map // map[gofakes3.UploadID]*multipartState
}

// newBackend creates a new SimpleBucketBackend.
func newBackend() gofakes3.Backend {
	b := &s3Backend{
		uploads: new(sync.Map),
		listDir: getDirEntries,
		statDir: func(ctx context.Context, p string) (model.Obj, error) {
			return fs.Get(withMeta(ctx, p), p, &fs.GetArgs{})
		},
	}
	b.startReaper()
	return b
}

// ListBuckets always returns the default bucket.
func (b *s3Backend) ListBuckets(ctx context.Context) ([]gofakes3.BucketInfo, error) {
	buckets, err := getAndParseBuckets()
	if err != nil {
		return nil, err
	}
	var response []gofakes3.BucketInfo
	for _, b := range buckets {
		node, _ := fs.Get(ctx, b.Path, &fs.GetArgs{})
		response = append(response, gofakes3.BucketInfo{
			// Name:         gofakes3.URLEncode(b.Name),
			Name:         b.Name,
			CreationDate: gofakes3.NewContentTime(node.ModTime()),
		})
	}
	return response, nil
}

// ListBucket lists the objects in the given bucket.
func (b *s3Backend) ListBucket(ctx context.Context, bucketName string, prefix *gofakes3.Prefix, page gofakes3.ListBucketPage) (*gofakes3.ObjectList, error) {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return nil, err
	}
	bucketPath := bucket.Path

	if prefix == nil {
		prefix = emptyPrefix
	}

	// workaround
	if strings.TrimSpace(prefix.Prefix) == "" {
		prefix.HasPrefix = false
	}
	if strings.TrimSpace(prefix.Delimiter) == "" {
		prefix.HasDelimiter = false
	}

	path, remaining := prefixParser(prefix)

	response, err := b.listPage(ctx, bucketPath, path, remaining, prefix.HasDelimiter, page)
	if err == gofakes3.ErrNoSuchKey {
		// AWS just returns an empty list
		response = gofakes3.NewObjectList()
	} else if err != nil {
		return nil, err
	}

	return response, nil
}

// HeadObject returns the fileinfo for the given object name.
//
// Note that the metadata is not supported yet.
func (b *s3Backend) HeadObject(ctx context.Context, bucketName, objectName string) (*gofakes3.Object, error) {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return nil, err
	}
	bucketPath := bucket.Path

	objectName = requestKey(ctx, objectName)
	fp := path.Join(bucketPath, objectName)
	if isMarkerKey(objectName) {
		return markerObject(ctx, fp, objectName)
	}
	node, rec, err := b.statObject(ctx, fp)
	if err != nil && rec == nil {
		return nil, gofakes3.KeyNotFound(objectName)
	}
	if node != nil && node.IsDir() {
		return nil, gofakes3.KeyNotFound(objectName)
	}

	var size int64
	var etag string
	var stored map[string]string
	var modified time.Time
	if rec != nil {
		size, modified = rec.Size, rec.UpdatedAt
		etag, stored = etagAndMeta(rec, &model.Object{Size: rec.Size})
	} else {
		size, modified = node.GetSize(), node.ModTime()
		etag, stored = loadObjectMeta(fp, node)
	}

	meta := map[string]string{
		"Last-Modified": modified.UTC().Format(timeFormat),
		"Content-Type":  utils.GetMimeType(fp),
	}
	for k, v := range stored {
		meta[k] = v
	}

	return &gofakes3.Object{
		Name:     objectName,
		ETag:     etag,
		Metadata: meta,
		Size:     size,
		Contents: noOpReadCloser{},
	}, nil
}

// GetObject fetchs the object from the filesystem.
func (b *s3Backend) GetObject(ctx context.Context, bucketName, objectName string, rangeRequest *gofakes3.ObjectRangeRequest) (s3Obj *gofakes3.Object, err error) {
	defer func() { err = mapBackendError(err) }()

	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return nil, err
	}
	bucketPath := bucket.Path

	objectName = requestKey(ctx, objectName)
	fp := path.Join(bucketPath, objectName)
	if isMarkerKey(objectName) {
		obj, err := markerObject(ctx, fp, objectName)
		if err == nil {
			obj.Range, err = rangeRequest.Range(0)
		}
		return obj, err
	}
	node, rec, err := b.statObject(ctx, fp)
	if err != nil {
		if rec != nil {
			// Written moments ago but not readable from the storage yet: ask the client to
			// retry instead of claiming the object does not exist.
			return nil, gofakes3.ErrSlowDown
		}
		return nil, gofakes3.KeyNotFound(objectName)
	}

	if node.IsDir() {
		return nil, gofakes3.KeyNotFound(objectName)
	}

	link, file, err := fs.Link(ctx, fp, model.LinkArgs{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if s3Obj == nil {
			_ = link.Close()
		}
	}()

	size := link.ContentLength
	if size <= 0 {
		size = file.GetSize()
	}
	if size <= 0 && rec != nil {
		size = rec.Size
	}
	rnge, err := rangeRequest.Range(size)
	if err != nil {
		return nil, err
	}

	rrf, err := stream.GetRangeReaderFromLink(size, link)
	if err != nil {
		return nil, fmt.Errorf("the remote storage driver need to be enhanced to support s3")
	}

	var rd io.ReadCloser
	if rnge != nil {
		rd, err = rrf.RangeRead(ctx, http_range.Range(*rnge))
	} else {
		rd, err = rrf.RangeRead(ctx, http_range.Range{Length: -1})
	}
	if err != nil {
		return nil, err
	}

	var etag string
	var stored map[string]string
	if rec != nil {
		etag, stored = etagAndMeta(rec, &model.Object{Size: rec.Size})
	} else {
		etag, stored = loadObjectMeta(fp, node)
	}
	meta := map[string]string{
		"Last-Modified":       node.ModTime().UTC().Format(timeFormat),
		"Content-Disposition": utils.GenerateContentDisposition(file.GetName()),
		"Content-Type":        utils.GetMimeType(fp),
	}
	for k, v := range stored {
		meta[k] = v
	}
	closers := utils.NewClosers(rd, link)

	return &gofakes3.Object{
		// Name: gofakes3.URLEncode(objectName),
		Name:     objectName,
		ETag:     etag,
		Metadata: meta,
		Size:     size,
		Range:    rnge,
		Contents: utils.ReadCloser{Reader: rd, Closer: &closers},
	}, nil
}

// TouchObject creates or updates meta on specified object.
func (b *s3Backend) TouchObject(ctx context.Context, fp string, meta map[string]string) (result gofakes3.PutObjectResult, err error) {
	//TODO: implement
	return result, gofakes3.ErrNotImplemented
}

// PutObject creates or overwrites the object with the given name.
func (b *s3Backend) PutObject(
	ctx context.Context, bucketName, objectName string,
	meta map[string]string,
	input io.Reader, size int64,
) (result gofakes3.PutObjectResult, err error) {
	_, err = b.putStream(ctx, bucketName, objectName, meta, input, size, "")
	return result, err
}

// putStream stores the given object into the underlying storage. It is shared
// by PutObject and the multipart-upload Complete step so both paths apply the
// same directory creation, metadata and ignore rules.
//
// etag is the ETag to record for the object; when empty it is the MD5 of input.
// It returns the recorded ETag.
func (b *s3Backend) putStream(
	ctx context.Context, bucketName, objectName string,
	meta map[string]string,
	input io.Reader, size int64,
	etag string,
) (string, error) {
	err := b.putStreamInner(ctx, bucketName, objectName, meta, input, size, &etag)
	return etag, err
}

func (b *s3Backend) putStreamInner(
	ctx context.Context, bucketName, objectName string,
	meta map[string]string,
	input io.Reader, size int64,
	etag *string,
) error {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return err
	}
	bucketPath := bucket.Path

	isDir := strings.HasSuffix(objectName, "/")
	log.Debugf("isDir: %v", isDir)

	fp := path.Join(bucketPath, objectName)
	log.Debugf("fp: %s, bucketPath: %s, objectName: %s", fp, bucketPath, objectName)

	var reqPath string
	if isDir {
		reqPath = fp + "/"
	} else {
		reqPath = path.Dir(fp)
	}
	log.Debugf("reqPath: %s", reqPath)
	fmeta, _ := op.GetNearestMeta(fp)
	ctx = context.WithValue(ctx, conf.MetaKey, fmeta)

	unlock := b.dirLocks.rlockAncestors(path.Clean(reqPath), path.Clean(bucketPath))
	defer unlock()

	_, err = fs.Get(ctx, reqPath, &fs.GetArgs{})
	if err != nil {
		if errs.IsObjectNotFound(err) && strings.Contains(objectName, "/") {
			log.Debugf("reqPath: %s not found and objectName contains /, need to makeDir", reqPath)
			err = fs.MakeDir(ctx, reqPath)
			if err != nil {
				return errors.WithMessagef(err, "failed to makeDir, reqPath: %s", reqPath)
			}
		} else {
			return gofakes3.KeyNotFound(objectName)
		}
	}

	if isDir {
		// A directory marker: the directory is the object; keep its metadata.
		if size > 0 {
			log.Warnf("s3: ignoring %d byte body of directory marker %s", size, objectName)
		}
		*etag = emptyETag
		saveObjectMeta(fp+"/", 0, emptyETag, meta)
		return nil
	}

	var ti time.Time

	if val, ok := meta["X-Amz-Meta-Mtime"]; ok {
		ti, _ = swift.FloatStringToTime(val)
	}

	if val, ok := meta["mtime"]; ok {
		ti, _ = swift.FloatStringToTime(val)
	}

	// If Modified is not set, use current time
	if ti.IsZero() {
		ti = time.Now()
	}

	obj := model.Object{
		Name:     path.Base(fp),
		Size:     size,
		Modified: ti,
		Ctime:    time.Now(),
	}
	// Check if system file should be ignored
	if setting.GetBool(conf.IgnoreSystemFiles) && utils.IsSystemFile(obj.Name) {
		return errs.IgnoredSystemFile
	}
	var hasher hash.Hash
	if *etag == "" {
		hasher = md5.New()
		input = io.TeeReader(input, hasher)
	}
	stream := &stream.FileStream{
		Obj:      &obj,
		Reader:   input,
		Mimetype: meta["Content-Type"],
	}
	if stream.Mimetype == "" {
		stream.Mimetype = "application/octet-stream"
	}

	err = fs.PutDirectly(ctx, reqPath, stream)
	if err != nil {
		return err
	}

	if hasher != nil {
		// A driver that skips part of the body (e.g. an instant upload by hash) must not
		// leave a truncated MD5 behind.
		if _, err := io.Copy(io.Discard, input); err != nil {
			return err
		}
		*etag = `"` + hex.EncodeToString(hasher.Sum(nil)) + `"`
	}
	saveObjectMeta(fp, size, *etag, meta)

	return nil
}

// DeleteMulti deletes multiple objects in a single request.
func (b *s3Backend) DeleteMulti(ctx context.Context, bucketName string, objects ...string) (result gofakes3.MultiDeleteResult, rerr error) {
	for _, object := range objects {
		if err := b.deleteObject(ctx, bucketName, object); err != nil {
			log.Errorf("delete object failed: %v", err)
			result.Error = append(result.Error, gofakes3.ErrorResult{
				Code:    gofakes3.ErrInternal,
				Message: gofakes3.ErrInternal.Message(),
				Key:     object,
			})
		} else {
			result.Deleted = append(result.Deleted, gofakes3.ObjectID{
				Key: object,
			})
		}
	}

	return result, nil
}

// DeleteObject deletes the object with the given name.
func (b *s3Backend) DeleteObject(ctx context.Context, bucketName, objectName string) (result gofakes3.ObjectDeleteResult, rerr error) {
	return result, b.deleteObject(ctx, bucketName, requestKey(ctx, objectName))
}

// deleteObject deletes the object from the filesystem.
func (b *s3Backend) deleteObject(ctx context.Context, bucketName, objectName string) error {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return err
	}
	bucketPath := bucket.Path

	fp := path.Join(bucketPath, objectName)
	if path.Clean(fp) == path.Clean(bucketPath) {
		return nil
	}
	if isMarkerKey(objectName) {
		return b.deleteMarker(ctx, fp)
	}
	// S3 does not report an error when attemping to delete a key that does not exist, so
	// we need to skip IsNotExist errors.
	node, err := fs.Get(withMeta(ctx, fp), fp, &fs.GetArgs{})
	if err != nil && !errs.IsObjectNotFound(err) {
		return err
	}
	if err == nil && node.IsDir() {
		// "dir" without the slash is not an S3 key; removing the directory here would
		// delete every object below it.
		return nil
	}

	if err := fs.Remove(ctx, fp); err != nil {
		return err
	}
	deleteObjectMeta(fp)
	return nil
}

// CreateBucket creates a new bucket.
func (b *s3Backend) CreateBucket(ctx context.Context, name string) error {
	return gofakes3.ErrNotImplemented
}

// DeleteBucket deletes the bucket with the given name.
func (b *s3Backend) DeleteBucket(ctx context.Context, name string) error {
	return gofakes3.ErrNotImplemented
}

// BucketExists checks if the bucket exists.
func (b *s3Backend) BucketExists(ctx context.Context, name string) (exists bool, err error) {
	buckets, err := getAndParseBuckets()
	if err != nil {
		return false, err
	}
	for _, b := range buckets {
		if b.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// CopyObject copy specified object from srcKey to dstKey.
func (b *s3Backend) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string, meta map[string]string) (result gofakes3.CopyObjectResult, err error) {
	replace := strings.EqualFold(meta["X-Amz-Metadata-Directive"], "REPLACE")

	srcB, err := getBucketByName(srcBucket)
	if err != nil {
		return result, err
	}
	srcFp := path.Join(srcB.Path, srcKey)
	fmeta, _ := op.GetNearestMeta(srcFp)
	srcNode, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), srcFp, &fs.GetArgs{})
	if err != nil || srcNode.IsDir() {
		return result, gofakes3.KeyNotFound(srcKey)
	}
	srcETag, srcMeta := loadObjectMeta(srcFp, srcNode)

	if srcBucket == dstBucket && srcKey == dstKey {
		// Copying an object onto itself is how S3 clients change its metadata.
		if replace {
			saveObjectMeta(srcFp, srcNode.GetSize(), srcETag, meta)
		}
		return gofakes3.CopyObjectResult{
			ETag:         srcETag,
			LastModified: gofakes3.NewContentTime(time.Now()),
		}, nil
	}

	c, err := b.GetObject(ctx, srcBucket, srcKey, nil)
	if err != nil {
		return
	}
	defer func() {
		_ = c.Contents.Close()
	}()

	putMeta := make(map[string]string)
	if replace {
		for k, v := range meta {
			putMeta[k] = v
		}
	} else {
		for k, v := range srcMeta {
			putMeta[k] = v
		}
	}
	_, hasMtime := putMeta["X-Amz-Meta-Mtime"]
	if _, ok := putMeta["mtime"]; !ok && !hasMtime {
		putMeta["mtime"] = swift.TimeToFloatString(srcNode.ModTime())
	}

	etag, err := b.putStream(ctx, dstBucket, dstKey, putMeta, c.Contents, c.Size, "")
	if err != nil {
		return
	}

	return gofakes3.CopyObjectResult{
		ETag:         etag,
		LastModified: gofakes3.NewContentTime(time.Now()),
	}, nil
}
