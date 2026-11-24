package serve

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
	"github.com/libdns/libdns"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
)

// certmagicSource is the production certSource: certmagic with file storage
// under <serve dir>/acme and, by default, a Let's Encrypt ACME issuer that
// solves DNS-01 through Cloudflare. certmagic retries failed obtains with
// backoff and renews in the background.
type certmagicSource struct {
	cfg   *certmagic.Config
	cache *certmagic.Cache
}

// certmagicOptions configures newCertmagicSource.
type certmagicOptions struct {
	// Dir is the serve dir; storage goes to Dir/acme.
	Dir string
	// ACME is serve.acme.
	ACME config.ServeACMEConfig
	// Issuer replaces the Let's Encrypt issuer (tests). nil means ACME.
	Issuer certmagic.Issuer
	// OnEvent receives "obtaining", "obtained" and "failed".
	OnEvent func(kind string, err error)
}

// newCertmagicSource builds the certmagic config. Nothing here touches the
// network.
func newCertmagicSource(opts certmagicOptions) (*certmagicSource, error) {
	storage := filepath.Join(opts.Dir, ACMEDir)
	if err := EnsureDir(storage); err != nil {
		return nil, err
	}
	logger := zap.New(zapcore.NewCore(
		zapcore.NewConsoleEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(certmagicLogWriter{}),
		zap.InfoLevel,
	))
	s := &certmagicSource{}
	s.cache = certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return s.cfg, nil },
		Logger:           logger,
	})
	onEvent := opts.OnEvent
	if onEvent == nil {
		onEvent = func(string, error) {}
	}
	s.cfg = certmagic.New(s.cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: storage},
		Logger:  logger,
		// Let's Encrypt no longer runs OCSP; nothing to staple.
		OCSP:    certmagic.OCSPConfig{DisableStapling: true},
		OnEvent: certmagicEvents(onEvent),
	})
	issuer := opts.Issuer
	if issuer == nil {
		issuer = newACMEIssuer(s.cfg, opts.Dir, opts.ACME, logger)
	}
	s.cfg.Issuers = []certmagic.Issuer{issuer}
	return s, nil
}

// newACMEIssuer is the Let's Encrypt issuer: DNS-01 only (serve never
// answers HTTP-01 or TLS-ALPN-01, and is not reachable from the internet).
func newACMEIssuer(cfg *certmagic.Config, dir string, ac config.ServeACMEConfig, logger *zap.Logger) *certmagic.ACMEIssuer {
	ca := certmagic.LetsEncryptProductionCA
	if ac.Staging {
		ca = certmagic.LetsEncryptStagingCA
	}
	return certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{
		CA:                      ca,
		TestCA:                  certmagic.LetsEncryptStagingCA,
		Email:                   ac.Email,
		Agreed:                  true,
		DisableHTTPChallenge:    true,
		DisableTLSALPNChallenge: true,
		DNS01Solver: &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{
			DNSProvider: &cloudflareTokenProvider{path: tokenPath(dir)},
			Logger:      logger,
		}},
		Logger: logger,
	})
}

// certmagicEvents adapts certmagic's event stream to PublicCert.event.
func certmagicEvents(onEvent func(string, error)) func(context.Context, string, map[string]any) error {
	return func(_ context.Context, event string, data map[string]any) error {
		switch event {
		case "cert_obtaining":
			onEvent("obtaining", nil)
		case "cert_obtained", "cached_managed_cert":
			onEvent("obtained", nil)
		case "cert_failed":
			err, _ := data["error"].(error)
			if err == nil {
				err = errors.New("certificate request failed")
			}
			onEvent("failed", err)
		}
		return nil
	}
}

// Manage loads any stored certificate for host and starts obtaining or
// renewing it in the background.
func (s *certmagicSource) Manage(ctx context.Context, host string) error {
	if err := s.cfg.ManageAsync(ctx, []string{host}); err != nil {
		return fmt.Errorf("managing certificate for %s: %w", host, err)
	}
	return nil
}

// Lookup returns a copy of the longest-lived cached certificate for host.
func (s *certmagicSource) Lookup(host string) *tls.Certificate {
	var best *certmagic.Certificate
	for _, c := range s.cache.AllMatchingCertificates(host) {
		if c.Leaf == nil {
			continue
		}
		if best == nil || c.Leaf.NotAfter.After(best.Leaf.NotAfter) {
			best = &c
		}
	}
	if best == nil {
		return nil
	}
	tc := best.Certificate
	return &tc
}

// Stop ends certmagic's maintenance goroutine.
func (s *certmagicSource) Stop() { s.cache.Stop() }

// cloudflareTokenProvider is the DNS-01 provider. It reads the token from
// disk on every call, so a missing or badly permissioned token is an
// ordinary (retried) obtain failure rather than a startup error, and a
// replaced token is picked up without a restart.
type cloudflareTokenProvider struct {
	path string
}

func (p *cloudflareTokenProvider) provider() (*cloudflare.Provider, error) {
	tok, err := readCloudflareToken(p.path)
	if err != nil {
		return nil, err
	}
	return &cloudflare.Provider{APIToken: tok}, nil
}

// AppendRecords creates the challenge TXT record.
func (p *cloudflareTokenProvider) AppendRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	cf, err := p.provider()
	if err != nil {
		return nil, err
	}
	return cf.AppendRecords(ctx, zone, recs)
}

// DeleteRecords removes the challenge TXT record.
func (p *cloudflareTokenProvider) DeleteRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	cf, err := p.provider()
	if err != nil {
		return nil, err
	}
	return cf.DeleteRecords(ctx, zone, recs)
}

// certmagicLogWriter sends certmagic's zap output to serve.log.
type certmagicLogWriter struct{}

func (certmagicLogWriter) Write(p []byte) (int, error) {
	logging.Log("serve: certmagic: %s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// newPublicCertFromConfig builds the PublicCert for cfg, or nil when
// serve.public_hostname is unset. It never fails: an invalid hostname or a
// backend that cannot be built is logged and reported by `serve status`.
func newPublicCertFromConfig(dir string, cfg config.ServeConfig, now func() time.Time, issuer certmagic.Issuer) *PublicCert {
	if strings.TrimSpace(cfg.PublicHostname) == "" {
		return nil
	}
	host, err := cfg.EffectivePublicHostname()
	if err != nil {
		logging.Warn("serve: public certificate disabled: %v", err)
		return newPublicCert(cfg.PublicHostname, cfg.ACME.Staging, nil, now, err)
	}
	var p *PublicCert
	src, err := newCertmagicSource(certmagicOptions{
		Dir:     dir,
		ACME:    cfg.ACME,
		Issuer:  issuer,
		OnEvent: func(kind string, err error) { p.event(kind, err) },
	})
	if err != nil {
		logging.Warn("serve: public certificate disabled: %v", err)
		return newPublicCert(host, cfg.ACME.Staging, nil, now, err)
	}
	p = newPublicCert(host, cfg.ACME.Staging, src, now, nil)
	if issuer == nil {
		// Surface a missing or loose token now rather than at the first
		// challenge; the provider re-reads it on each attempt.
		if _, err := readCloudflareToken(tokenPath(dir)); err != nil {
			p.event("failed", err)
		}
	}
	return p
}
