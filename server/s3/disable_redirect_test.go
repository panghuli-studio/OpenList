package s3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

const urlLinkDriverName = "S3URLLinkLocal"

// cdnURL is where the URL-link driver points downloads, like a cloud drive's CDN.
var cdnURL string

type urlLinkDriver struct{ local.Local }

func (d *urlLinkDriver) Config() driver.Config {
	c := d.Local.Config()
	c.Name = urlLinkDriverName
	c.NoLinkURL = false
	c.OnlyProxy = false
	return c
}

func (d *urlLinkDriver) Link(_ context.Context, file model.Obj, _ model.LinkArgs) (*model.Link, error) {
	return &model.Link{URL: cdnURL + "/" + file.GetName()}, nil
}

func init() {
	op.RegisterDriver(func() driver.Driver { return &urlLinkDriver{} })
}

func TestDisableRedirectProxiesDownloads(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "from cdn")
	}))
	t.Cleanup(cdn.Close)
	cdnURL = cdn.URL

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(s *e2eS3) *http.Response {
		t.Helper()
		resp, err := noFollow.Get(s.url + "/" + s.bucket + "/obj.bin")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	s := newE2ES3(t, urlLinkDriverName)
	if err := os.WriteFile(filepath.Join(s.localRoot, "obj.bin"), []byte("from cdn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if resp := get(s); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != cdn.URL+"/obj.bin" {
		t.Fatalf("default: status %d location %q, want 302 to the CDN", resp.StatusCode, resp.Header.Get("Location"))
	}

	prev := conf.Conf.S3.DisableRedirect
	conf.Conf.S3.DisableRedirect = true
	t.Cleanup(func() { conf.Conf.S3.DisableRedirect = prev })
	proxied := newE2ES3OnStorage(t, s)
	resp := get(proxied)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "from cdn" {
		t.Fatalf("disable_redirect: status %d body %q, want 200 with the object proxied", resp.StatusCode, body)
	}
}
