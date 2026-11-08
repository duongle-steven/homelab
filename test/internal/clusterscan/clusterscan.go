// Package clusterscan decides a scan's report against .cluster-scan-accepted.yaml: what just testbed-scan and just
// dev-scan compare a tool's report with (docs/how-to-guides/cluster-scan.md). Reports hold command lines and config:
// nothing here names more than a check, a control and a resource.
package clusterscan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The scan must have seen the cluster: the spike's sandbox had 1307 resources
const MinResources = 500

// A resource the sandbox always has and kubescape always reports on
const KnownResource = "istio-system/DaemonSet/ztunnel"

// Accepted only by resource, so a new workload that may do these fails the target: the spec's thirteen (cluster-admin,
// wildcards, impersonation, bind and escalate, privileged, host path, network and PID, capabilities, credentials in
// configuration), then who may read every Secret, exec, create pods or volumes, port-forward or reach the kubelets'
// proxy, and the workloads holding those roles
var Dangerous = []string{"C-0185", "C-0187", "C-0035", "C-0065", "C-0191", "C-0057", "C-0045", "C-0048", "C-0041", "C-0038", "C-0046", "C-0012", "C-0259",
	"C-0002", "C-0015", "C-0016", "C-0044", "C-0063", "C-0186", "C-0188", "C-0256", "C-0262", "C-0267", "C-0272", "C-0275",
	"C-0278", "C-0279", "C-0280", "C-0281", "C-0282", "C-0307"}

// What a name pattern keeps once its wildcards and classes are gone: too little of it matches nearly every name
var PatternParts = regexp.MustCompile(`\[[^\]]*\]|[*?\\]`)

type Check struct {
	Check  string `yaml:"check"`
	Reason string `yaml:"reason"`
}

type Control struct {
	ID     string `yaml:"id"`
	Reason string `yaml:"reason"`
}

// Resource accepts each of its controls on each of its resources, for one reason
type Resource struct {
	IDs       []string `yaml:"ids"`
	Resources []string `yaml:"resources"`
	Reason    string   `yaml:"reason"`
}

type Accepted struct {
	KubeBench []Check `yaml:"kubeBench"`
	Kubescape struct {
		Controls  []Control  `yaml:"controls"`
		Resources []Resource `yaml:"resources"`
	} `yaml:"kubescape"`
}

