package loxone

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// cloudHosts are the Loxone cloud DNS services; their answer for /<serial> redirects to the
// Miniserver's current address (a Remote Connect relay or the forwarded port).
var cloudHosts = map[string]bool{
	"dns.loxonecloud.com":     true,
	"connect.loxonecloud.com": true,
}

// Resolve returns the base URL (https://host:port/) of the Miniserver. A cloud DNS address is
// resolved on every call, because the relay host and port change; any other address is returned
// as is.
func Resolve(ctx context.Context, hc *http.Client, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("miniserver address is not a URL")
	}
	if !cloudHosts[strings.ToLower(u.Hostname())] {
		u.Path, u.RawQuery, u.Fragment, u.User = "/", "", "", nil
		return u.String(), nil
	}
	noRedirect := *hc
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var lastErr error
	for range 3 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return "", fmt.Errorf("cloud dns: %w", err)
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("cloud dns: %w", err)
			if ctx.Err() != nil {
				return "", lastErr
			}
			continue
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if resp.StatusCode/100 != 3 || loc == "" {
			lastErr = fmt.Errorf("cloud dns answered %d without a redirect", resp.StatusCode)
			continue
		}
		target, err := resp.Request.URL.Parse(loc)
		if err != nil {
			return "", fmt.Errorf("cloud dns redirect: %w", err)
		}
		if target.Scheme != "https" {
			return "", fmt.Errorf("cloud dns redirects to %s, not https", target.Scheme)
		}
		if h := strings.ToLower(target.Hostname()); !strings.HasSuffix(h, ".loxonecloud.com") {
			return "", errors.New("cloud dns redirects outside loxonecloud.com")
		}
		target.Path, target.RawQuery, target.Fragment, target.User = "/", "", "", nil
		return target.String(), nil
	}
	return "", lastErr
}
