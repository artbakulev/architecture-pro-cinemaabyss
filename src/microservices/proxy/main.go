package main

import (
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

type ProxyBackend struct {
	Url    url.URL
	weight int
	prefix string
}

func (p ProxyBackend) Weight() int    { return p.weight }
func (p ProxyBackend) Prefix() string { return p.prefix }

type weighted interface {
	Weight() int
	Prefix() string
}

func weightedChoice[T weighted](choices []T, path string) (T, bool) {
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

func getProxyRewriteFunc(backends []ProxyBackend, defaultBackend ProxyBackend) func(*httputil.ProxyRequest) {
	return func(pr *httputil.ProxyRequest) {
		backend, ok := weightedChoice(backends, pr.In.URL.Path)
		if !ok {
			backend = defaultBackend
		}

		pr.Out.URL.Scheme = backend.Url.Scheme
		pr.Out.URL.Host = backend.Url.Host
		pr.Out.Header.Add("X-Forwarded-From", pr.In.RemoteAddr)
	}
}

func getBackends() (ProxyBackend, []ProxyBackend) {
	monolithUrl, err := url.Parse(os.Getenv("MONOLITH_URL"))
	if err != nil || monolithUrl.Host == "" {
		log.Fatal("Invalid $MONOLITH_URL")
	}
	defaultBackend := ProxyBackend{Url: *monolithUrl, weight: 100}

	isGradualMigration := os.Getenv("GRADUAL_MIGRATION")
	if isGradualMigration != "" && isGradualMigration != "true" {
		return defaultBackend, []ProxyBackend{}
	}

	moviesServiceUrl, err := url.Parse(os.Getenv("MOVIES_SERVICE_URL"))
	if err != nil || moviesServiceUrl.Host == "" {
		return defaultBackend, []ProxyBackend{}
	}

	moviesTrafficPercent, err := strconv.Atoi(os.Getenv("MOVIES_MIGRATION_PERCENT"))
	if err != nil || moviesTrafficPercent > 100 || moviesTrafficPercent < 0 {
		log.Fatal("Invalid $MOVIES_MIGRATION_PERCENT")
	}

	return defaultBackend, []ProxyBackend{
		{Url: *monolithUrl, weight: 100 - moviesTrafficPercent, prefix: "/api/movies"},
		{Url: *moviesServiceUrl, weight: moviesTrafficPercent, prefix: "/api/movies"},
	}

}

func main() {
	defaultBackend, backends := getBackends()

	proxy := &httputil.ReverseProxy{
		Rewrite: getProxyRewriteFunc(backends, defaultBackend),
	}

	port, err := strconv.Atoi((os.Getenv("PORT")))
	if err != nil {
		log.Fatal("Invalid $PORT")
	}

	proxy_address := fmt.Sprintf("0.0.0.0:%d", port)
	log.Printf("Starting reverse proxy listens on %s port", proxy_address)

	err = http.ListenAndServe(proxy_address, proxy)
	if err != nil {
		log.Fatalf("Can not start proxy on %s: %v", proxy_address, err)
	}
}
