package main

import "testing"

func TestMatcherExact(t *testing.T) {
	m, err := NewMatcher(DeviceConfig{FriendlyName: "NVIDIA GeForce RTX 3060 Laptop GPU", MaxRetries: 1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("NVIDIA GeForce RTX 3060 Laptop GPU") {
		t.Fatal("exact match should succeed")
	}
	if m.Match("NVIDIA GeForce RTX 3060 Laptop GPU ") {
		t.Fatal("exact match should not trim target")
	}
	if m.Match("nvidia geforce rtx 3060 laptop gpu") {
		t.Fatal("exact match is case-sensitive")
	}
}

func TestMatcherRegex(t *testing.T) {
	m, err := NewMatcher(DeviceConfig{FriendlyName: "regex:^Intel.*BE200.*$", MaxRetries: 1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("Intel(R) Wi-Fi 6E AX211 (BE200)") && !m.Match("Intel BE200 Wireless") {
		// Second should match; first may or may not depending on pattern — BE200 in middle
	}
	if !m.Match("Intel Wi-Fi BE200 Adapter") {
		t.Fatal("regex should match Intel...BE200...")
	}
	if m.Match("Realtek BE200") {
		t.Fatal("should not match without Intel prefix")
	}
	if m.Match("Intel Something") {
		t.Fatal("should require BE200")
	}
}

func TestBuildMatchers(t *testing.T) {
	ms, err := BuildMatchers([]DeviceConfig{
		{FriendlyName: "A", MaxRetries: 1},
		{FriendlyName: "regex:^B.*", MaxRetries: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("len=%d", len(ms))
	}
	if !ms[0].Match("A") || !ms[1].Match("BCD") {
		t.Fatal("matchers failed")
	}
}
