package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/asabla/dataground/internal/auditseal"
)

type commandRequest struct {
	envelopeFile           string
	exportTrustFile        string
	recipientTrustFile     string
	recipientEncryptionKey string
	outputFile             string
	verifyFile             string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround audit export encryption failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("audit export encryption context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.verifyFile != "" {
		_, err := auditseal.VerifyEncryptedPackageFile(
			request.verifyFile,
			request.envelopeFile,
			request.exportTrustFile,
			request.recipientTrustFile,
		)
		return err
	}
	return auditseal.EncryptFile(auditseal.EncryptRequest{
		EnvelopeFile:              request.envelopeFile,
		ExportTrustProfileFile:    request.exportTrustFile,
		RecipientTrustProfileFile: request.recipientTrustFile,
		EncryptionKeyID:           request.recipientEncryptionKey,
		OutputFile:                request.outputFile,
	})
}

func parseArguments(arguments []string) (commandRequest, error) {
	var request commandRequest
	flags := flag.NewFlagSet("dataground-audit-export-encrypt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&request.envelopeFile, "envelope-file", "", "canonical signed audit export envelope")
	flags.StringVar(&request.exportTrustFile, "trust-file", "", "pinned audit export signing trust profile")
	flags.StringVar(&request.recipientTrustFile, "recipient-trust-file", "", "identity-proven recipient trust profile")
	flags.StringVar(
		&request.recipientEncryptionKey,
		"recipient-encryption-key",
		"",
		"recipient X25519 encryption key identifier",
	)
	flags.StringVar(&request.outputFile, "output-file", "", "new encrypted audit export package")
	flags.StringVar(&request.verifyFile, "verify-file", "", "installed encrypted audit export package")
	if err := flags.Parse(arguments); err != nil {
		return commandRequest{}, errors.New("audit export encryption arguments are invalid")
	}
	if flags.NArg() != 0 || request.envelopeFile == "" || request.exportTrustFile == "" ||
		request.recipientTrustFile == "" {
		return commandRequest{}, errors.New("audit export encryption arguments are incomplete")
	}
	if (request.verifyFile == "" && (request.recipientEncryptionKey == "" || request.outputFile == "")) ||
		(request.verifyFile != "" && (request.recipientEncryptionKey != "" || request.outputFile != "")) {
		return commandRequest{}, errors.New("audit export encryption mode is invalid")
	}
	return request, nil
}
