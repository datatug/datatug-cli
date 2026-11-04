package commands

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"time"

	athenasdk "github.com/dal-go/dalgo2athena"
)

func openAthenaClient(ctx context.Context, cfg athenaNativeConfig, timeout time.Duration) (client *athenasdk.Client, closeTransport func(), err error) {
	if ctx == nil {
		return nil, nil, errAthenaConfig
	}
	var transport *http.Transport
	defer func() {
		ctxErr := athenaFresh(ctx)
		if ctxErr == nil && err == nil {
			return
		}
		if ctxErr == nil {
			ctxErr = err
		}
		if client != nil {
			shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = client.Shutdown(shutdown)
			cancel()
		}
		if transport != nil {
			transport.CloseIdleConnections()
		}
		client, closeTransport, err = nil, nil, ctxErr
	}()
	options, err := athenaClientOptions(ctx, cfg, timeout)
	if err != nil {
		return nil, nil, err
	}
	var roots *x509.CertPool
	if cfg.CAFile != "" {
		pem, readErr := readDynamoFile(ctx, cfg.CAFile)
		if readErr != nil {
			if ctxErr := athenaFresh(ctx); ctxErr != nil {
				return nil, nil, ctxErr
			}
			return nil, nil, errAthenaConfig
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, nil, errAthenaConfig
		}
	}
	if err = athenaFresh(ctx); err != nil {
		return nil, nil, err
	}
	transport = &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		DisableCompression:  true,
		MaxConnsPerHost:     1,
		MaxIdleConns:        1,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     10 * time.Second,
	}
	options.Transport = transport
	client, err = athenasdk.New(options)
	if err != nil {
		return nil, nil, err
	}
	if ctxErr := athenaFresh(ctx); ctxErr != nil {
		return client, nil, ctxErr
	}
	return client, transport.CloseIdleConnections, nil
}
