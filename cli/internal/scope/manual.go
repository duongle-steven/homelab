package scope

import (
	"fmt"
	"regexp"
)

// CheckManual refuses a make dev tests=... regex that would test nothing or run an alone test with the others: a
// term must match at least one test, and only an alone term may match an alone test.
func (c Config) CheckManual(regex string, tests []string) error {
	for _, term := range SplitTerms(regex) {
		re, err := regexp.Compile(term)
		if err != nil {
			return fmt.Errorf("tests: invalid regex %q: %w", term, err)
		}
		if !matchesAny(re, tests) {
			return fmt.Errorf("tests: %q matches no test", term)
		}
		if c.isAlone(term) {
			continue
		}
		for _, test := range tests {
			if c.isAlone(test) && re.MatchString(test) {
				return fmt.Errorf("tests: %q also matches %s, which runs alone: name it on its own", term, test)
			}
		}
	}
	return nil
}
