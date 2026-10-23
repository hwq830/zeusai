package gen

import (
	"context"
	"testing"
)

func TestSpecIsValid(t *testing.T) {
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatalf("openapi.yaml is invalid: %v", err)
	}
}
