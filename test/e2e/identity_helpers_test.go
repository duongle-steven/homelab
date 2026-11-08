package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/duongle-steven/homelab/test/internal/identity"
	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/testenv"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	kanidmURL     = "https://auth." + suiteDomain
	smokeRedirect = "https://smoke." + suiteDomain + "/callback"
)

const credentialsSecret = "identity-test-credentials"

type testAccount struct {
	name  string
	group string // empty: the account is in no homelab group
}

var testAccounts = []testAccount{
	{"dev-admin", "homelab-admins"},
	{"dev-editor", "homelab-editors"},
	{"dev-viewer", "homelab-users"},
	{"dev-outsider", ""},
}

// lookupGatewayAddress finds the address the environment's Gateway listens on once: the k3d node's IP in the sandbox,
// the Gateway's VIP on testbed (config.json's service_vips.gateway). An error fails the tests that need it, not the
// whole run. It runs once for the whole suite, so outside any spec's context, with its own minute.
var lookupGatewayAddress = sync.OnceValues(func() (string, error) {
	if os.Getenv("ENV") == "testbed" {
		return testbedGatewayVIP()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", testenv.K3dNode).Output()
	if err != nil {
		return "", fmt.Errorf("docker inspect %s: %w (is the sandbox running?)", testenv.K3dNode, err)
	}
	return testenv.NodeAddressFromInspect(string(out))
})

// gatewayAddress is the address the Gateway listens on, or fails the spec.
func gatewayAddress() string {
	addr, err := lookupGatewayAddress()
	Expect(err).NotTo(HaveOccurred())
	return addr
}

// dialGateway sends the environment's hosts (*.<suiteDomain>) to its Gateway (the sandbox's k3d node instead of the
// localhost proxy, which GitHub Actions does not run); other hosts are dialled as they are
func dialGateway(ctx context.Context, network, addr string) (net.Conn, error) {
	if host, port, err := net.SplitHostPort(addr); err == nil && strings.HasSuffix(host, "."+suiteDomain) {
		node, err := lookupGatewayAddress()
		if err != nil {
			return nil, err
		}
		addr = net.JoinHostPort(node, port)
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
}

// The sandbox uses a local CA; these tests only ever run against it.
var insecureClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext:     dialGateway,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func randomPassword() string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var b strings.Builder
	for i := 0; i < 32; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		Expect(err).NotTo(HaveOccurred())
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String()
}

// ensureCredentials returns stored credentials, or enrols new ones with the reset token kaniop published.
func ensureCredentials(ctx context.Context, account string) identity.Credentials {
	stored, err := kube.Kubectl(ctx, "kanidm", "get", "secret", credentialsSecret, "-o", "jsonpath={.data."+account+"}")
	if err == nil && stored != "" {
		raw, err := base64.StdEncoding.DecodeString(stored)
		Expect(err).NotTo(HaveOccurred())
		var c identity.Credentials
		Expect(json.Unmarshal(raw, &c)).To(Succeed())
		return c
	}

	return enrol(ctx, account, resetToken(ctx, account), func(c identity.Credentials) { storeCredentials(ctx, account, c) })
}

func storeCredentials(ctx context.Context, account string, c identity.Credentials) {
	encoded, err := json.Marshal(c)
	Expect(err).NotTo(HaveOccurred())
	if _, err := kube.Kubectl(ctx, "kanidm", "get", "secret", credentialsSecret); err != nil {
		_, err := kube.Kubectl(ctx, "kanidm", "create", "secret", "generic", credentialsSecret)
		Expect(err).NotTo(HaveOccurred())
	}
	patch, err := json.Marshal(map[string]any{"stringData": map[string]string{account: string(encoded)}})
	Expect(err).NotTo(HaveOccurred())
	// The patch carries credentials: an error names the command, not its output
	_, err = kube.Kubectl(ctx, "kanidm", "patch", "secret", credentialsSecret, "--type", "merge", "-p", string(patch))
	Expect(err != nil).To(BeFalse(), "kubectl patch secret %s failed", credentialsSecret)
}

var resetTokenPattern = regexp.MustCompile(`/ui/reset\?token=([A-Za-z0-9_-]+)`)

// resetToken reads the newest credential reset link kaniop published for the account.
func resetToken(ctx context.Context, account string) string {
	var token string
	Eventually(ctx, func() bool {
		out, err := kube.Kubectl(ctx, "kanidm", "get", "events",
			"--field-selector", "involvedObject.name="+account+",reason=TokenCreated",
			"--sort-by", ".lastTimestamp", "-o", "jsonpath={range .items[*]}{.message}{\"\\n\"}{end}")
		if err != nil {
			return false
		}
		matches := resetTokenPattern.FindAllStringSubmatch(out, -1)
		if len(matches) == 0 {
			return false
		}
		token = matches[len(matches)-1][1]
		return true
	}).WithTimeout(5*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "no TokenCreated event for %s", account)
	return token
}

func kanidmPost(ctx context.Context, path string, body any, out any) {
	payload, err := json.Marshal(body)
	Expect(err).NotTo(HaveOccurred())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kanidmURL+path, bytes.NewReader(payload))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	resp, err := insecureClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(http.StatusOK), "POST %s: %s", path, raw)
	if out != nil {
		Expect(json.Unmarshal(raw, out)).To(Succeed(), "POST %s: %s", path, raw)
	}
}