func LoadScanAccepted(raw []byte) (Accepted, error) {
	var accepted Accepted
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&accepted); err != nil {
		return accepted, fmt.Errorf("accepted findings: %w", err)
	}
	for _, c := range accepted.KubeBench {
		if c.Check == "" || strings.TrimSpace(c.Reason) == "" {
			return accepted, fmt.Errorf("kubeBench check %q needs a number and a reason", c.Check)
		}
	}
	for _, c := range accepted.Kubescape.Controls {
		if c.ID == "" || strings.TrimSpace(c.Reason) == "" {
			return accepted, fmt.Errorf("kubescape control %q needs an id and a reason", c.ID)
		}
		if slices.Contains(Dangerous, c.ID) {
			return accepted, fmt.Errorf("kubescape control %s is dangerous: accept it by resource", c.ID)
		}
	}
	for _, r := range accepted.Kubescape.Resources {
		if len(r.IDs) == 0 || len(r.Resources) == 0 || strings.TrimSpace(r.Reason) == "" {
			return accepted, fmt.Errorf("kubescape resources %q of %q need ids, resources and a reason", r.Resources, r.IDs)
		}
		for _, resource := range r.Resources {
			fields := strings.Split(resource, "/")
			if _, err := path.Match(resource, ""); err != nil || len(fields) != 3 ||
				strings.ContainsAny(fields[0]+fields[1], `*?[\`) || (fields[2] != PatternParts.ReplaceAllString(fields[2], "") && len(PatternParts.ReplaceAllString(fields[2], "")) < 3) {
				return accepted, fmt.Errorf("kubescape resource %q must be <namespace>/<Kind>/<name>, with a pattern only inside the name", resource)
			}
		}
	}
	return accepted, nil
}

// UndecidedBench returns the FAILs of one node's kube-bench report that nobody decided on. It refuses a report that
// did not really check: a section missing or empty, or a check whose audit command was not found (the image has no
// journalctl unless the node's /nix is mounted, and every k3s check calls it)
func UndecidedBench(report []byte, accepted Accepted) ([]string, error) {
	var parsed struct {
		Controls []struct {
			ID    string `json:"id"`
			Tests []struct {
				Results []struct {
					Number      string `json:"test_number"`
					Status      string `json:"status"`
					Reason      string `json:"reason"`
					ActualValue string `json:"actual_value"`
				} `json:"results"`
			} `json:"tests"`
		} `json:"Controls"`
	}
	if err := json.Unmarshal(report, &parsed); err != nil {
		return nil, fmt.Errorf("kube-bench report: %w", err)
	}
	checked := map[string]int{}
	undecided := []string{}
	for _, control := range parsed.Controls {
		for _, test := range control.Tests {
			for _, result := range test.Results {
				checked[control.ID]++
				if strings.Contains(result.Reason, ": not found") || strings.Contains(result.ActualValue, ": not found") {
					return nil, fmt.Errorf("kube-bench check %s: its audit command was not found", result.Number)
				}
				if result.Status == "FAIL" && !slices.ContainsFunc(accepted.KubeBench, func(c Check) bool { return c.Check == result.Number }) {
					undecided = append(undecided, result.Number)
				}
			}
		}
	}
	for _, section := range []string{"1", "2", "3", "4", "5"} {
		if checked[section] == 0 {
			return nil, fmt.Errorf("kube-bench report: section %s did not run", section)
		}
	}
	slices.Sort(undecided)
	return slices.Compact(undecided), nil
}

// ResourceKey is <namespace>/<Kind>/<name>, the last three fields of a resourceID: kubescape prefixes an API
// group and version, and names a binding by its subject and role first. A cluster-scoped resource starts with "/"
func ResourceKey(resourceID string) string {
	fields := strings.Split(resourceID, "/")
	if len(fields) < 3 {
		return resourceID
	}
	return strings.Join(fields[len(fields)-3:], "/")
}

// UndecidedKubescape returns "<control> <resource>" for each failure nobody decided on. It refuses a report that did
// not see the cluster: too few resources, or a known one missing
func UndecidedKubescape(report []byte, accepted Accepted) ([]string, error) {
	var parsed struct {
		SummaryDetails struct {
			ResourceCounters struct {
				Passed   int `json:"passedResources"`
				Failed   int `json:"failedResources"`
				Skipped  int `json:"skippedResources"`
				Excluded int `json:"excludedResources"`
			} `json:"ResourceCounters"`
		} `json:"summaryDetails"`
		Results []struct {
			ResourceID string `json:"resourceID"`
			Controls   []struct {
				ControlID string `json:"controlID"`
				Status    struct {
					Status string `json:"status"`
				} `json:"status"`
			} `json:"controls"`
		} `json:"results"`
	}
	if err := json.Unmarshal(report, &parsed); err != nil {
		return nil, fmt.Errorf("kubescape report: %w", err)
	}
	counters := parsed.SummaryDetails.ResourceCounters
	if seen := counters.Passed + counters.Failed + counters.Skipped + counters.Excluded; seen < MinResources {
		return nil, fmt.Errorf("kubescape report: saw %d resources, fewer than %d", seen, MinResources)
	}
	known := false
	undecided := []string{}
	for _, result := range parsed.Results {
		resource := ResourceKey(result.ResourceID)
		known = known || resource == KnownResource
		for _, control := range result.Controls {
			if control.Status.Status != "failed" {
				continue
			}
			if slices.ContainsFunc(accepted.Kubescape.Controls, func(c Control) bool { return c.ID == control.ControlID }) {
				continue
			}
			if slices.ContainsFunc(accepted.Kubescape.Resources, func(r Resource) bool {
				return slices.Contains(r.IDs, control.ControlID) && slices.ContainsFunc(r.Resources, func(pattern string) bool {
					matched, _ := path.Match(pattern, resource)
					return matched
				})
			}) {
				continue
			}
			undecided = append(undecided, control.ControlID+" "+resource)
		}
	}
	if !known {
		return nil, fmt.Errorf("kubescape report: %s is not in the results", KnownResource)
	}
	slices.Sort(undecided)
	return slices.Compact(undecided), nil
}

// SecretIsEncrypted checks a Secret's raw value in etcd: k3s's provider prefix, and no trace of the plain value
func SecretIsEncrypted(raw []byte, value string) error {
	if value == "" {
		return errors.New("no value to look for")
	}
	if !bytes.HasPrefix(raw, []byte("k8s:enc:aescbc:")) {
		return errors.New("the value in etcd lacks the prefix k8s:enc:aescbc:")
	}
	if bytes.Contains(raw, []byte(value)) {
		return errors.New("the plain value is readable in etcd")
	}
	return nil
}
