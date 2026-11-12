package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSignatureSignerParsing(t *testing.T) {
	for _, tc := range []struct{ input, role, email, name string }{
		{"Signer=signer@example.com:Example", "Signer", "signer@example.com", "Example"},
		{`{"role":"A=B","email":"signer@example.com","name":"Example: Signer"}`, "A=B", "signer@example.com", "Example: Signer"},
	} {
		v, err := parseSignatureSigner(tc.input)
		require.NoError(t, err)
		require.Equal(t, tc.role, v.Role)
		require.Equal(t, tc.email, v.Email)
		require.Equal(t, tc.name, v.Name)
	}
	for _, raw := range []string{`{"role":"Signer","email":"a@example.com","Email":"b@example.com"}`, `{"role":"Signer","role":"Other","email":"a@example.com"}`, `{"role":null,"email":"a@example.com"}`, "Signer=Name <a@example.com>"} {
		_, err := parseSignatureSigner(raw)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "a@example.com")
	}
}