type cuSession struct {
	Token string `json:"token"`
}

type cuStatus struct {
	MfaRegState json.RawMessage `json:"mfaregstate"`
}

func (s cuStatus) state() string {
	var name string
	if json.Unmarshal(s.MfaRegState, &name) == nil {
		return name
	}
	var tagged map[string]json.RawMessage
	if json.Unmarshal(s.MfaRegState, &tagged) == nil {
		for k := range tagged {
			return k
		}
	}
	return ""
}

// enrol sets a password and a TOTP through Kanidm's credential update API (Kanidm requires MFA for persons).
// store runs before the commit: if storing fails, the account keeps no credentials and kaniop issues a
// new reset token once this one expires, instead of leaving credentials nobody knows.
func enrol(ctx context.Context, account, token string, store func(identity.Credentials)) identity.Credentials {
	var exchanged []json.RawMessage
	kanidmPost(ctx, "/v1/credential/_exchange_intent", token, &exchanged)
	Expect(exchanged).To(HaveLen(2))
	var session cuSession
	Expect(json.Unmarshal(exchanged[0], &session)).To(Succeed())

	update := func(request any) cuStatus {
		var status cuStatus
		kanidmPost(ctx, "/v1/credential/_update", []any{request, session}, &status)
		return status
	}

	c := identity.Credentials{Password: randomPassword()}
	update(map[string]string{"password": c.Password})

	generated := update("totpgenerate")
	var reg struct {
		TotpCheck struct {
			Secret []int  `json:"secret"`
			Algo   string `json:"algo"`
			Step   uint   `json:"step"`
			Digits int    `json:"digits"`
		} `json:"TotpCheck"`
	}
	Expect(json.Unmarshal(generated.MfaRegState, &reg)).To(Succeed(), "mfaregstate: %s", generated.MfaRegState)
	secret := make([]byte, len(reg.TotpCheck.Secret))
	for i, b := range reg.TotpCheck.Secret {
		secret[i] = byte(b)
	}
	c.TOTPSecret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	c.Algorithm, c.Period, c.Digits = reg.TotpCheck.Algo, reg.TotpCheck.Step, reg.TotpCheck.Digits

	code, err := strconv.Atoi(clock.Code(GinkgoTB(), account, c))
	Expect(err).NotTo(HaveOccurred())
	verified := update(map[string][]any{"totpverify": {code, "smoke-test"}})
	if verified.state() == "TotpInvalidSha1" {
		verified = update("totpacceptsha1")
	}
	Expect(verified.state()).To(Equal("None"), "TOTP not accepted: %s", verified.MfaRegState)

	store(c)
	kanidmPost(ctx, "/v1/credential/_commit", session, nil)
	return c
}

