package link

import "testing"

func TestParseRoundTripDoesNotImportAnotherInbound(t *testing.T) {
	a := Inbound{
		ID: 1, Remark: "a", Protocol: "vless", Port: 443, Host: "203.0.113.10",
		Transport: TCP, Security: Reality, SNI: "www.samsung.com",
		PublicKey: "PUB_A", ShortID: "sidA", Fingerprint: "chrome",
	}
	b := Inbound{
		ID: 2, Remark: "b", Protocol: "vless", Port: 8443, Host: "203.0.113.20",
		Transport: TCP, Security: Reality, SNI: "www.microsoft.com",
		PublicKey: "PUB_B", ShortID: "sidB", Fingerprint: "chrome",
	}
	raw, err := Build(a, Client{UUID: "11111111-1111-1111-1111-111111111111", Email: "a@hami"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicKey != "PUB_A" || got.SNI != "www.samsung.com" || got.ShortID != "sidA" || got.Port != 443 {
		t.Fatalf("parsed: %+v", got)
	}
	other, err := Build(b, Client{UUID: "22222222-2222-2222-2222-222222222222"})
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicKey == "PUB_B" || contains(raw, "PUB_B") || contains(other, "PUB_A") {
		t.Fatal("reality parameters crossed between inbounds")
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || len(s) > 0 && index(s, sub) >= 0)
}

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
