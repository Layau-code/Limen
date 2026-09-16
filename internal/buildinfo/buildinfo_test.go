package buildinfo

import (
	"bytes"
	"testing"
)

func TestWriteJSONUsesStableDevelopmentDefaults(t *testing.T) {
	var output bytes.Buffer
	if err := WriteJSON(&output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), `{"version":"dev","commit":"unknown","build_date":"unknown"}`+"\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
