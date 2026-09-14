package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
)

//   EVENTS_SERVICE_URL: http://events-service:8082
//   GRADUAL_MIGRATION: "true"
//   MOVIES_MIGRATION_PERCENT: "50"

func getProxyRewriteFunc(backend url.URL) func(*httputil.ProxyRequest) {
	return func(pr *httputil.ProxyRequest) {
		pr.In.URL.Scheme = backend.Scheme
		pr.In.URL.Host = backend.Host
	}
}

func main() {

	monolith_url, err := url.Parse(os.Getenv("MONOLITH_URL"))
	if err != nil || monolith_url.Host == "" {
		log.Fatal("Invalid $MONOLITH_URL")
	}

	// movies_service_url, err := url.Parse(os.Getenv("MOVIES_SERVICE_URL"))
	// if err != nil {
	// log.Fatal("Invalid $MOVIES_SERVICE_URL")
	// }

	proxy := &httputil.ReverseProxy{
		Rewrite: getProxyRewriteFunc(*monolith_url),
	}

	port, err := strconv.Atoi((os.Getenv("PORT")))
	if err != nil {
		log.Fatal("Invalid $PORT")
	}

	proxy_address := fmt.Sprintf("127.0.0.1:%d", port)
	log.Printf("Starting reverse proxy listens on %s port", proxy_address)

	err = http.ListenAndServe(proxy_address, proxy)
	if err != nil {
		log.Fatalf("Can not start proxy on %s: %v", proxy_address, err)
	}
}