var clock = &identity.TOTPClock{Now: time.Now, Sleep: time.Sleep, Last: map[string]uint64{}}

// newBrowser starts headless chromium, bound to the spec's context, that resolves the environment's hosts to its
// Gateway.
func newBrowser(ctx context.Context) (context.Context, func()) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.Flag("host-resolver-rules", "MAP *."+suiteDomain+" "+gatewayAddress()),
		// NixOS-WSL exports an LD_LIBRARY_PATH built for a newer glibc than the dev shell's chromium
		chromedp.Env("LD_LIBRARY_PATH="),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	browserCtx, cancelTimeout := context.WithTimeout(browserCtx, 3*time.Minute)
	// Start the browser on the long-lived context; the first Run owns the browser's lifetime
	Expect(chromedp.Run(browserCtx, network.Enable())).To(Succeed())
	return browserCtx, func() { cancelTimeout(); cancelBrowser(); cancelAlloc() }
}

// browserStep runs actions with a 30 s timeout and reports the page on failure.
func browserStep(ctx context.Context, name, account string, actions ...chromedp.Action) {
	stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := chromedp.Run(stepCtx, actions...); err != nil {
		var location, text string
		_ = chromedp.Run(ctx, chromedp.Location(&location), chromedp.Evaluate(`document.body ? document.body.innerText.slice(0, 500) : ""`, &text))
		Expect(err).NotTo(HaveOccurred(), "login step %q for %s at %s:\n%s", name, account, location, text)
	}
}

// sendKeys types into the field selector matches, again when Kanidm replaced the field between the query and the
// typing (chromium's "Could not find node with given id"): a slow runner saw the password field swapped right after
// the TOTP step. The replaced field had received nothing, so typing again sends the keys once.
func sendKeys(selector, keys string) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		for {
			err := chromedp.SendKeys(selector, keys).Do(ctx)
			if err == nil || !strings.Contains(err.Error(), "Could not find node with given id") || ctx.Err() != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
}

// kanidmLogin completes Kanidm's login form, which the browser must already show.
// Kanidm asks for the TOTP code before the password.
func kanidmLogin(ctx context.Context, account string, c identity.Credentials, password string) {
	browserStep(ctx, "username", account, sendKeys(`input#username`, account+"\n"), chromedp.WaitVisible(`input#totp`))
	browserStep(ctx, "totp", account, sendKeys(`input#totp`, clock.Code(GinkgoTB(), account, c)+"\n"), chromedp.WaitVisible(`input#password`))
	browserStep(ctx, "password", account, sendKeys(`input#password`, password+"\n"))
}

// browserAuthorize logs in to Kanidm with the given password for the sandbox client smoke. It returns the
// code sent to the smoke client and the PKCE verifier, or an empty code and the text of the page the browser
// ended on.
func browserAuthorize(specCtx context.Context, account string, c identity.Credentials, password string) (string, string, string) {
	ctx, cancel := newBrowser(specCtx)
	defer cancel()

	codes := make(chan string, 1)
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*network.EventRequestWillBeSent); ok && strings.HasPrefix(e.Request.URL, smokeRedirect) {
			if u, err := url.Parse(e.Request.URL); err == nil && u.Query().Get("code") != "" {
				select {
				case codes <- u.Query().Get("code"):
				default:
				}
			}
		}
	})

	verifier, challenge := identity.PKCEPair()
	authURL := kanidmURL + "/ui/oauth2?" + url.Values{
		"client_id":             {"smoke"},
		"redirect_uri":          {smokeRedirect},
		"response_type":         {"code"},
		"scope":                 {"openid email profile groups"},
		"state":                 {"identity-test"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
	browserStep(ctx, "open", account, chromedp.Navigate(authURL), chromedp.WaitVisible(`input#username`))
	kanidmLogin(ctx, account, c, password)

	// Stop at the code or at Kanidm's refusal or login failure page instead of always waiting 30 s
	pageText := func() (string, error) {
		var text string
		err := chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &text))
		return text, err
	}
	deadline := time.After(30 * time.Second)
	for {
		select {
		case code := <-codes:
			return code, "", verifier
		case <-deadline:
			text, err := pageText()
			Expect(err).NotTo(HaveOccurred())
			return "", text, verifier
		case <-time.After(250 * time.Millisecond):
			// Errors while the page is navigating are expected; try again on the next tick
			if text, err := pageText(); err == nil && (strings.Contains(text, "Access Denied") || strings.Contains(text, "Login Failed")) {
				return "", text, verifier
			}
		}
	}
}

