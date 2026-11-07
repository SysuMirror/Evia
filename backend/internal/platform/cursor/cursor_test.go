package cursor

import (
	"strings"
	"testing"
)

func TestScopeAndIntegrity(t *testing.T) {
	c, e := New([]byte(strings.Repeat("a", 32)))
	if e != nil {
		t.Fatal(e)
	}
	token := c.Encode("projects:user-a", "0001")
	if p, e := c.Decode("projects:user-a", token); e != nil || p != "0001" {
		t.Fatal(p, e)
	}
	for _, v := range []struct{ s, t string }{{"projects:user-b", token}, {"projects:user-a", token + "x"}, {"projects:user-a", "malformed"}} {
		if _, e := c.Decode(v.s, v.t); e == nil {
			t.Fatal("accepted invalid cursor")
		}
	}
}
