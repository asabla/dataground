package main

import "testing"

func TestRunRejectsMixedProviderDPoPIssuanceModes(t *testing.T) {
	t.Parallel()
	for name, arguments := range map[string][]string{
		"missing trust": {
			"-verify-file", "/run/dataground/provider-issuance.json",
		},
		"mixed verify and statement": {
			"-trust-file", "/run/dataground/provider-issuance-trust.json",
			"-verify-file", "/run/dataground/provider-issuance.json",
			"-statement-file", "/run/dataground/provider-issuance-statement.json",
		},
		"mixed prepare and signature": {
			"-trust-file", "/run/dataground/provider-issuance-trust.json",
			"-statement-file", "/run/dataground/provider-issuance-statement.json",
			"-signature-file", "/run/dataground/provider-issuance-signature.json",
			"-signing-message-file", "/run/dataground/provider-issuance-message.bin",
		},
		"incomplete install": {
			"-trust-file", "/run/dataground/provider-issuance-trust.json",
			"-statement-file", "/run/dataground/provider-issuance-statement.json",
		},
		"positional argument": {
			"-trust-file", "/run/dataground/provider-issuance-trust.json",
			"unexpected",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(arguments); err == nil {
				t.Fatal("invalid provider DPoP issuance arguments were accepted")
			}
		})
	}
}
