package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mccutchen/go-httpbin/v2/httpbin"
)

const (
	defaultHost = "0.0.0.0"
	defaultPort = 8080
)

var (
	host            string
	port            int
	maxBodySize     int64
	maxDuration     time.Duration
	httpsCertFile   string
	httpsKeyFile    string
	useRealHostname bool
)

func main() {
	flag.StringVar(&host, "host", defaultHost, "Host to listen on")
	flag.IntVar(&port, "port", defaultPort, "Port to listen on")
	flag.StringVar(&httpsCertFile, "https-cert-file", "", "HTTPS Server certificate file")
	flag.StringVar(&httpsKeyFile, "https-key-file", "", "HTTPS Server private key file")
	flag.Int64Var(&maxBodySize, "max-body-size", httpbin.DefaultMaxBodySize, "Maximum size of request or response, in bytes")
	flag.DurationVar(&maxDuration, "max-duration", httpbin.DefaultMaxDuration, "Maximum duration a response may take")
	flag.BoolVar(&useRealHostname, "use-real-hostname", false, "Expose value of os.Hostname() in the /hostname endpoint instead of dummy value")
	flag.Parse()

	// Command line flags take precedence over environment vars, so we only
	// check for environment vars if we have default values for our command
	// line flags.
	var err error
	if maxBodySize == httpbin.DefaultMaxBodySize && os.Getenv("MAX_BODY_SIZE") != "" {
		maxBodySize, err = strconv.ParseInt(os.Getenv("MAX_BODY_SIZE"), 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid value %#v for env var MAX_BODY_SIZE: %s\n\n", os.Getenv("MAX_BODY_SIZE"), err)
			flag.Usage()
			os.Exit(1)
		}
	}
	if maxDuration == httpbin.DefaultMaxDuration && os.Getenv("MAX_DURATION") != "" {
		maxDuration, err = time.ParseDuration(os.Getenv("MAX_DURATION"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid value %#v for env var MAX_DURATION: %s\n\n", os.Getenv("MAX_DURATION"), err)
			flag.Usage()
			os.Exit(1)
		}
	}
	if host == defaultHost && os.Getenv("HOST") != "" {
		host = os.Getenv("HOST")
	}
	if port == defaultPort && os.Getenv("PORT") != "" {
		port, err = strconv.Atoi(os.Getenv("PORT"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid value %#v for env var PORT: %s\n\n", os.Getenv("PORT"), err)
			flag.Usage()
			os.Exit(1)
		}
	}

	if httpsCertFile == "" && os.Getenv("HTTPS_CERT_FILE") != "" {
		httpsCertFile = os.Getenv("HTTPS_CERT_FILE")
	}
	if httpsKeyFile == "" && os.Getenv("HTTPS_KEY_FILE") != "" {
		httpsKeyFile = os.Getenv("HTTPS_KEY_FILE")
	}

	var serveTLS bool
	if httpsCertFile != "" || httpsKeyFile != "" {
		serveTLS = true
		if httpsCertFile == "" || httpsKeyFile == "" {
			fmt.Fprintf(os.Stderr, "Error: https cert and key must both be provided\n\n")
			flag.Usage()
			os.Exit(1)
		}
	}

	// useRealHostname will be true if either the `-use-real-hostname`
	// arg is given on the command line or if the USE_REAL_HOSTNAME env var
	// is one of "1" or "true".
	if useRealHostnameEnv := os.Getenv("USE_REAL_HOSTNAME"); useRealHostnameEnv == "1" || useRealHostnameEnv == "true" {
		useRealHostname = true
	}

	logger := log.New(os.Stderr, "", 0)

	// A hacky log helper function to ensure that shutdown messages are
	// formatted the same as other messages.  See StdLogObserver in
	// httpbin/middleware.go for the format we're matching here.
	serverLog := func(msg string, args ...interface{}) {
		const (
			logFmt  = "time=%q msg=%q"
			dateFmt = "2006-01-02T15:04:05.9999"
		)
		logger.Printf(logFmt, time.Now().Format(dateFmt), fmt.Sprintf(msg, args...))
	}

	opts := []httpbin.OptionFunc{
		httpbin.WithMaxBodySize(maxBodySize),
		httpbin.WithMaxDuration(maxDuration),
		httpbin.WithObserver(httpbin.StdLogObserver(logger)),
	}
	if useRealHostname {
		hostname, err := os.Hostname()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: use-real-hostname=true but hostname lookup failed: %s\n", err)
			os.Exit(1)
		}
		opts = append(opts, httpbin.WithHostname(hostname))
	}
	h := httpbin.New(opts...)

	listenAddr := net.JoinHostPort(host, strconv.Itoa(port))

	server := &http.Server{
		Addr:    listenAddr,
		Handler: h.Handler(),
	}

	// shutdownCh triggers graceful shutdown on SIGINT or SIGTERM
	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, syscall.SIGINT, syscall.SIGTERM)

	// exitCh will be closed when it is safe to exit, after graceful shutdown
	exitCh := make(chan struct{})

	go func() {
		sig := <-shutdownCh
		serverLog("shutdown started by signal: %s", sig)

		shutdownTimeout := maxDuration + 1*time.Second
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		server.SetKeepAlivesEnabled(false)
		if err := server.Shutdown(ctx); err != nil {
			serverLog("shutdown error: %s", err)
		}

		close(exitCh)
	}()

	var listenErr error
	if serveTLS {
		serverLog("go-httpbin listening on https://%s", listenAddr)
		listenErr = server.ListenAndServeTLS(httpsCertFile, httpsKeyFile)
	} else {
		serverLog("go-httpbin listening on http://%s", listenAddr)
		listenErr = server.ListenAndServe()
	}
	if listenErr != nil && listenErr != http.ErrServerClosed {
		logger.Fatalf("failed to listen: %s", listenErr)
	}

	<-exitCh
	serverLog("shutdown finished")
}
