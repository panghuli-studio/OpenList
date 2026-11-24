package model

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// S3ObjectMeta is what the S3 server knows about an object it wrote: the ETag it returned
// and the metadata headers the client sent. Storages cannot hold either, so without this
// they were lost on restart.
type S3ObjectMeta struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// Full OpenList path (mount path + object path).
	Path string `json:"path" gorm:"type:text"`
	// Path is TEXT, which MySQL cannot index uniquely; the hash carries the unique index.
	PathHash string `json:"-" gorm:"size:64;uniqueIndex:idx_s3_object_meta_path"`
	// Size at write time. A record whose size differs from the storage object is stale (the
	// file was replaced outside S3) and is ignored.
	Size      int64     `json:"size"`
	ETag      string    `json:"etag" gorm:"size:80"`
	Meta      string    `json:"meta" gorm:"type:text"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (m *S3ObjectMeta) BeforeSave(*gorm.DB) error {
	m.PathHash = HashS3ObjectPath(m.Path)
	return nil
}

func HashS3ObjectPath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}
