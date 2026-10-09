package acceptance_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xraph/relay/acceptance"
)

func TestCanonicalJSON(t *testing.T) {
	a, err := acceptance.CanonicalJSON([]byte(`{"z":9007199254740993,"a":[1.0,100e-2,-0,"\ud83d\ude00"]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := acceptance.CanonicalJSON([]byte(`{"a":[1,1,0,"😀"],"z":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("%s != %s", a, b)
	}
	for _, s := range []string{`{"a":1,"a":2}`, `{"a":1,"\u0061":2}`, `{"x":{"a":1,"a":1}}`, `"\ud800"`, `"\udc00"`, `"\u0000"`, `1 2`, `NaN`, `1e10001`, `{"x":01}`} {
		if _, err := acceptance.CanonicalJSON([]byte(s)); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
	if _, err := acceptance.CanonicalJSON(bytes.Repeat([]byte(" "), acceptance.MaxDataBytes+1)); err == nil {
		t.Fatal("oversized JSON accepted")
	}
}

func TestIdentityAndFingerprintBindings(t *testing.T) {
	r := acceptance.Request{Producer: "a:b", InstallationID: "c", SourceKey: "d", SourceFingerprint: strings.Repeat("a", 64), AppID: "app", TenantID: "tenant", Type: "test", Data: []byte(`{"value":9007199254740993}`)}
	other := r
	other.Producer = "a"
	other.InstallationID = "b:c"
	if r.Identity() == other.Identity() {
		t.Fatal("ambiguous source identity")
	}
	fp, err := acceptance.Fingerprint(r)
	if err != nil {
		t.Fatal(err)
	}
	other = r
	other.Data = []byte(`{"value":9007199254740992}`)
	otherFP, err := acceptance.Fingerprint(other)
	if err != nil {
		t.Fatal(err)
	}
	if fp == otherFP {
		t.Fatal("rounded large integer")
	}
	other = r
	other.SourceFingerprint = strings.Repeat("a", 100000)
	if _, err = other.Normalize(); err == nil {
		t.Fatal("unbounded source fingerprint")
	}
}
