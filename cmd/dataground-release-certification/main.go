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
		fmt.Fprintln(os.Stderr, "DataGround release certification failed")
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("dataground-release-certification", flag.ContinueOnError)
	flags.SetOutput(ioDiscard{})
	statementFile := flags.String("statement-file", "", "canonical release certification statement")
	signatureFile := flags.String("signature-file", "", "detached Ed25519 signature envelope")
	trustFile := flags.String("trust-file", "", "pinned release certification trust profile")
	outputFile := flags.String("output-file", "", "new installed certification envelope")
	verifyFile := flags.String("verify-file", "", "installed certification envelope to verify")
	signingMessageFile := flags.String("signing-message-file", "", "new exact message for an external Ed25519 signer")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || *trustFile == "" {
		return errors.New("release certification arguments are invalid")
	}
	revision, goVersion, err := releasecert.CurrentBuild()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if *verifyFile != "" {
		if *statementFile != "" || *signatureFile != "" || *outputFile != "" || *signingMessageFile != "" {
			return errors.New("release certification verification arguments are invalid")
		}
		_, err := releasecert.VerifyFile(*verifyFile, *trustFile, revision, goVersion, now)
		return err
	}
	if *signingMessageFile != "" {
		if *statementFile == "" || *signatureFile != "" || *outputFile != "" {
			return errors.New("release certification signing-message arguments are invalid")
		}
		return releasecert.PrepareSigningMessage(releasecert.PrepareRequest{
			StatementFile:      *statementFile,
			TrustProfileFile:   *trustFile,
			SigningMessageFile: *signingMessageFile,
			SourceRevision:     revision,
			GoVersion:          goVersion,
			Now:                now,
		})
	}
	if *statementFile == "" || *signatureFile == "" || *outputFile == "" {
		return errors.New("release certification installation arguments are incomplete")
	}
	return releasecert.Install(releasecert.InstallRequest{
		StatementFile:    *statementFile,
		SignatureFile:    *signatureFile,
		TrustProfileFile: *trustFile,
		OutputFile:       *outputFile,
		SourceRevision:   revision,
		GoVersion:        goVersion,
		Now:              now,
	})
}

type ioDiscard struct{}

func (ioDiscard) Write(content []byte) (int, error) { return len(content), nil }
