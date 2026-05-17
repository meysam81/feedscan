package scanner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

// ExtractExternalURLs fetches the input page, parses HTML, and returns deduped
// external HTTPS URLs (different registrable domain than input).
func ExtractExternalURLs(ctx context.Context, client *http.Client, cfg *Config) ([]string, error) {
	base, err := url.Parse(cfg.InputURL)
	if err != nil {
		return nil, fmt.Errorf("parse input URL: %w", err)
	}
	baseDomain, err := RegistrableDomain(base.Hostname())
	if err != nil {
		return nil, fmt.Errorf("registrable domain: %w", err)
	}

	res, err := fetch(ctx, client, cfg, cfg.InputURL)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("input URL returned %d", res.StatusCode)
	}

	doc, err := html.Parse(bytes.NewReader(res.Body))
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}

	seen := make(map[string]struct{})
	var out []string

	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key != "href" {
					continue
				}
				ref, err := base.Parse(strings.TrimSpace(a.Val))
				if err != nil {
					continue
				}
				if ref.Scheme != "https" {
					continue
				}
				host := ref.Hostname()
				if host == "" {
					continue
				}
				dom, err := RegistrableDomain(host)
				if err != nil || dom == baseDomain {
					continue
				}
				ref.Fragment = ""
				ref.RawQuery = ""
				normalized := ref.Scheme + "://" + ref.Host
				if _, ok := seen[normalized]; ok {
					continue
				}
				seen[normalized] = struct{}{}
				out = append(out, normalized)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)

	sort.Strings(out)
	return out, nil
}

// RegistrableDomain returns the eTLD+1 (e.g. "blog.example.co.uk" -> "example.co.uk").
func RegistrableDomain(host string) (string, error) {
	if host == "" {
		return "", errors.New("empty host")
	}
	return publicsuffix.EffectiveTLDPlusOne(host)
}
