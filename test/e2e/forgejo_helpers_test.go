package e2e

import (
	"context"
	"fmt"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// forgejoAs calls Forgejo's API as the site admin acting as user (Forgejo's Sudo header; "" acts as the admin)
func forgejoAs(ctx context.Context, user, method, path string, body, out any) int {
	GinkgoHelper()
	admin, password := secretValue(ctx, "forgejo", "forgejo-admin", "username"), secretValue(ctx, "forgejo", "forgejo-admin", "password")
	headers := []string{}
	if user != "" {
		headers = append(headers, "Sudo", user)
	}
	return getJSON(ctx, method, "https://"+forgejoHost+"/api/v1"+path, nil, admin, password, body, out, headers...)
}

// teamMembers lists the members of org ci's team
func teamMembers(ctx context.Context, team string) []string {
	GinkgoHelper()
	var teams []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	Expect(forgejoAs(ctx, "", "GET", "/orgs/ci/teams", nil, &teams)).To(Equal(http.StatusOK))
	for _, tm := range teams {
		if tm.Name == team {
			var members []struct {
				Login string `json:"login"`
			}
			Expect(forgejoAs(ctx, "", "GET", fmt.Sprintf("/teams/%d/members", tm.ID), nil, &members)).To(Equal(http.StatusOK))
			var names []string
			for _, m := range members {
				names = append(names, m.Login)
			}
			return names
		}
	}
	Fail("no team: org ci has no team " + team)
	return nil
}
