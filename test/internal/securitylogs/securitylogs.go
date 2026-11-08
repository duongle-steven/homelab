// Package securitylogs holds what the security logs' offline and cluster tests share (step 15c)
package securitylogs

// AuditAlerts: the alerts from logs (the spec's table) and their severities
var AuditAlerts = map[string]string{
	"AuditExecByPerson":           "warning",
	"AuditSecretReadByPerson":     "warning",
	"AuditClusterAdminBinding":    "critical",
	"AuditAnonymousAllowed":       "critical",
	"AuditManyDenied":             "warning",
	"AuditPolicyExceptionChanged": "warning",
	"OpenBaoRootToken":            "critical",
	"OpenBaoManyFailedLogins":     "warning",
	"OpenBaoConfigChanged":        "critical",
	"AuditLogsMissing":            "critical",
}
