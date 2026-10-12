package cmd

import (
	"encoding/json"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

func TestParseToolOptions(t *testing.T) {
	tests := []struct {
		name    string
		rawJSON string
		pairs   []string
		want    string
		wantErr bool
	}{
		{name: "text stays text", pairs: []string{"person_name=Ada Example"}, want: `{"person_name":"Ada Example"}`},
		{name: "a number is a number", pairs: []string{"limit=3"}, want: `{"limit":3}`},
		{name: "a list is a list", pairs: []string{`domains=["a.com","b.com"]`}, want: `{"domains":["a.com","b.com"]}`},
		{name: "a quoted number stays text", pairs: []string{`zip="01234"`}, want: `{"zip":"01234"}`},
		{name: "a leading zero is text, not an invalid number", pairs: []string{"zip=01234"}, want: `{"zip":"01234"}`},
		{name: "an equals sign in the value survives", pairs: []string{"q=a=b"}, want: `{"q":"a=b"}`},
		{name: "--opt overrides --options", rawJSON: `{"ticker":"AAPL","limit":5}`, pairs: []string{"ticker=MSFT"}, want: `{"limit":5,"ticker":"MSFT"}`},
		{name: "--options keeps its number literals", rawJSON: `{"ratio":0.10}`, want: `{"ratio":0.10}`},
		{name: "nothing is an empty object", want: `{}`},
		{name: "a pair without an equals sign", pairs: []string{"ticker"}, wantErr: true},
		{name: "a pair without a key", pairs: []string{"=MSFT"}, wantErr: true},
		{name: "--options that is not an object", rawJSON: `["a"]`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseToolOptions(tc.rawJSON, tc.pairs)
			if tc.wantErr {
				if !isCode(err, errs.CodeInvalidArgs) {
					t.Fatalf("err = %v, want invalid_args", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseToolOptions: %v", err)
			}
			b, _ := json.Marshal(got)
			if string(b) != tc.want {
				t.Errorf("options = %s, want %s", b, tc.want)
			}
		})
	}
}
