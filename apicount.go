package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"

	"golang.org/x/oauth2"
)

// apiClient is the HTTP client for every Google API. In debug mode it
// counts requests per method, and logAPICalls writes the counts to the log.
func apiClient(ctx context.Context, ts oauth2.TokenSource) *http.Client {
	hc := oauth2.NewClient(ctx, ts)
	if debugDir != "" {
		hc.Transport = &counting{base: hc.Transport, n: map[string]int{}}
	}
	return hc
}

type counting struct {
	base http.RoundTripper
	mu   sync.Mutex
	n    map[string]int
}

func (c *counting) RoundTrip(r *http.Request) (*http.Response, error) {
	key := r.Method + " " + r.URL.Host + apiPattern(r.URL.Path)
	c.mu.Lock()
	c.n[key]++
	c.mu.Unlock()
	return c.base.RoundTrip(r)
}

// apiPattern replaces the IDs in a Google API path with *. Paths alternate
// collection and ID after the version, as in /v1/spaces/ID/messages/ID, and
// may end in a :verb. Uploads carry an /upload prefix, and media downloads
// take an opaque resource name.
func apiPattern(path string) string {
	path = strings.Trim(path, "/")
	prefix := ""
	if rest, ok := strings.CutPrefix(path, "upload/"); ok {
		prefix, path = "/upload", rest
	}
	segs := strings.Split(path, "/")
	if len(segs) > 2 && segs[1] == "media" {
		return prefix + "/" + segs[0] + "/media/*"
	}
	for i := range segs {
		if i == 0 || i%2 == 1 {
			continue // the version, and collection names
		}
		_, verb, hasVerb := strings.Cut(segs[i], ":")
		segs[i] = "*"
		if hasVerb {
			segs[i] += ":" + verb
		}
	}
	return prefix + "/" + strings.Join(segs, "/")
}

func logAPICalls(hc *http.Client) {
	c, ok := hc.Transport.(*counting)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.n))
	total := 0
	for k, n := range c.n {
		keys = append(keys, k)
		total += n
	}
	slices.SortFunc(keys, func(a, b string) int { return c.n[b] - c.n[a] })
	var b strings.Builder
	fmt.Fprintf(&b, "api calls: %d total", total)
	for _, k := range keys {
		fmt.Fprintf(&b, "\n  %6d  %s", c.n[k], k)
	}
	log.Print(b.String())
}
