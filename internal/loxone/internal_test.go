package loxone

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFormatUUID(t *testing.T) {
	b := []byte{0x78, 0x56, 0x34, 0x12, 0xcd, 0xab, 0x01, 0xef, 0xff, 0xff, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	if got := formatUUID(b); got != "12345678-abcd-ef01-ffff010203040506" {
		t.Fatal(got)
	}
}

func TestParseEvents(t *testing.T) {
	u := []byte{1, 0, 0, 0, 2, 0, 3, 0, 4, 5, 6, 7, 8, 9, 10, 11}
	v := append([]byte(nil), u...)
	v = binary.LittleEndian.AppendUint64(v, math.Float64bits(21.5))
	evs, err := parseValueEvents(v)
	if err != nil || len(evs) != 1 || evs[0].Value != 21.5 || evs[0].UUID != "00000001-0002-0003-0405060708090a0b" {
		t.Fatalf("%+v %v", evs, err)
	}
	if _, err := parseValueEvents(v[:20]); err == nil {
		t.Fatal("truncated value table accepted")
	}
	var tx []byte
	for _, s := range []string{"abcde", "xy"} {
		tx = append(tx, u...)
		tx = append(tx, make([]byte, 16)...)
		tx = binary.LittleEndian.AppendUint32(tx, uint32(len(s)))
		tx = append(tx, s...)
		for len(tx)%4 != 0 {
			tx = append(tx, 0)
		}
	}
	tevs, err := parseTextEvents(tx)
	if err != nil || len(tevs) != 2 || tevs[0].Text != "abcde" || tevs[1].Text != "xy" {
		t.Fatalf("%+v %v", tevs, err)
	}
	bad := append([]byte(nil), tx[:36]...)
	binary.LittleEndian.PutUint32(bad[32:], 1000)
	if _, err := parseTextEvents(bad); err == nil {
		t.Fatal("text longer than the table accepted")
	}
}

func TestHeader(t *testing.T) {
	h, err := parseHeader([]byte{3, 2, 1, 0, 24, 0, 0, 0})
	if err != nil || h.ident != msgValueEvents || !h.estimated || h.length != 24 {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := parseHeader([]byte{4, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("bad header accepted")
	}
	if (header{ident: msgKeepalive}).hasPayload() || !(header{ident: msgText}).hasPayload() {
		t.Fatal("payload rules")
	}
}

func TestPasswordHash(t *testing.T) {
	// HMAC-SHA256("key", "user:" + upper(hex(sha256("pw:salt"))))
	got, err := passwordHash(keyInfo{Key: "6b6579", Salt: "salt", HashAlg: "SHA256"}, "user", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 64 {
		t.Fatalf("hash %q", got)
	}
	again, _ := passwordHash(keyInfo{Key: "6b6579", Salt: "salt", HashAlg: "SHA256"}, "user", "pw")
	other, _ := passwordHash(keyInfo{Key: "6b6579", Salt: "salt", HashAlg: "SHA256"}, "user", "pw2")
	if got != again || got == other {
		t.Fatal("hash is not a function of the password")
	}
	if _, err := passwordHash(keyInfo{Key: "zz", HashAlg: "SHA256"}, "u", "p"); err == nil {
		t.Fatal("non-hex key accepted")
	}
	if _, err := hashFunc("MD5"); err == nil {
		t.Fatal("md5 accepted")
	}
}

func TestControlKey(t *testing.T) {
	if controlKey("jdev/sps/io/abc/on") != controlKey("dev/sps/io/abc/on") {
		t.Fatal("jdev and dev differ")
	}
	if controlKey("jdev/sps/io/abc/on") == controlKey("dev/sps/io/abd/on") {
		t.Fatal("different controls match")
	}
	match := answers("jdev/sps/io/abc/on")
	if !match([]byte(`{"LL":{"control":"dev/sps/io/abc/on","value":"1","Code":"200"}}`)) {
		t.Fatal("own answer rejected")
	}
	if match([]byte(`{"LL":{"control":"dev/sps/io/xyz/off","value":"1","Code":"200"}}`)) {
		t.Fatal("another command's answer accepted")
	}
	if !isFile([]byte(`{"lastModified":"x"}`)) || isFile([]byte(`{"LL":{"control":"dev/x","Code":"200"}}`)) {
		t.Fatal("file detection")
	}
}

func TestParseResponseCode(t *testing.T) {
	for _, in := range []string{`{"LL":{"control":"x","value":"1","Code":"200"}}`, `{"LL":{"control":"x","value":"1","code":200}}`} {
		r, err := parseResponse([]byte(in))
		if err != nil || r.Code != 200 {
			t.Fatalf("%s: %+v %v", in, r, err)
		}
	}
}

func TestResolveCloud(t *testing.T) {
	target := "https://1-2-3-4.000000000000.dyndns.loxonecloud.com:4321/path"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/evil" {
			http.Redirect(w, r, "https://example.com/", http.StatusTemporaryRedirect)
			return
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	cloudHosts[u.Hostname()] = true
	defer delete(cloudHosts, u.Hostname())
	got, err := Resolve(context.Background(), srv.Client(), srv.URL+"/000000000000")
	if err != nil || got != "https://1-2-3-4.000000000000.dyndns.loxonecloud.com:4321/" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := Resolve(context.Background(), srv.Client(), srv.URL+"/evil"); err == nil {
		t.Fatal("redirect outside loxonecloud.com accepted")
	}
}

func TestWebsocketURL(t *testing.T) {
	got, err := websocketURL("https://h.example:1234/")
	if err != nil || got != "wss://h.example:1234/ws/rfc6455" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := websocketURL("ftp://x/"); err == nil {
		t.Fatal("ftp accepted")
	}
}

func TestErrorCategoryHidesAddresses(t *testing.T) {
	err := &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "1-2-3-4.000000000000.dyndns.loxonecloud.com"}}
	got := errorCategory(fmt.Errorf("websocket: Get \"wss://1-2-3-4.000000000000.dyndns.loxonecloud.com:4321/ws/rfc6455\": %w", err))
	if strings.Contains(got, "1-2-3-4") || strings.Contains(got, "000000000000") || got != "the Miniserver address could not be resolved" {
		t.Fatal(got)
	}
	if got := errorCategory(&CodeError{Command: "x", Code: 401}); got != "the Miniserver answered 401" {
		t.Fatal(got)
	}
}

func TestCommandWithoutConnectionFailsAtOnce(t *testing.T) {
	c := New(Config{URL: "https://127.0.0.1:1/", User: "u", Password: "p"})
	start := time.Now()
	if _, err := c.Command(context.Background(), "00000000-0000-0000-ffff000000000000", "on"); err == nil {
		t.Fatal("command without a connection")
	}
	if time.Since(start) > time.Second {
		t.Fatal("waited for a connection")
	}
}