// openLogin opens an app's SSO entry point and waits for Kanidm's login form. Some apps (paperless through
// django-allauth) first show a confirmation page with a submit button; it is clicked when present.
func openLogin(ctx context.Context, account, startURL string) {
	browserStep(ctx, "open", account, chromedp.Navigate(startURL))
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if chromedp.Run(waitCtx, chromedp.WaitVisible(`input#username`)) == nil {
		return
	}
	// The click navigates without waiting for the load event: a node found before Kanidm's page has loaded
	// is gone when the document is replaced ("Could not find node with given id"), so wait for the load first
	browserStep(ctx, "confirm", account, chromedp.Click(`button[type=submit]`))
	Eventually(ctx, func() bool {
		var loaded bool
		// Errors while the page is navigating are expected; try again on the next tick
		err := chromedp.Run(ctx, chromedp.Evaluate(`location.origin === "`+kanidmURL+`" && document.readyState === "complete"`, &loaded))
		return err == nil && loaded
	}).WithTimeout(30*time.Second).WithPolling(100*time.Millisecond).Should(BeTrue(), "%s: Kanidm's login page never loaded", account)
	browserStep(ctx, "login form", account, chromedp.WaitVisible(`input#username`))
}

// appLogin opens an app's SSO entry point, logs in through Kanidm, waits until the browser is back
// on appHost (outside any callback or login path) and returns the cookies the app set.
func appLogin(specCtx context.Context, account string, c identity.Credentials, startURL, appHost string) []*http.Cookie {
	ctx, cancel := newBrowser(specCtx)
	defer cancel()

	openLogin(ctx, account, startURL)
	kanidmLogin(ctx, account, c, c.Password)

	var location string
	Eventually(ctx, func() bool {
		if chromedp.Run(ctx, chromedp.Location(&location)) != nil {
			return false
		}
		if acceptConsent(ctx) {
			return false
		}
		u, err := url.Parse(location)
		return err == nil && u.Host == appHost && !strings.Contains(u.Path, "callback") && !strings.Contains(u.Path, "/login")
	}).WithTimeout(30*time.Second).WithPolling(500*time.Millisecond).Should(BeTrue(), func() string {
		var text string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText.slice(0, 500) : ""`, &text))
		return fmt.Sprintf("did not return to app: %s did not return to %s; last at %s:\n%s", account, appHost, location, text)
	})

	var cookies []*http.Cookie
	Expect(chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		// The start URL too: an app under a path (Kiali's /kiali) scopes its cookies to it
		got, err := network.GetCookies().WithURLs([]string{"https://" + appHost + "/", startURL}).Do(ctx)
		for _, c := range got {
			cookies = append(cookies, &http.Cookie{Name: c.Name, Value: c.Value})
		}
		return err
	}))).To(Succeed())
	Expect(cookies).NotTo(BeEmpty(), "no session cookies from %s", appHost)
	return cookies
}

// acceptConsent clicks "Proceed" on Kanidm's consent page, which public clients (zot, OpenCloud) cannot skip,
// like a user would; it reports whether the page was there.
func acceptConsent(ctx context.Context) bool {
	var consented bool
	_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		if (!document.body || !document.body.innerText.includes("Consent to Proceed")) return false;
		const b = [...document.querySelectorAll("button")].find(b => b.innerText.trim() === "Proceed");
		if (b) b.click();
		return !!b;
	})()`, &consented))
	return consented
}

