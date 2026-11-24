package s3

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// Strict clients (the AWS CRT transfer client behind "aws s3 cp") look for UploadId under
// an InitiateMultipartUploadResult root; gofakes3 upstream encodes InitiateMultipartUpload.
func TestCreateMultipartUploadResponseRoot(t *testing.T) {
	s := newE2ES3(t, "Local")
	resp, err := http.Post(s.url+"/"+s.bucket+"/big.bin?uploads", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<InitiateMultipartUploadResult>") {
		t.Fatalf("status %d body %s, want an InitiateMultipartUploadResult root", resp.StatusCode, body)
	}
}
