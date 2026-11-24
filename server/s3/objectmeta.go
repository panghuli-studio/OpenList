package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

// storedMetaHeaders are the non X-Amz-Meta-* headers kept with an object. Everything
// else gofakes3 hands the backend (signing headers, X-Amz-Date, the request's
// Last-Modified) describes the request, not the object.
var storedMetaHeaders = []string{
	"Cache-Control",
	"Content-Disposition",
	"Content-Encoding",
	"Content-Language",
	"Content-Type",
	"Expires",
}

func objectMetaToStore(meta map[string]string) map[string]string {
	res := make(map[string]string)
	for k, v := range meta {
		if strings.HasPrefix(k, "X-Amz-Meta-") || slices.Contains(storedMetaHeaders, k) {
			res[k] = v
		}
	}
	return res
}

func saveObjectMeta(fp string, size int64, etag string, meta map[string]string) {
	b, err := json.Marshal(objectMetaToStore(meta))
	if err != nil {
		log.Errorf("s3: encode metadata of %s: %v", fp, err)
		return
	}
	if err := db.SaveS3ObjectMeta(&model.S3ObjectMeta{Path: fp, Size: size, ETag: etag, Meta: string(b)}); err != nil {
		log.Errorf("s3: save metadata of %s: %+v", fp, err)
	}
}

func deleteObjectMeta(fp string) {
	if err := db.DeleteS3ObjectMetaTree(fp); err != nil {
		log.Errorf("s3: delete metadata of %s: %+v", fp, err)
	}
}

// loadObjectMeta returns the ETag and stored metadata for the object at fp. A record whose
// size no longer matches the storage object is stale and ignored.
func loadObjectMeta(fp string, node model.Obj) (string, map[string]string) {
	rec, err := db.GetS3ObjectMeta(fp)
	if err != nil {
		log.Errorf("s3: load metadata of %s: %+v", fp, err)
	}
	return etagAndMeta(rec, node)
}

func etagAndMeta(rec *model.S3ObjectMeta, node model.Obj) (string, map[string]string) {
	if rec == nil || rec.Size != node.GetSize() || rec.ETag == "" {
		return fallbackETag(node), nil
	}
	var meta map[string]string
	if rec.Meta != "" {
		if err := json.Unmarshal([]byte(rec.Meta), &meta); err != nil {
			log.Errorf("s3: decode metadata of %s: %v", rec.Path, err)
		}
	}
	return rec.ETag, meta
}

// fallbackETag is used for objects the S3 server did not write. It uses the storage's MD5
// when there is one. Otherwise it is derived from the storage's other hashes, or from size
// and mtime, and carries a "-1" suffix like a multipart ETag: clients such as rclone compare
// an ETag without a dash against the MD5 of the content.
func fallbackETag(node model.Obj) string {
	hi := node.GetHash()
	if m := hi.GetHash(utils.MD5); len(m) == utils.MD5.Width {
		return `"` + strings.ToLower(m) + `"`
	}
	var parts []string
	for ht, v := range hi.All() {
		if v != "" {
			parts = append(parts, ht.Name+":"+strings.ToLower(v))
		}
	}
	slices.Sort(parts)
	seed := strings.Join(parts, ",")
	if seed == "" {
		seed = fmt.Sprintf("%d:%d", node.GetSize(), node.ModTime().UnixNano())
	}
	sum := md5.Sum([]byte(seed))
	return `"` + hex.EncodeToString(sum[:]) + `-1"`
}

// recentWriteWindow bounds how long the S3 server trusts its own record of a write over a
// storage that does not show the object yet (eventually consistent listings right after an
// upload, or a directory cache that a concurrent List refilled without it).
const recentWriteWindow = 5 * time.Minute

func recentRecord(fp string) *model.S3ObjectMeta {
	rec, err := db.GetS3ObjectMeta(fp)
	if err != nil {
		log.Errorf("s3: load metadata of %s: %+v", fp, err)
		return nil
	}
	if rec == nil || time.Since(rec.UpdatedAt) > recentWriteWindow {
		return nil
	}
	return rec
}

// statObject looks up the storage object at fp. When the storage misses an object, or
// reports it empty, while the S3 server wrote it within recentWriteWindow, it refreshes the
// parent listing once. If that still does not show the written object, it also returns the
// record (rec != nil), which callers trust for size, ETag and metadata.
func (b *s3Backend) statObject(ctx context.Context, fp string) (node model.Obj, rec *model.S3ObjectMeta, err error) {
	fmeta, _ := op.GetNearestMeta(fp)
	ctx = context.WithValue(ctx, conf.MetaKey, fmeta)
	node, err = fs.Get(ctx, fp, &fs.GetArgs{})
	if err == nil && (node.IsDir() || node.GetSize() > 0) {
		return node, nil, nil
	}
	rec = recentRecord(fp)
	if rec == nil || (err == nil && rec.Size == 0) {
		return node, nil, err
	}
	if _, lerr := fs.List(ctx, path.Dir(fp), &fs.ListArgs{Refresh: true, NoLog: true}); lerr == nil {
		if n, gerr := fs.Get(ctx, fp, &fs.GetArgs{}); gerr == nil {
			if n.IsDir() || n.GetSize() == rec.Size {
				return n, nil, nil
			}
			node, err = n, nil
		}
	}
	log.Debugf("s3: storage does not show %s written at %s yet; answering from the S3 record", fp, rec.UpdatedAt)
	return node, rec, err
}
