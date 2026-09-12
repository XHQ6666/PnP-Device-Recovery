package main

import (
	"regexp"
	"strings"
)

// Matcher matches device friendly names against a config entry.
// Exact match unless friendly_name starts with "regex:" (Go RE2).
type Matcher struct {
	exact  string
	regex  *regexp.Regexp
	isRE   bool
	raw    string
	cfgIdx int
}

// NewMatcher builds a Matcher from a DeviceConfig. Assumes Validate already ran.
func NewMatcher(cfg DeviceConfig, index int) (*Matcher, error) {
	name := strings.TrimSpace(cfg.FriendlyName)
	m := &Matcher{raw: name, cfgIdx: index}
	if strings.HasPrefix(name, "regex:") {
		pattern := strings.TrimPrefix(name, "regex:")
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		m.regex = re
		m.isRE = true
		return m, nil
	}
	m.exact = name
	return m, nil
}

// Match returns true if friendlyName matches this entry.
func (m *Matcher) Match(friendlyName string) bool {
	if m.isRE {
		return m.regex.MatchString(friendlyName)
	}
	return friendlyName == m.exact
}

// Pattern returns the original pattern string for logging.
func (m *Matcher) Pattern() string {
	return m.raw
}

// Index returns the config device index.
func (m *Matcher) Index() int {
	return m.cfgIdx
}

// BuildMatchers creates matchers for all device configs.
func BuildMatchers(devices []DeviceConfig) ([]*Matcher, error) {
	out := make([]*Matcher, 0, len(devices))
	for i, d := range devices {
		m, err := NewMatcher(d, i)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
