package cache

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestStore(t *testing.T, ttl int) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir(), ttl, 128*1024*1024, 1024, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func meta(ce string, ts int64) *CacheMeta {
	return &CacheMeta{
		StateCode:       200,
		Header:          [][]string{{"Content-Type", "text/html"}},
		Timestamp:       ts,
		contentEncoding: ce,
	}
}

func TestSetGetRoundtrip(t *testing.T) {
	s := newTestStore(t, 0)
	if err := s.Set("/hello/", "", meta("none", time.Now().Unix()), []byte("body")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	data, m, err := s.Get(context.Background(), "/hello/", "", "none")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "body" {
		t.Fatalf("body = %q, want %q", data, "body")
	}
	if m.StateCode != 200 {
		t.Fatalf("StateCode = %d", m.StateCode)
	}
}

// Per-encoding meta must survive a cold memory tier (disk reload) so the body
// pairs with the correct Content-Length/ETag.
func TestPerEncodingMetaColdLoad(t *testing.T) {
	dir := t.TempDir()
	s1, err := NewStore(dir, 0, 1<<20, 1024, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	mNone := meta("none", time.Now().Unix())
	mNone.Header = [][]string{{"Content-Length", "5"}}
	mGz := meta("gzip", time.Now().Unix())
	mGz.Header = [][]string{{"Content-Length", "2"}}
	if err := s1.Set("/p/", "", mNone, []byte("plain")); err != nil {
		t.Fatal(err)
	}
	if err := s1.Set("/p/", "", mGz, []byte("gz")); err != nil {
		t.Fatal(err)
	}

	// Fresh store => empty memory => forces disk load.
	s2, err := NewStore(dir, 0, 1<<20, 1024, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	dNone, metaNone, err := s2.Get(context.Background(), "/p/", "", "none")
	if err != nil {
		t.Fatalf("Get none: %v", err)
	}
	dGz, metaGz, err := s2.Get(context.Background(), "/p/", "", "gzip")
	if err != nil {
		t.Fatalf("Get gzip: %v", err)
	}
	if string(dNone) != "plain" || string(dGz) != "gz" {
		t.Fatalf("bodies swapped: none=%q gzip=%q", dNone, dGz)
	}
	if cl, _ := metaNone.headerValue("Content-Length"); cl != "5" {
		t.Fatalf("none Content-Length = %q, want 5", cl)
	}
	if cl, _ := metaGz.headerValue("Content-Length"); cl != "2" {
		t.Fatalf("gzip Content-Length = %q, want 2", cl)
	}
}

func TestTTLExpiry(t *testing.T) {
	s := newTestStore(t, 10)
	// Timestamp 100s in the past with a 10s TTL => already expired.
	if err := s.Set("/old/", "", meta("none", time.Now().Unix()-100), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(context.Background(), "/old/", "", "none"); err != ErrCacheExpired {
		t.Fatalf("err = %v, want ErrCacheExpired", err)
	}
}

// Purge must be segment-exact: purging /post never removes /post-2.
func TestPurgeSegmentExact(t *testing.T) {
	s := newTestStore(t, 0)
	for _, p := range []string{"/post/", "/post-2/", "/postmortem/"} {
		if err := s.Set(p, "", meta("none", time.Now().Unix()), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	s.Purge("/post/")

	if _, _, err := s.Get(context.Background(), "/post/", "", "none"); err == nil {
		t.Fatalf("/post/ should have been purged")
	}
	for _, p := range []string{"/post-2/", "/postmortem/"} {
		if _, _, err := s.Get(context.Background(), p, "", "none"); err != nil {
			t.Fatalf("%s should still be cached, got %v", p, err)
		}
	}
}

func TestFlush(t *testing.T) {
	s := newTestStore(t, 0)
	for i := 0; i < 5; i++ {
		if err := s.Set(fmt.Sprintf("/p%d/", i), "", meta("none", time.Now().Unix()), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if _, _, err := s.Get(context.Background(), "/p0/", "", "none"); err == nil {
		t.Fatalf("expected miss after flush")
	}
}

// Run under -race to catch Set/Purge/Flush disk races.
func TestConcurrentSetPurgeFlush(t *testing.T) {
	s := newTestStore(t, 0)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		p := fmt.Sprintf("/p%d/", i)
		wg.Add(3)
		go func() { defer wg.Done(); _ = s.Set(p, "", meta("none", time.Now().Unix()), []byte("x")) }()
		go func() { defer wg.Done(); s.Purge(p) }()
		go func() { defer wg.Done(); _, _, _ = s.Get(context.Background(), p, "", "none") }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = s.Flush() }()
	wg.Wait()
}
