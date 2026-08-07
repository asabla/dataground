package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/asabla/dataground/internal/releasecert"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround provider DPoP issuance certification failed")
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("dataground-provider-dpop-issuance", flag.ContinueOnError)
	flags.SetOutput(ioDiscard{})
	statementFile := flags.String("statement-file", "", "canonical provider DPoP issuance statement")
	signatureFile := flags.String("signature-file", "", "detached Ed25519 signature envelope")
	trustFile := flags.String("trust-file", "", "pinned issuance-certification trust profile")
	outputFile := flags.String("output-file", "", "new installed issuance-certification envelope")
	verifyFile := flags.String("verify-file", "", "installed issuance-certification envelope to verify")
	signingMessageFile := flags.String("signing-message-file", "", "new exact message for an external Ed25519 signer")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || *trustFile == "" {
		return errors.New("provider DPoP issuance certification arguments are invalid")
	}
	now := time.Now().UTC()
	if *verifyFile != "" {
		if *statementFile != "" || *signatureFile != "" || *outputFile != "" || *signingMessageFile != "" {
			return errors.New("provider DPoP issuance verification arguments are invalid")
		}
		_, err := releasecert.VerifyProviderDPoPIssuanceFile(*verifyFile, *trustFile, now)
		return err
	}
	if *signingMessageFile != "" {
		if *statementFile == "" || *signatureFile != "" || *outputFile != "" {
			return errors.New("provider DPoP issuance signing-message arguments are invalid")
		}
		return releasecert.PrepareProviderDPoPIssuanceSigningMessage(
			releasecert.ProviderDPoPIssuancePrepareRequest{
				StatementFile: *statementFile, TrustProfileFile: *trustFile,
				SigningMessageFile: *signingMessageFile, Now: now,
			},
		)
	}
	if *statementFile == "" || *signatureFile == "" || *outputFile == "" {
		return errors.New("provider DPoP issuance installation arguments are incomplete")
	}
	return releasecert.InstallProviderDPoPIssuance(releasecert.ProviderDPoPIssuanceInstallRequest{
		StatementFile: *statementFile, SignatureFile: *signatureFile,
		TrustProfileFile: *trustFile, OutputFile: *outputFile, Now: now,
	})
}

type ioDiscard struct{}

func (ioDiscard) Write(content []byte) (int, error) { return len(content), nil }
