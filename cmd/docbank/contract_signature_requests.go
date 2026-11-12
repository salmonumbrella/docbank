package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"go.kenn.io/docbank/internal/api"
	"go.kenn.io/docbank/internal/daemonconn"
	"go.kenn.io/docbank/internal/store"
)

func parseSignatureSigner(raw string) (store.SignatureSignerInput, error) {
	invalid := errors.New("invalid signer; use ROLE=EMAIL[:NAME] or signer JSON")
	if strings.HasPrefix(raw, "{") {
		var m map[string]jsontext.Value
		if json.Unmarshal([]byte(raw), &m) != nil {
			return store.SignatureSignerInput{}, invalid
		}
		for k, v := range m {
			if (k != "role" && k != "email" && k != "name" && k != "$schema") || string(v) == "null" {
				return store.SignatureSignerInput{}, invalid
			}
		}
		var out store.SignatureSignerInput
		if json.Unmarshal([]byte(raw), &out) != nil || !store.ValidSignatureSigner(out) {
			return store.SignatureSignerInput{}, invalid
		}
		return out, nil
	}
	role, value, ok := strings.Cut(raw, "=")
	if !ok {
		return store.SignatureSignerInput{}, invalid
	}
	email, name, _ := strings.Cut(value, ":")
	out := store.SignatureSignerInput{Role: role, Name: name, Email: email}
	if !store.ValidSignatureSigner(out) {
		return store.SignatureSignerInput{}, invalid
	}
	return out, nil
}
func printSignatureRequest(cmd *cobra.Command, v store.SignatureRequest, asJSON bool) error {
	if asJSON {
		return writeCLIJSON(cmd.OutOrStdout(), v)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\trevision %d\n", v.RequestID, v.State, v.Revision); err != nil {
		return fmt.Errorf("write signature request: %w", err)
	}
	for _, s := range v.Signers {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%q\t%q\t%q\t%s\n", s.Role, s.Name, s.Email, s.State); err != nil {
			return fmt.Errorf("write signer: %w", err)
		}
	}
	return nil
}
func newSignatureRequestCommand() *cobra.Command {
	var template, revision int64
	var signers []string
	var asJSON bool
	cmd := &cobra.Command{Use: "request <contract-id>", Short: "Send a template for signature", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if template <= 0 || cmd.Flags().Changed("revision") && revision <= 0 {
			return errors.New("template and revision must be positive")
		}
		inputs := make([]store.SignatureSignerInput, 0, len(signers))
		for _, raw := range signers {
			v, err := parseSignatureSigner(raw)
			if err != nil {
				return err
			}
			inputs = append(inputs, v)
		}
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("revision") {
			record, e := c.Contract(cmd.Context(), args[0], "")
			if e != nil {
				return e
			}
			revision = record.Revision
		}
		v, err := c.CreateSignatureRequest(cmd.Context(), args[0], revision, api.SignatureRequestBody{TemplateID: template, Signers: inputs})
		if err != nil {
			if id := daemonconn.SignatureRequestErrorID(err); id != "" {
				return fmt.Errorf("signature request %s: outcome uncertain or failed; inspect it before retrying", id)
			}
			return contractMutationError("signature request", err)
		}
		return printSignatureRequest(cmd, v, asJSON)
	}}
	cmd.Flags().Int64Var(&template, "template", 0, "DocuSeal template ID")
	cmd.Flags().StringArrayVar(&signers, "signer", nil, "ROLE=EMAIL[:NAME] or signer JSON (repeatable)")
	cmd.Flags().Int64Var(&revision, "revision", 0, "expected contract revision")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}
func newSignatureStatusCommand() *cobra.Command {
	var request, asJSON bool
	var limit, offset int
	cmd := &cobra.Command{Use: "status <contract-id|request-id>", Short: "Inspect local signature history", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := daemonconn.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		if request {
			v, e := c.SignatureRequest(cmd.Context(), args[0])
			if e != nil {
				return e
			}
			return printSignatureRequest(cmd, v, asJSON)
		}
		page, err := c.SignatureRequests(cmd.Context(), args[0], limit, offset)
		if err != nil {
			return err
		}
		if asJSON {
			return writeCLIJSON(cmd.OutOrStdout(), page)
		}
		for _, v := range page.Items {
			if _, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%q\trevision %d\n", v.RequestID, v.State, v.TemplateName, v.Revision); err != nil {
				return fmt.Errorf("write signature history: %w", err)
			}
		}
		return nil
	}}
	cmd.Flags().BoolVar(&request, "request", false, "show a single request")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum requests")
	cmd.Flags().IntVar(&offset, "offset", 0, "history offset")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}
