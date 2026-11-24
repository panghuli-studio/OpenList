package s3

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
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