// waitForPage polls the page text until it contains one of the markers (30 s) and returns it.
func waitForPage(ctx context.Context, markers ...string) string {
	var text string
	Eventually(ctx, func() bool {
		if chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &text)) != nil {
			return false
		}
		for _, m := range markers {
			if strings.Contains(text, m) {
				return true
			}
		}
		return false
	}).WithTimeout(30*time.Second).WithPolling(250*time.Millisecond).Should(BeTrue(), "page never showed %v", markers)
	return text
}

// browserDenied opens an app's SSO entry point, logs in to Kanidm and returns the page Kanidm ends on
// (expected: "Access Denied" for an account outside the app's access group).
func browserDenied(specCtx context.Context, account string, c identity.Credentials, startURL string) string {
	ctx, cancel := newBrowser(specCtx)
	defer cancel()
	openLogin(ctx, account, startURL)
	kanidmLogin(ctx, account, c, c.Password)
	return waitForPage(ctx, "Access Denied", "Login Failed")
}

// actualOpenIDLogin follows Actual Budget's OpenID login from the IdP URL that /account/login returned. It
// returns the Actual session token from the /openid-cb redirect, or an empty token and Kanidm's page.
func actualOpenIDLogin(specCtx context.Context, account string, c identity.Credentials, idpURL, appBase string) (string, string) {
	ctx, cancel := newBrowser(specCtx)
	defer cancel()
	tokens := make(chan string, 1)
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*network.EventRequestWillBeSent); ok && strings.HasPrefix(e.Request.URL, appBase+"/openid-cb") {
			if u, err := url.Parse(e.Request.URL); err == nil && u.Query().Get("token") != "" {
				select {
				case tokens <- u.Query().Get("token"):
				default:
				}
			}
		}
	})
	openLogin(ctx, account, idpURL)
	kanidmLogin(ctx, account, c, c.Password)
	deadline := time.After(30 * time.Second)
	for {
		select {
		case token := <-tokens:
			return token, ""
		case <-deadline:
			return "", waitForPage(ctx, "")
		case <-time.After(250 * time.Millisecond):
			var text string
			if chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &text)) == nil && strings.Contains(text, "Access Denied") {
				return "", text
			}
		}
	}
}

