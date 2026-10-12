package test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

const (
	kanidmURL         = "https://auth.127-0-0-1.nip.io"
	dexURL            = "https://dex.127-0-0-1.nip.io"
	smokeRedirect     = "https://smoke.127-0-0-1.nip.io/callback"
	credentialsSecret = "identity-test-credentials"
)

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

// credentials of a sandbox test account, stored in a Secret so reruns can log in again
type credentials struct {
	Password   string `json:"password"`
	TOTPSecret string `json:"totpSecret"` // base32 without padding
	Algorithm  string `json:"algorithm"`  // sha1, sha256 or sha512
	Digits     int    `json:"digits"`
	Period     uint   `json:"period"`
}

// sandboxIngress is the ingress LoadBalancer IP. The tests connect to it directly instead of through
// the localhost socat proxy, whose connects to :443 occasionally stall for ~4.5 s and hang the browser.
const sandboxIngress = "172.30.255.200"

// The sandbox uses a local CA; these tests only ever run against it.
var insecureClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, port, err := net.SplitHostPort(addr); err == nil && strings.HasSuffix(host, ".127-0-0-1.nip.io") {
				addr = net.JoinHostPort(sandboxIngress, port)
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
		},
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func kanidmNamespace() *k8s.KubectlOptions { return k8s.NewKubectlOptions("", "", "kanidm") }

func randomPassword(t *testing.T) string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var b strings.Builder
	for i := 0; i < 32; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		require.NoError(t, err)
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String()
}

// ensureCredentials returns stored credentials, or enrols new ones with the reset token kaniop published.
func ensureCredentials(t *testing.T, account string) credentials {
	stored, err := k8s.RunKubectlAndGetOutputE(t, kanidmNamespace(),
		"get", "secret", credentialsSecret, "-o", "jsonpath={.data."+account+"}")
	if err == nil && stored != "" {
		raw, err := base64.StdEncoding.DecodeString(stored)
		require.NoError(t, err)
		var c credentials
		require.NoError(t, json.Unmarshal(raw, &c))
		return c
	}

	c := enrol(t, account, resetToken(t, account))

	encoded, err := json.Marshal(c)
	require.NoError(t, err)
	if _, err := k8s.RunKubectlAndGetOutputE(t, kanidmNamespace(), "get", "secret", credentialsSecret); err != nil {
		k8s.RunKubectl(t, kanidmNamespace(), "create", "secret", "generic", credentialsSecret)
	}
	patch, err := json.Marshal(map[string]any{"stringData": map[string]string{account: string(encoded)}})
	require.NoError(t, err)
	k8s.RunKubectl(t, kanidmNamespace(), "patch", "secret", credentialsSecret, "--type", "merge", "-p", string(patch))
	return c
}

var resetTokenPattern = regexp.MustCompile(`/ui/reset\?token=([A-Za-z0-9_-]+)`)

// resetToken reads the newest credential reset link kaniop published for the account.
func resetToken(t *testing.T, account string) string {
	var token string
	require.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, kanidmNamespace(), "get", "events",
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
	}, 5*time.Minute, 10*time.Second, "no TokenCreated event for %s", account)
	return token
}

