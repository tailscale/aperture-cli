package config

import "testing"

func TestParseEndpointURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "bare host assumes http", in: "ai", want: "http://ai"},
		{name: "trailing slash trimmed", in: "http://ai/", want: "http://ai"},
		{name: "surrounding space trimmed", in: "  http://ai  ", want: "http://ai"},
		{name: "https preserved", in: "https://aperture.example.ts.net", want: "https://aperture.example.ts.net"},
		{name: "empty", in: "   ", wantErr: true},
		{name: "scheme only", in: "http://", wantErr: true},
		{name: "unsupported scheme", in: "ftp://ai", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEndpointURL(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseEndpointURL(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseEndpointURL(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseEndpointURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Two kinds, told apart by the type and not by an empty field, and the same
// file shape as before for both.
func TestEndpointKindsRoundTripThroughSettings(t *testing.T) {
	s := Settings{Endpoints: []Endpoint{Direct("http://ai"), Bridged("http://ai", "bridge-abcdef")}}
	data, err := s.Endpoints[1].(BridgeEndpoint).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"url":"http://ai","bridgeId":"bridge-abcdef"}` {
		t.Errorf("bridge endpoint = %s", data)
	}
	var list endpointList
	if err := list.UnmarshalJSON([]byte(`[{"url":"http://ai"},{"url":"http://ai","bridgeId":"bridge-abcdef"}]`)); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0] != Direct("http://ai") || list[1] != Bridged("http://ai", "bridge-abcdef") {
		t.Errorf("decoded = %#v", list)
	}
	if Direct("http://ai") == Endpoint(Bridged("http://ai", "")) {
		t.Error("a direct endpoint compared equal to a bridged one")
	}
	if got := Bridged("http://old", "bridge-abcdef").WithURL("http://new"); got != Bridged("http://new", "bridge-abcdef") {
		t.Errorf("WithURL = %#v", got)
	}
}
