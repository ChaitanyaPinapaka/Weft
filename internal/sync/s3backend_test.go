package sync

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeS3 is a minimal in-process S3-compatible store (path-style) that the real
// aws-sdk-go-v2 client talks to over HTTP. It implements exactly the verbs the
// backend uses — enough to validate SDK signing/addressing without a real bucket.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeS3() *fakeS3 { return &fakeS3{objects: map[string][]byte{}} }

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Path-style: /{bucket}/{key...}
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	slash := strings.IndexByte(trimmed, '/')
	key := ""
	if slash >= 0 {
		key = trimmed[slash+1:]
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// ListObjectsV2: GET on the bucket with ?list-type=2
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		prefix := r.URL.Query().Get("prefix")
		type obj struct {
			Key string `xml:"Key"`
		}
		type result struct {
			XMLName     xml.Name `xml:"ListBucketResult"`
			IsTruncated bool     `xml:"IsTruncated"`
			Contents    []obj    `xml:"Contents"`
		}
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		res := result{}
		for _, k := range keys {
			res.Contents = append(res.Contents, obj{Key: k})
		}
		w.Header().Set("Content-Type", "application/xml")
		xml.NewEncoder(w).Encode(res)
		return
	}

	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = body
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			f.notFound(w)
			return
		}
		w.Write(data)
	case http.MethodHead:
		if _, ok := f.objects[key]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3) notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, `<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`)
}

// backendConformance is the contract every Backend must satisfy.
func backendConformance(t *testing.T, be Backend) {
	t.Helper()
	if _, err := be.Get("missing"); err != ErrNotFound {
		t.Fatalf("Get missing want ErrNotFound, got %v", err)
	}
	if ok, _ := be.Head("missing"); ok {
		t.Fatal("Head missing should be false")
	}
	if err := be.Put("a/x", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if data, err := be.Get("a/x"); err != nil || string(data) != "hello" {
		t.Fatalf("Get after Put: %v %q", err, data)
	}
	if ok, _ := be.Head("a/x"); !ok {
		t.Fatal("Head should be true after Put")
	}
	// PutIfAbsent: first writes, second is a no-op.
	if wrote, _ := be.PutIfAbsent("a/y", []byte("1")); !wrote {
		t.Fatal("PutIfAbsent should write a new key")
	}
	if wrote, _ := be.PutIfAbsent("a/y", []byte("2")); wrote {
		t.Fatal("PutIfAbsent should not overwrite")
	}
	if data, _ := be.Get("a/y"); string(data) != "1" {
		t.Fatalf("PutIfAbsent overwrote: %q", data)
	}
	be.Put("a/z", []byte("z"))
	be.Put("b/w", []byte("w"))
	keys, err := be.List("a/")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "a/x,a/y,a/z" {
		t.Fatalf("List(a/) = %v", keys)
	}
	if err := be.Delete("a/x"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := be.Head("a/x"); ok {
		t.Fatal("Head should be false after Delete")
	}
	if err := be.Delete("a/x"); err != nil {
		t.Fatalf("Delete of absent key must be nil, got %v", err)
	}
}

func TestFileBackendConformance(t *testing.T) {
	be, err := NewFileBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backendConformance(t, be)
}

// TestS3BackendConformance runs the real aws-sdk-go-v2 S3 client against the
// in-process fake — exercising request signing, path-style addressing, and the
// six verbs over actual HTTP.
func TestS3BackendConformance(t *testing.T) {
	srv := httptest.NewServer(newFakeS3())
	defer srv.Close()
	be := NewS3Backend(S3Config{
		Endpoint:        srv.URL,
		Region:          "us-east-1",
		Bucket:          "weft-test",
		Prefix:          "weft/v1",
		AccessKeyID:     "test",
		SecretAccessKey: "test",
		PathStyle:       true,
	})
	backendConformance(t, be)
}

// TestEngineConvergesOverS3 proves the full encrypted convergence works over
// the S3 backend (via the fake) — the first-milestone path end to end.
func TestEngineConvergesOverS3(t *testing.T) {
	srv := httptest.NewServer(newFakeS3())
	defer srv.Close()
	mk := func() Backend {
		return NewS3Backend(S3Config{
			Endpoint: srv.URL, Region: "us-east-1", Bucket: "weft-test",
			Prefix: "weft/v1", AccessKeyID: "k", SecretAccessKey: "s", PathStyle: true,
		})
	}
	vk, _ := NewVaultKey()
	va, _ := vaultTemp(t)
	vb, _ := vaultTemp(t)
	ea, _ := NewEncrypted(va, mk(), vk)
	eb, _ := NewEncrypted(vb, mk(), vk)

	mustWrite(t, va, "n.html", note("W", "<p>over-s3</p>"))
	if _, err := ea.Sync(); err != nil {
		t.Fatalf("A sync: %v", err)
	}
	if _, err := eb.Sync(); err != nil {
		t.Fatalf("B sync: %v", err)
	}
	if !strings.Contains(read(t, vb, "n.html"), "over-s3") {
		t.Fatalf("B did not converge over S3: %q", read(t, vb, "n.html"))
	}
}