func kanidmPost(t *testing.T, path string, body any, out any) {
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := insecureClient.Post(kanidmURL+path, "application/json", bytes.NewReader(payload))
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST %s: %s", path, raw)
	if out != nil {
		require.NoError(t, json.Unmarshal(raw, out), "POST %s: %s", path, raw)
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
func enrol(t *testing.T, account, token string) credentials {
	var exchanged []json.RawMessage
	kanidmPost(t, "/v1/credential/_exchange_intent", token, &exchanged)
	require.Len(t, exchanged, 2)
	var session cuSession
	require.NoError(t, json.Unmarshal(exchanged[0], &session))

	update := func(request any) cuStatus {
		var status cuStatus
		kanidmPost(t, "/v1/credential/_update", []any{request, session}, &status)
		return status
	}

	c := credentials{Password: randomPassword(t)}
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
	require.NoError(t, json.Unmarshal(generated.MfaRegState, &reg), "mfaregstate: %s", generated.MfaRegState)
	secret := make([]byte, len(reg.TotpCheck.Secret))
	for i, b := range reg.TotpCheck.Secret {
		secret[i] = byte(b)
	}
	c.TOTPSecret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	c.Algorithm, c.Period, c.Digits = reg.TotpCheck.Algo, reg.TotpCheck.Step, reg.TotpCheck.Digits

	code, err := strconv.Atoi(clock.code(t, account, c))
	require.NoError(t, err)
	verified := update(map[string][]any{"totpverify": {code, "smoke-test"}})
	if verified.state() == "TotpInvalidSha1" {
		verified = update("totpacceptsha1")
	}
	require.Equal(t, "None", verified.state(), "TOTP not accepted: %s", verified.MfaRegState)

	kanidmPost(t, "/v1/credential/_commit", session, nil)
	return c
}

func totpCode(t *testing.T, c credentials, at time.Time) string {
	algorithms := map[string]otp.Algorithm{"sha1": otp.AlgorithmSHA1, "sha256": otp.AlgorithmSHA256, "sha512": otp.AlgorithmSHA512}
	algorithm, ok := algorithms[c.Algorithm]
	require.True(t, ok, "unknown TOTP algorithm %q", c.Algorithm)
	digits := otp.DigitsSix
	if c.Digits == 8 {
		digits = otp.DigitsEight
	}
	code, err := totp.GenerateCodeCustom(c.TOTPSecret, at, totp.ValidateOpts{Period: c.Period, Digits: digits, Algorithm: algorithm})
	require.NoError(t, err)
	return code
}

// totpClock hands out codes from a step not used before, because Kanidm rejects a reused step.
type totpClock struct {
	now   func() time.Time
	sleep func(time.Duration)
	last  map[string]uint64
}

var clock = &totpClock{now: time.Now, sleep: time.Sleep, last: map[string]uint64{}}

func (k *totpClock) code(t *testing.T, account string, c credentials) string {
	for {
		now := k.now()
		step := uint64(now.Unix()) / uint64(c.Period)
		if step > k.last[account] {
			k.last[account] = step
			return totpCode(t, c, now)
		}
		k.sleep(time.Duration(uint64(c.Period)-uint64(now.Unix())%uint64(c.Period)) * time.Second)
	}
}

// browserAuthorize logs in through Dex and Kanidm and returns the code sent to the smoke client.
func browserAuthorize(t *testing.T, account string, c credentials) (string, bool) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.Flag("host-resolver-rules", "MAP *.127-0-0-1.nip.io "+sandboxIngress),
		// NixOS-WSL exports an LD_LIBRARY_PATH built for a newer glibc than the dev shell's chromium
		chromedp.Env("LD_LIBRARY_PATH="),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	ctx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	ctx, cancelTimeout := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelTimeout()

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

	authURL := dexURL + "/auth?" + url.Values{
		"client_id":     {"smoke"},
		"redirect_uri":  {smokeRedirect},
		"response_type": {"code"},
		"scope":         {"openid email profile groups"},
		"state":         {"identity-test"},
	}.Encode()

	// Start the browser on the long-lived context; the first Run owns the browser's lifetime
	require.NoError(t, chromedp.Run(ctx))
	step := func(name string, actions ...chromedp.Action) {
		stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := chromedp.Run(stepCtx, actions...); err != nil {
			var location, text string
			_ = chromedp.Run(ctx, chromedp.Location(&location), chromedp.Evaluate(`document.body ? document.body.innerText.slice(0, 500) : ""`, &text))
			require.NoError(t, err, "login step %q for %s at %s:\n%s", name, account, location, text)
		}
	}
	step("open", network.Enable(), chromedp.Navigate(authURL), chromedp.WaitVisible(`input#username`))
	// Kanidm asks for the TOTP code before the password
	step("username", chromedp.SendKeys(`input#username`, account+"\n"), chromedp.WaitVisible(`input#totp`))
	step("totp", chromedp.SendKeys(`input#totp`, clock.code(t, account, c)+"\n"), chromedp.WaitVisible(`input#password`))
	step("password", chromedp.SendKeys(`input#password`, c.Password+"\n"))

	select {
	case code := <-codes:
		return code, true
	case <-time.After(30 * time.Second):
		return "", false
	}
}

func smokeClientSecret(t *testing.T) string {
	secret := k8s.GetSecret(t, k8s.NewKubectlOptions("", "", "global-secrets"), "dex.smoke")
	return string(secret.Data["client_secret"])
}

// exchangeCode trades the code for tokens at Dex and returns the ID token claims.
func exchangeCode(t *testing.T, code, secret string) map[string]any {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {smokeRedirect}}
	req, err := http.NewRequest(http.MethodPost, dexURL+"/token", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.SetBasicAuth("smoke", secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := insecureClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "token exchange: %s", raw)

	var tokens struct {
		IDToken string `json:"id_token"`
	}
	require.NoError(t, json.Unmarshal(raw, &tokens))
	parts := strings.Split(tokens.IDToken, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	return claims
}

// hasGroup accepts Kanidm's short group names and SPNs (name@domain).
func hasGroup(claims map[string]any, group string) bool {
	groups, _ := claims["groups"].([]any)
	for _, g := range groups {
		if s, ok := g.(string); ok && (s == group || strings.HasPrefix(s, group+"@")) {
			return true
		}
	}
	return false
}
