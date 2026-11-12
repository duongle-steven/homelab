package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	matrixHost = "matrix." + suiteDomain
	chatHost   = "chat." + suiteDomain
)

// Matrix (tuwunel and FluffyChat, apps/matrix.yaml): an account signs in through Kanidm only, then chats
var _ = Describe("Matrix", Label("sandbox", "matrix"), func() {
	base := "https://" + matrixHost
	waitForIdentity()
	waitForAppBeforeEach(base + "/_matrix/client/versions")

	It("signs in through Kanidm only and sends a message", func(ctx SpecContext) {
		// FluffyChat's page: its image may still be pulling on a fresh sync
		Eventually(ctx, func() int { return getJSON(ctx, "GET", "https://"+chatHost+"/config.json", nil, "", "", nil, nil) }).
			WithTimeout(10*time.Minute).WithPolling(10*time.Second).Should(Equal(http.StatusOK), "FluffyChat does not answer")

		// No password login: SSO alone
		var flows struct {
			Flows []struct {
				Type string `json:"type"`
			} `json:"flows"`
		}
		Expect(getJSON(ctx, "GET", base+"/_matrix/client/v3/login", nil, "", "", nil, &flows)).To(Equal(http.StatusOK))
		var types []string
		for _, f := range flows.Flows {
			types = append(types, f.Type)
		}
		Expect(types).To(ContainElement("m.login.sso"))
		Expect(types).NotTo(ContainElement("m.login.password"))

		// Kanidm's form, then tuwunel's redirect back with a login token. To a static file of the client: FluffyChat's page
		// reads the token and drops it from the address before the spec can
		account := "dev-admin"
		back := "https://" + chatHost + "/config.json"
		start := base + "/_matrix/client/v3/login/sso/redirect/matrix?redirectUrl=" + url.QueryEscape(back)
		browser, cancel := newBrowser(ctx)
		defer cancel()
		openLogin(browser, account, start)
		credentials := ensureCredentials(ctx, account)
		kanidmLogin(browser, account, credentials, credentials.Password)
		var token, location string
		Eventually(browser, func() string {
			if chromedp.Run(browser, chromedp.Location(&location)) != nil {
				return ""
			}
			acceptConsent(browser)
			// tuwunel asks the person to confirm where the login goes (against phishing): click Continue, as they would
			if u, err := url.Parse(location); err == nil && u.Host == matrixHost && strings.Contains(u.Path, "/sso/callback/") {
				var found bool
				_ = chromedp.Run(browser, chromedp.Evaluate(`(() => { const b = [...document.querySelectorAll("button, a, input[type=submit]")]
					.find(e => (e.innerText || e.value || "").trim() === "Continue"); if (b) { b.click(); return true } return false })()`, &found))
				return ""
			}
			if u, err := url.Parse(location); err == nil && u.Host == chatHost {
				token = u.Query().Get("loginToken")
			}
			return token
		}).WithTimeout(60*time.Second).WithPolling(500*time.Millisecond).ShouldNot(BeEmpty(), func() string {
			var text string
			_ = chromedp.Run(browser, chromedp.Evaluate(`document.body ? document.body.innerText.slice(0, 600) : ""`, &text))
			return fmt.Sprintf("no loginToken back on %s; last at %s:\n%s", chatHost, location, text)
		})

		var login struct {
			AccessToken string `json:"access_token"`
			UserID      string `json:"user_id"`
		}
		Expect(getJSON(ctx, "POST", base+"/_matrix/client/v3/login", nil, "", "",
			map[string]any{"type": "m.login.token", "token": token}, &login)).To(Equal(http.StatusOK))
		Expect(login.AccessToken).NotTo(BeEmpty())
		Expect(login.UserID).To(Equal("@" + account + ":" + matrixHost))
		auth := []string{"Authorization", "Bearer " + login.AccessToken}

		// A room, a message, read back
		var room struct {
			RoomID string `json:"room_id"`
		}
		Expect(getJSON(ctx, "POST", base+"/_matrix/client/v3/createRoom", nil, "", "",
			map[string]any{"preset": "private_chat"}, &room, auth...)).To(Equal(http.StatusOK))
		Expect(room.RoomID).NotTo(BeEmpty())
		marker := fmt.Sprint("matrix-", time.Now().UnixNano())
		txn := fmt.Sprint(time.Now().UnixNano())
		Expect(getJSON(ctx, "PUT", base+"/_matrix/client/v3/rooms/"+url.PathEscape(room.RoomID)+"/send/m.room.message/"+txn, nil, "", "",
			map[string]any{"msgtype": "m.text", "body": marker}, nil, auth...)).To(Equal(http.StatusOK))
		var messages struct {
			Chunk []struct {
				Content struct {
					Body string `json:"body"`
				} `json:"content"`
			} `json:"chunk"`
		}
		Expect(getJSON(ctx, "GET", base+"/_matrix/client/v3/rooms/"+url.PathEscape(room.RoomID)+"/messages?dir=b&limit=10", nil, "", "",
			nil, &messages, auth...)).To(Equal(http.StatusOK))
		var bodies []string
		for _, m := range messages.Chunk {
			bodies = append(bodies, m.Content.Body)
		}
		Expect(strings.Join(bodies, "\n")).To(ContainSubstring(marker))
	}, SpecTimeout(15*time.Minute))
})
