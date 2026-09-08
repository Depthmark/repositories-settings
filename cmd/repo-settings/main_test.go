package main

import (
	"reflect"
	"testing"
)

func TestParseTrustedJWKSHosts(t *testing.T) {
	got, err := parseTrustedJWKSHosts("https://issuer.example=keys.example,backup.example\nhttps://other.example=other-keys.example")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"https://issuer.example": {"keys.example", "backup.example"},
		"https://other.example":  {"other-keys.example"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, value := range []string{"missing-equals", "=host", "issuer=", "issuer=host,", "issuer=host,other=host"} {
		if _, err := parseTrustedJWKSHosts(value); err == nil {
			t.Errorf("invalid exception %q accepted", value)
		}
	}
}
