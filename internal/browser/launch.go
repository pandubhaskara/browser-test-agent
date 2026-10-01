package browser

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/pkg/spec"
)

// Launched is a running browser plus the Playwright instance behind it.
type Launched struct {
	PW      *playwright.Playwright
	Browser playwright.Browser
	// Name and Version are what run_started reports.
	Name    string
	Version string
}

// Close shuts the browser and the driver down. Errors are joined rather than
// short-circuited: a browser that would not close must not stop the driver
// from being stopped, or the Node process it runs in is left behind on the VM.
func (l *Launched) Close() error {
	var errs []error
	if l.Browser != nil {
		if err := l.Browser.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close browser: %w", err))
		}
	}
	if l.PW != nil {
		if err := l.PW.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop playwright: %w", err))
		}
	}
	return errors.Join(errs...)
}

// InfraError marks a failure that is the runner's or the VM's, not the test's:
// the browser would not start, the driver is missing. The runner exits
// non-zero for these so the worker files the run as `error`, which does not
// count toward alerts.
type InfraError struct{ Err error }

func (e *InfraError) Error() string { return e.Err.Error() }
func (e *InfraError) Unwrap() error { return e.Err }

// Launch starts the browser a RunSpec names, headless, with the launch-level
// options (proxy, CORS). Context-level options are applied per attempt by
// NewContext, because every retry gets a fresh context.
func Launch(driverDir string, s *spec.RunSpec) (*Launched, error) {
	pw, err := playwright.Run(&playwright.RunOptions{
		DriverDirectory:     driverDir,
		SkipInstallBrowsers: true,
		Verbose:             false,
	})
	if err != nil {
		return nil, &InfraError{fmt.Errorf("start playwright driver (run `install-browsers` on this VM?): %w", err)}
	}

	launch := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
		// Two minutes to start: a cold VM under load can take a while to page
		// the browser in, and a failure here is an infra error either way.
		Timeout: playwright.Float(120_000),
	}
	if s.Options.ProxyURL != "" {
		proxy, err := proxyFromURL(s.Options.ProxyURL)
		if err != nil {
			pw.Stop()
			return nil, err
		}
		launch.Proxy = proxy
	}

	var bt playwright.BrowserType
	switch s.Browser {
	case spec.BrowserChrome:
		bt = pw.Chromium
		if s.Options.DisableCORS {
			launch.Args = append(launch.Args, "--disable-web-security")
		}
	case spec.BrowserEdge:
		bt = pw.Chromium
		launch.Channel = playwright.String("msedge")
		if s.Options.DisableCORS {
			launch.Args = append(launch.Args, "--disable-web-security")
		}
	case spec.BrowserFirefox:
		bt = pw.Firefox
		// Firefox has no equivalent flag. The option is refused in the editor
		// for Firefox; a spec that still carries it is a bug upstream, so say
		// so rather than silently run without it.
		if s.Options.DisableCORS {
			pw.Stop()
			return nil, &InfraError{errors.New("disable_cors is not supported on firefox")}
		}
	default:
		pw.Stop()
		return nil, &InfraError{fmt.Errorf("unknown browser %q", s.Browser)}
	}

	b, err := bt.Launch(launch)
	if err != nil {
		pw.Stop()
		return nil, &InfraError{fmt.Errorf("launch %s: %w", s.Browser, err)}
	}
	return &Launched{PW: pw, Browser: b, Name: s.Browser, Version: b.Version()}, nil
}

// Session is one attempt's browser context and its first page.
type Session struct {
	Context playwright.BrowserContext
	Page    playwright.Page
	Net     *NetworkLog
}

// Close closes the context, which closes every page in it and discards its
// cookies and downloads — the fresh start the next attempt wants.
func (s *Session) Close() error {
	if s.Context == nil {
		return nil
	}
	return s.Context.Close()
}

