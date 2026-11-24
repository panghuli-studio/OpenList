package s3

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/gofakes3"
	log "github.com/sirupsen/logrus"
)

// Directory markers: S3 keys ending in "/" (zero bytes) map to directories. PUT "dir/"
// creates the directory, HEAD/GET "dir/" find it, an empty directory is listed as "dir/",
// and DELETE "dir/" removes the directory only while it is empty.

type trailingSlashKey struct{}

// trailingSlashHandler keeps the trailing "/" of object keys that gofakes3's router trims
// for every method but PUT, so the backend can tell "dir/" from "dir".
func trailingSlashHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodDelete:
			if _, object, ok := parseObjectPath(r.URL.Path); ok && strings.HasSuffix(object, "/") && !hasNonObjectQuery(r) {
				r = r.WithContext(context.WithValue(r.Context(), trailingSlashKey{}, true))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requestKey restores the trailing "/" that the router trimmed from objectName.
func requestKey(ctx context.Context, objectName string) string {
	if v, _ := ctx.Value(trailingSlashKey{}).(bool); v && !strings.HasSuffix(objectName, "/") {
		return objectName + "/"
	}
	return objectName
}

func isMarkerKey(objectName string) bool {
	return strings.HasSuffix(objectName, "/")
}

// markerObject returns the directory marker for fp, or KeyNotFound when fp is not a
// directory. A file "a" never answers for the key "a/".
func markerObject(ctx context.Context, fp, objectName string) (*gofakes3.Object, error) {
	node, err := fs.Get(withMeta(ctx, fp), fp, &fs.GetArgs{})
	if err != nil || !node.IsDir() {
		return nil, gofakes3.KeyNotFound(objectName)
	}
	meta := map[string]string{
		"Last-Modified": node.ModTime().UTC().Format(timeFormat),
		"Content-Type":  "application/x-directory",
	}
	etag := emptyETag
	if rec, err := db.GetS3ObjectMeta(fp + "/"); err == nil && rec != nil {
		var stored map[string]string
		etag, stored = etagAndMeta(rec, &model.Object{})
		for k, v := range stored {
			meta[k] = v
		}
	}
	return &gofakes3.Object{
		Name:     objectName,
		ETag:     etag,
		Metadata: meta,
		Size:     0,
		Contents: noOpReadCloser{},
	}, nil
}

// deleteMarker deletes the directory marker for fp: the directory goes only if it is
// empty, so objects below it are never removed, and a file of the same name is untouched.
func (b *s3Backend) deleteMarker(ctx context.Context, fp string) error {
	if err := db.DeleteS3ObjectMeta(fp + "/"); err != nil {
		log.Errorf("s3: delete metadata of %s/: %+v", fp, err)
	}
	ctx = withMeta(ctx, fp)
	node, err := fs.Get(ctx, fp, &fs.GetArgs{})
	if err != nil || !node.IsDir() {
		return nil
	}
	unlock := b.dirLocks.lock(fp)
	defer unlock()
	// Refresh: a cached listing could miss files and get a non-empty directory deleted.
	entries, err := fs.List(ctx, fp, &fs.ListArgs{Refresh: true, NoLog: true})
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return nil
	}
	return fs.Remove(ctx, fp)
}

// pathLocks serialises removing an empty directory against uploads into it: an upload
// read-locks its target directory and every ancestor, a directory removal write-locks the
// directory.
type pathLocks struct {
	mu sync.Mutex
	m  map[string]*pathLock
}

type pathLock struct {
	sync.RWMutex
	refs int
}

func (l *pathLocks) acquire(p string) *pathLock {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = make(map[string]*pathLock)
	}
	pl := l.m[p]
	if pl == nil {
		pl = &pathLock{}
		l.m[p] = pl
	}
	pl.refs++
	return pl
}

func (l *pathLocks) release(p string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if pl := l.m[p]; pl != nil {
		pl.refs--
		if pl.refs == 0 {
			delete(l.m, p)
		}
	}
}

func (l *pathLocks) lock(p string) func() {
	pl := l.acquire(p)
	pl.Lock()
	return func() {
		pl.Unlock()
		l.release(p)
	}
}

// rlockAncestors read-locks dir and its ancestors down from the root, stopping at stop.
func (l *pathLocks) rlockAncestors(dir, stop string) func() {
	var chain []string
	for p := dir; ; {
		chain = append(chain, p)
		if p == stop || p == "/" || p == "." || p == "" {
			break
		}
		p = parentDir(p)
	}
	unlocks := make([]func(), 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		p := chain[i]
		pl := l.acquire(p)
		pl.RLock()
		unlocks = append(unlocks, func() {
			pl.RUnlock()
			l.release(p)
		})
	}
	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
}

func parentDir(p string) string {
	i := strings.LastIndexByte(strings.TrimSuffix(p, "/"), '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

func withMeta(ctx context.Context, fp string) context.Context {
	m, _ := op.GetNearestMeta(fp)
	return context.WithValue(ctx, conf.MetaKey, m)
}
