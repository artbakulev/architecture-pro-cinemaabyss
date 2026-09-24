package main

import (
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
)

//   EVENTS_SERVICE_URL: http://events-service:8082

type proxyBackend struct {
	url    *url.URL
	weight int
	prefix string
}

func (p proxyBackend) Weight() int    { return p.weight }
func (p proxyBackend) Prefix() string { return p.prefix }

type routable interface {
	Weight() int
	Prefix() string
}

func weightedChoice[T routable](choices []T, path string) (T, bool) {
	total := 0
	var filtered []T
	if path != "" {
		for _, c := range choices {
			if strings.HasPrefix(path, c.Prefix()) {
				filtered = append(filtered, c)
			}
		}
	} else {
		filtered = choices
	}

	var zero T
	if len(filtered) == 0 {
		return zero, false
	}

	for _, c := range filtered {
		total += c.Weight()
	}
	if total <= 0 {
		return zero, false
	}

	n := rand.IntN(total)
	for _, c := range filtered {
		n -= c.Weight()
		if n < 0 {
			return c, true
		}
	}
	return filtered[len(filtered)-1], true
}

func newProxyRewrite(backends []proxyBackend, defaultBackend proxyBackend) func(*httputil.ProxyRequest) {
	return func(pr *httputil.ProxyRequest) {
		backend, ok := weightedChoice(backends, pr.In.URL.Path)
		if !ok {
			backend = defaultBackend
		}

		pr.Out.URL.Scheme = backend.url.Scheme
		pr.Out.URL.Host = backend.url.Host
		pr.Out.Header.Add("X-Forwarded-From", pr.In.RemoteAddr)
	}
}

func loadBackends() (proxyBackend, []proxyBackend, error) {
	monolithURL, err := url.Parse(os.Getenv("MONOLITH_URL"))
	if err != nil || monolithURL.Host == "" {
		return proxyBackend{}, nil, errors.New("invalid $MONOLITH_URL")
	}
	defaultBackend := proxyBackend{url: monolithURL, weight: 100}

	isGradualMigration := os.Getenv("GRADUAL_MIGRATION")
	if isGradualMigration != "" && isGradualMigration != "true" {
		return defaultBackend, nil, nil
	}

	moviesServiceURL, err := url.Parse(os.Getenv("MOVIES_SERVICE_URL"))
	if err != nil || moviesServiceURL.Host == "" {
		return defaultBackend, nil, nil
	}

	moviesTrafficPercent, err := strconv.Atoi(os.Getenv("MOVIES_MIGRATION_PERCENT"))
	if err != nil || moviesTrafficPercent > 100 || moviesTrafficPercent < 0 {
		return defaultBackend, nil, nil
	}

	return defaultBackend, []proxyBackend{
		{url: monolithURL, weight: 100 - moviesTrafficPercent, prefix: "/api/movies"},
		{url: moviesServiceURL, weight: moviesTrafficPercent, prefix: "/api/movies"},
	}, nil

}

func main() {
	defaultBackend, backends, err := loadBackends()
	if err != nil {
		log.Fatalf("load backends: %v", err)
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: newProxyRewrite(backends, defaultBackend),
	}

	port, err := strconv.Atoi((os.Getenv("PORT")))
	if err != nil {
		log.Fatal("Invalid $PORT")
	}

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	log.Printf("Starting reverse proxy listens on %s port", addr)

	err = http.ListenAndServe(addr, proxy)
	if err != nil {
		log.Fatalf("Can not start proxy on %s: %v", addr, err)
	}
}