// openCloudLogin opens OpenCloud's web UI, logs in through Kanidm (accepting the consent page of the public
// client) and returns the bearer token the web UI then sends to the Graph API. The web UI keeps its tokens in
// the browser, so there are no session cookies to reuse.
func openCloudLogin(specCtx context.Context, account string, c identity.Credentials, appBase string) string {
	ctx, cancel := newBrowser(specCtx)
	defer cancel()
	tokens := make(chan string, 1)
	chromedp.ListenTarget(ctx, func(ev any) {
		e, ok := ev.(*network.EventRequestWillBeSent)
		if !ok || !strings.HasPrefix(e.Request.URL, appBase+"/graph/") {
			return
		}
		for name, value := range e.Request.Headers {
			if s, ok := value.(string); ok && strings.EqualFold(name, "Authorization") && strings.HasPrefix(s, "Bearer ") {
				select {
				case tokens <- strings.TrimPrefix(s, "Bearer "):
				default:
				}
			}
		}
	})

	browserStep(ctx, "open", account, chromedp.Navigate(appBase+"/"))
	// The web UI either redirects to Kanidm or first shows its own login button
	Eventually(ctx, func() bool {
		var ready bool
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
			if (document.querySelector("input#username")) return true;
			const b = [...document.querySelectorAll("button, a")].find(b => /^(log ?in|sign in)$/i.test(b.innerText.trim()));
			if (b) b.click();
			return false;
		})()`, &ready))
		return ready
	}).WithTimeout(60*time.Second).WithPolling(500*time.Millisecond).Should(BeTrue(), "%s: Kanidm's login form never showed", account)
	kanidmLogin(ctx, account, c, c.Password)

	deadline := time.After(60 * time.Second)
	for {
		select {
		case token := <-tokens:
			return token
		case <-deadline:
			var location, text string
			_ = chromedp.Run(ctx, chromedp.Location(&location), chromedp.Evaluate(`document.body ? document.body.innerText.slice(0, 500) : ""`, &text))
			Fail(fmt.Sprintf("no OpenCloud token: %s: the web UI sent no bearer token to /graph; last at %s:\n%s", account, location, text))
		case <-time.After(500 * time.Millisecond):
			acceptConsent(ctx)
		}
	}
}

// openCloudAuthorizeURL is the authorization request OpenCloud's web UI sends to Kanidm, with a throwaway
// PKCE challenge: Kanidm refuses accounts outside homelab-files before redirecting back.
func openCloudAuthorizeURL(appBase string) string {
	_, challenge := identity.PKCEPair()
	return kanidmURL + "/ui/oauth2?" + url.Values{
		"client_id":             {"opencloud"},
		"redirect_uri":          {appBase + "/oidc-callback.html"},
		"response_type":         {"code"},
		"scope":                 {"openid profile email"},
		"state":                 {"denied-test"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
}

// getJSON sends a request with cookies or basic auth and optional header name/value pairs, decodes a
// JSON body into out (if not nil) and returns the status.
func getJSON(ctx context.Context, method, target string, cookies []*http.Cookie, user, pass string, body any, out any, headers ...string) int {
	status, err := getJSONE(ctx, method, target, cookies, user, pass, body, out, headers...)
	Expect(err).NotTo(HaveOccurred())
	return status
}

// getJSONE is getJSON returning its error, for conditions inside Eventually (a transport error there must retry)
func getJSONE(ctx context.Context, method, target string, cookies []*http.Cookie, user, pass string, body any, out any, headers ...string) (int, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := insecureClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: %w: %s", method, target, err, raw)
		}
	}
	return resp.StatusCode, nil
}

// waitForIdentityStack blocks until a fresh sandbox has synced Kanidm and the smoke client, because the
// identity tests run before the smoke tests that would otherwise wait for them.
func waitForIdentityStack(ctx context.Context) {
	Eventually(ctx, func() bool {
		_, err := kube.Kubectl(ctx, "kanidm", "get", "secret", "smoke-kanidm-oauth2-credentials", "-o", "name")
		return err == nil && getOK(ctx, insecureClient, kanidmURL+"/oauth2/openid/smoke/.well-known/openid-configuration") == nil
	}).WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "Kanidm is not ready")
}

func smokeClientSecret(ctx context.Context) string {
	return string(waitForSecret(ctx, "kanidm", "smoke-kanidm-oauth2-credentials")["CLIENT_SECRET"])
}

// waitForSecret waits for a Secret that an operator creates (kaniop client credentials) and returns its data.
func waitForSecret(ctx context.Context, namespace, name string) map[string][]byte {
	var secret struct {
		Data map[string][]byte `json:"data"`
	}
	Eventually(ctx, func() error {
		return kube.GetJSON(ctx, namespace, &secret, "secret", name)
	}).WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(Succeed(), "Secret %s/%s does not exist", namespace, name)
	return secret.Data
}

// exchangeCode trades the code for tokens at Kanidm (with the PKCE verifier) and returns the ID token claims.
func exchangeCode(ctx context.Context, code, secret, verifier string) map[string]any {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {smokeRedirect}, "code_verifier": {verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kanidmURL+"/oauth2/token", strings.NewReader(form.Encode()))
	Expect(err).NotTo(HaveOccurred())
	req.SetBasicAuth("smoke", secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := insecureClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(http.StatusOK), "token exchange: %s", raw)

	var tokens struct {
		IDToken string `json:"id_token"`
	}
	Expect(json.Unmarshal(raw, &tokens)).To(Succeed())
	parts := strings.Split(tokens.IDToken, ".")
	Expect(parts).To(HaveLen(3))
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	Expect(err).NotTo(HaveOccurred())
	var claims map[string]any
	Expect(json.Unmarshal(payload, &claims)).To(Succeed())
	return claims
}
