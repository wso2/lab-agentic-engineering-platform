// local-cluster-gateway-proxy is a docker-compose-only stand-in for the
// wso2cloud cluster-gateway-proxy. The real cloud proxy bridges to a
// remote dataplane via an mTLS cluster-agent tunnel; this stub instead
// forwards every request straight to the local k3d k8s API using the
// mounted kubeconfig.
//
// Wire shape matches the cloud proxy exactly so asdlc-api's
// `clients/clustergatewayproxy` HTTP client can target it without
// branching on environment:
//
//	  <verb> /cloud-dp-cgw/<k8s path>   →   <verb> <kubeAPI>/<k8s path>
//
// Allowlist + JWT verification are deliberately disabled here — the
// cloud proxy's middleware chain is logger-only today, and this stub
// inherits that posture for local dev. Hardening (allowlist + auth)
// lives in the cloud proxy and is out-of-scope for local. See
// docs/oc-refactor/workflows/01-plan.md WS1.2.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const proxyPathPrefix = "/cloud-dp-cgw"

func main() {
	level := parseLogLevel(os.Getenv("LOG_LEVEL"))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8085"
	}

	cfg, err := loadKubeRESTConfig()
	if err != nil {
		slog.Error("kubeconfig load failed", "error", err)
		os.Exit(1)
	}
	slog.Info("local-cluster-gateway-proxy starting",
		"listenAddr", listenAddr,
		"upstream", cfg.Host)

	handler, err := newProxyHandler(cfg)
	if err != nil {
		slog.Error("proxy handler init failed", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle(proxyPathPrefix+"/", handler)
	mux.Handle(proxyPathPrefix, http.RedirectHandler(proxyPathPrefix+"/", http.StatusPermanentRedirect))

	server := &http.Server{
		Addr:         listenAddr,
		Handler:      withRequestLogging(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown failed", "error", err)
	}
}

// loadKubeRESTConfig resolves the upstream k8s API:
//   - in-cluster when running inside k8s (not used here, but harmless)
//   - kubeconfig file from KUBECONFIG / standard rules otherwise
func loadKubeRESTConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	loader := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return cfg, nil
}

// newProxyHandler builds an httputil.ReverseProxy targeted at the k8s
// API. Authentication is injected by a rest.HTTPWrappersForConfig
// round-tripper (handles bearer tokens, client certs, exec plugins —
// every kubeconfig auth shape works without branching here).
func newProxyHandler(cfg *rest.Config) (http.Handler, error) {
	upstream, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL %q: %w", cfg.Host, err)
	}

	tlsConfig, err := rest.TLSConfigFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("build TLS config: %w", err)
	}
	if tlsConfig == nil {
		// Kubeconfig might be plain HTTP (rare). Use a permissive
		// default so the transport is still wrappable below.
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	baseTransport := &http.Transport{
		TLSClientConfig:     tlsConfig,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	authed, err := rest.HTTPWrappersForConfig(cfg, baseTransport)
	if err != nil {
		return nil, fmt.Errorf("wrap transport for auth: %w", err)
	}

	proxy := &httputil.ReverseProxy{
		Transport: authed,
		Director: func(req *http.Request) {
			// Strip the `/cloud-dp-cgw` prefix; what remains is the k8s
			// path (e.g. `/api/v1/namespaces`).
			trimmed := strings.TrimPrefix(req.URL.Path, proxyPathPrefix)
			if trimmed == "" {
				trimmed = "/"
			}
			req.URL.Scheme = upstream.Scheme
			req.URL.Host = upstream.Host
			req.URL.Path = singleJoiningSlash(upstream.Path, trimmed)
			req.Host = upstream.Host
			// k8s API expects User-Agent on every request — fill if absent
			// so logs aren't blank.
			if req.Header.Get("User-Agent") == "" {
				req.Header.Set("User-Agent", "local-cluster-gateway-proxy/1.0")
			}
			// X-Correlation-ID is the cloud proxy's tracing key; preserve
			// or synthesize so downstream k8s audit logs can be matched.
			if req.Header.Get("X-Correlation-ID") == "" {
				req.Header.Set("X-Correlation-ID", randomCorrelationID())
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("proxy upstream error",
				"method", r.Method,
				"path", r.URL.Path,
				"error", err)
			http.Error(w, fmt.Sprintf(`{"error":"upstream call failed: %s"}`, err.Error()), http.StatusBadGateway)
		},
		ModifyResponse: func(resp *http.Response) error {
			// Surface k8s status codes verbatim so client_009 retries
			// match the cloud proxy's behavior.
			return nil
		},
	}

	return proxy, nil
}

// withRequestLogging wraps the mux with a one-line request log so
// dispatch traces can be matched against k8s API audit entries.
func withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"durationMs", time.Since(start).Milliseconds(),
			"correlationId", r.Header.Get("X-Correlation-ID"),
		)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

func randomCorrelationID() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	const length = 16
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
		// not crypto-grade — purely for log correlation when callers
		// forget to set X-Correlation-ID. Replace with crypto/rand if
		// real entropy is ever needed.
	}
	return string(b)
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// silence unused-import on platforms where io / strings aren't needed —
// keep the references in one place so refactors don't accidentally drop
// the build-required imports.
var _ = io.Discard
