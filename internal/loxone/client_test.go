package loxone_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/vsem-azamat/loxone-mcp/internal/fakems"
	"github.com/vsem-azamat/loxone-mcp/internal/loxone"
	"github.com/vsem-azamat/loxone-mcp/internal/structure"
)

const (
	lightActive = "10000000-0000-0001-ffff0000000000a2"
	blindPos    = "20000000-0000-0001-ffff0000000000a4"
)

func start(t *testing.T, ms *fakems.Server, tokenFile, password string) (*loxone.Client, chan *structure.File) {
	t.Helper()
	structs := make(chan *structure.File, 4)
	c := loxone.New(loxone.Config{
		URL: ms.URL, User: fakems.User, Password: password, TokenFile: tokenFile,
		HTTPClient: ms.Client(), MaxBackoff: 50 * time.Millisecond, AuthFailBackoff: 50 * time.Millisecond,
		OnStructure: func(f *structure.File) { structs <- f },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return c, structs
}

func waitReady(t *testing.T, c *loxone.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.WaitReady(ctx); err != nil {
		t.Fatalf("not ready: %v (status %+v)", err, c.Status())
	}
}

func TestConnectLoadsStructureAndStates(t *testing.T) {
	ms := fakems.New(map[string]float64{blindPos: 0.25}, map[string]string{lightActive: "[778]"})
	defer ms.Close()
	c, structs := start(t, ms, filepath.Join(t.TempDir(), "token.json"), fakems.Password)
	waitReady(t, c)
	f := <-structs
	if f.LastModified != "2026-01-02 03:04:05" || len(f.Controls) == 0 {
		t.Fatalf("structure: %q, %d controls", f.LastModified, len(f.Controls))
	}
	if v, ok := c.Store().Get(blindPos); !ok || v.Number != 0.25 {
		t.Fatalf("blind position: %+v %v", v, ok)
	}
	if v, ok := c.Store().Get(lightActive); !ok || !v.IsText || v.Text != "[778]" {
		t.Fatalf("light moods: %+v %v", v, ok)
	}
}

func TestTokenIsCachedAndReused(t *testing.T) {
	ms := fakems.New(nil, nil)
	defer ms.Close()
	tokenFile := filepath.Join(t.TempDir(), "token.json")
	c, _ := start(t, ms, tokenFile, fakems.Password)
	waitReady(t, c)
	if ms.Logins() != 1 {
		t.Fatalf("logins = %d, want 1", ms.Logins())
	}
	// a reconnect authenticates with the cached token, not the password
	ms.DropConnections()
	time.Sleep(100 * time.Millisecond)
	waitReady(t, c)
	// a second client with a wrong password still gets in with the cached token
	c2, _ := start(t, ms, tokenFile, "wrong")
	waitReady(t, c2)
	if ms.Logins() != 1 {
		t.Fatalf("logins = %d after reconnects, want 1", ms.Logins())
	}
}

func TestWrongPasswordIsNotReady(t *testing.T) {
	ms := fakems.New(nil, nil)
	defer ms.Close()
	c, _ := start(t, ms, "", "wrong")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := c.WaitReady(ctx); err == nil {
		t.Fatal("ready with a wrong password")
	}
	if st := c.Status(); st.Connected || st.LastError == "" {
		t.Fatalf("status %+v", st)
	}
}

func TestCommandAndEvents(t *testing.T) {
	ms := fakems.New(map[string]float64{blindPos: 0}, nil)
	defer ms.Close()
	ms.SetEffect(func(uuid, cmd string) (map[string]float64, map[string]string, int) {
		if cmd == "FullDown" {
			return map[string]float64{blindPos: 1}, nil, 0
		}
		return nil, nil, 500
	})
	c, _ := start(t, ms, "", fakems.Password)
	waitReady(t, c)
	changed := c.Store().Changed()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Command(ctx, "20000000-0000-0001-ffff000000000001", "FullDown"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-ctx.Done():
		t.Fatal("no state update")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if v, _ := c.Store().Get(blindPos); v.Number == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("blind position not updated")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := c.Command(ctx, "20000000-0000-0001-ffff000000000001", "bogus"); err == nil {
		t.Fatal("a 500 answer is not an error")
	}
	if _, err := c.Command(ctx, "bad/uuid", "on"); err == nil {
		t.Fatal("a uuid with a slash was sent")
	}
}

func TestLogoutRevokesToken(t *testing.T) {
	ms := fakems.New(nil, nil)
	defer ms.Close()
	tokenFile := filepath.Join(t.TempDir(), "token.json")
	c, _ := start(t, ms, tokenFile, fakems.Password)
	waitReady(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	// the revoked token is gone: a client without the password cannot get in
	c2, _ := start(t, ms, tokenFile, "wrong")
	wctx, wcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer wcancel()
	if err := c2.WaitReady(wctx); err == nil {
		t.Fatal("ready after logout without the password")
	}
}

func TestResolveCloudDNS(t *testing.T) {
	relay := "https://1-2-3-4.000000000000.dyndns.loxonecloud.com:4321/"
	dns := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, relay, http.StatusTemporaryRedirect)
	}))
	defer dns.Close()
	// a non-cloud address is returned as is, without its path
	got, err := loxone.Resolve(context.Background(), dns.Client(), dns.URL+"/x?y=1")
	if err != nil || got != dns.URL+"/" {
		t.Fatalf("direct: %q %v", got, err)
	}
}