// NewContext opens a fresh browser context configured from the RunSpec's
// options and device, with network hooks attached. Everything here is
// per-attempt: a retry must not inherit the failed attempt's cookies, storage
// or half-loaded pages.
func NewContext(l *Launched, s *spec.RunSpec, runDir string) (*Session, error) {
	opts := playwright.BrowserNewContextOptions{
		AcceptDownloads:   playwright.Bool(true),
		IgnoreHttpsErrors: playwright.Bool(s.Options.IgnoreCertErrors),
		BypassCSP:         playwright.Bool(s.Options.DisableCSP),
	}
	if err := contextOptionsForDevice(s.Device, &opts); err != nil {
		return nil, &InfraError{err}
	}
	if len(s.Options.Headers) > 0 {
		opts.ExtraHttpHeaders = s.Options.Headers
	}
	if a := s.Options.HTTPAuth; a != nil {
		// Playwright answers whatever scheme the server challenges with
		// (Basic, Digest, NTLM), so the type is not passed through.
		opts.HttpCredentials = &playwright.HttpCredentials{Username: a.Username, Password: a.Password}
	}
	tz := s.Options.Timezone
	if tz == "" {
		tz = "UTC"
	}
	opts.TimezoneId = playwright.String(tz)
	lang := s.Options.Language
	if lang == "" {
		lang = "en"
	}
	opts.Locale = playwright.String(lang)
	if cc := s.ClientCert; cc != nil {
		if len(cc.Origins) == 0 {
			return nil, &InfraError{errors.New("client certificate has no origins; Playwright needs the exact origin(s) it applies to")}
		}
		for _, origin := range cc.Origins {
			cert := playwright.ClientCertificate{
				Origin: origin,
				Cert:   []byte(cc.CertPEM),
				Key:    []byte(cc.KeyPEM),
			}
			if cc.Passphrase != "" {
				cert.Passphrase = playwright.String(cc.Passphrase)
			}
			opts.ClientCertificates = append(opts.ClientCertificates, cert)
		}
	}

	ctx, err := l.Browser.NewContext(opts)
	if err != nil {
		return nil, &InfraError{fmt.Errorf("new browser context: %w", err)}
	}

	if len(s.Options.Cookies) > 0 {
		cookies, err := parseCookies(s.Options.Cookies, s.StartURL)
		if err != nil {
			ctx.Close()
			return nil, err
		}
		if err := ctx.AddCookies(cookies); err != nil {
			ctx.Close()
			return nil, &InfraError{fmt.Errorf("set cookies: %w", err)}
		}
	}

	net := NewNetworkLog(runDir, s.Options.CaptureHTTPPayloads)
	if err := net.Attach(ctx, s.Options.BlockedRequests); err != nil {
		ctx.Close()
		return nil, err
	}

	page, err := ctx.NewPage()
	if err != nil {
		ctx.Close()
		return nil, &InfraError{fmt.Errorf("new page: %w", err)}
	}
	return &Session{Context: ctx, Page: page, Net: net}, nil
}

// proxyFromURL turns http://user:pass@host:3128 into Playwright's proxy shape.
func proxyFromURL(raw string) (*playwright.Proxy, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("proxy_url %q is not a URL", raw)
	}
	p := &playwright.Proxy{Server: u.Scheme + "://" + u.Host}
	if u.User != nil {
		p.Username = playwright.String(u.User.Username())
		if pw, ok := u.User.Password(); ok {
			p.Password = playwright.String(pw)
		}
	}
	return p, nil
}

// parseCookies reads Set-Cookie lines. A cookie with no Domain applies to the
// start URL's host, which is what a user pasting a session cookie means.
func parseCookies(lines []string, startURL string) ([]playwright.OptionalCookie, error) {
	host := ""
	if u, err := url.Parse(startURL); err == nil {
		host = u.Hostname()
	}
	out := make([]playwright.OptionalCookie, 0, len(lines))
	for _, line := range lines {
		c, err := http.ParseSetCookie(strings.TrimSpace(line))
		if err != nil {
			return nil, fmt.Errorf("cookie %q: %w", line, err)
		}
		oc := playwright.OptionalCookie{Name: c.Name, Value: c.Value}
		domain := c.Domain
		if domain == "" {
			domain = host
		}
		oc.Domain = playwright.String(domain)
		path := c.Path
		if path == "" {
			path = "/"
		}
		oc.Path = playwright.String(path)
		if !c.Expires.IsZero() {
			oc.Expires = playwright.Float(float64(c.Expires.Unix()))
		} else if c.MaxAge > 0 {
			oc.Expires = playwright.Float(float64(time.Now().Add(time.Duration(c.MaxAge) * time.Second).Unix()))
		}
		if c.HttpOnly {
			oc.HttpOnly = playwright.Bool(true)
		}
		if c.Secure {
			oc.Secure = playwright.Bool(true)
		}
		out = append(out, oc)
	}
	return out, nil
}
