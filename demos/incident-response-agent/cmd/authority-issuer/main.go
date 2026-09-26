package main

import (
	"fmt"
	"os"

	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/authority"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/jsonio"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "issue" {
		fmt.Fprintln(os.Stderr, "usage: incident-authority-issuer issue")
		os.Exit(2)
	}
	var request authority.IssueRequest
	if err := jsonio.Decode(os.Stdin, &request); err != nil {
		fmt.Fprintln(os.Stderr, "invalid issuance request")
		os.Exit(2)
	}
	result, err := authority.IssueEphemeral(request)
	if err != nil {
		fmt.Fprintln(os.Stderr, "authority issuance failed")
		os.Exit(1)
	}
	if err = jsonio.Encode(os.Stdout, result); err != nil {
		fmt.Fprintln(os.Stderr, "authority output failed")
		os.Exit(1)
	}
}
