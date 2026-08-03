package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/asabla/dataground/internal/auditseal"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround audit export sealing failed")
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("dataground-audit-export-seal", flag.ContinueOnError)
	flags.SetOutput(ioDiscard{})
	exportFile := flags.String("export-file", "", "canonical authorization or operator audit export")
	signatureFile := flags.String("signature-file", "", "detached Ed25519 signature envelope")
	trustFile := flags.String("trust-file", "", "pinned audit export trust profile")
	outputFile := flags.String("output-file", "", "new sealed audit export envelope")
	verifyFile := flags.String("verify-file", "", "sealed audit export envelope to verify")
	signingMessageFile := flags.String("signing-message-file", "", "new exact message for an external Ed25519 signer")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || *trustFile == "" {
		return errors.New("audit export sealing arguments are invalid")
	}
	if *verifyFile != "" {
		if *exportFile != "" || *signatureFile != "" || *outputFile != "" || *signingMessageFile != "" {
			return errors.New("audit export verification arguments are invalid")
		}
		_, err := auditseal.VerifyFile(*verifyFile, *trustFile)
		return err
	}
	if *signingMessageFile != "" {
		if *exportFile == "" || *signatureFile != "" || *outputFile != "" {
			return errors.New("audit export signing-message arguments are invalid")
		}
		return auditseal.PrepareSigningMessage(auditseal.PrepareRequest{
			ExportFile:         *exportFile,
			TrustProfileFile:   *trustFile,
			SigningMessageFile: *signingMessageFile,
		})
	}
	if *exportFile == "" || *signatureFile == "" || *outputFile == "" {
		return errors.New("audit export sealing arguments are incomplete")
	}
	return auditseal.Install(auditseal.InstallRequest{
		ExportFile:       *exportFile,
		SignatureFile:    *signatureFile,
		TrustProfileFile: *trustFile,
		OutputFile:       *outputFile,
	})
}

type ioDiscard struct{}

func (ioDiscard) Write(content []byte) (int, error) { return len(content), nil }
