package sql

import "testing"

func TestParseSmoke(t *testing.T) {
	q := `SELECT a.id, b.name FROM users a JOIN orders b ON a.id = b.uid WHERE a.age >= 18 AND b.total < 100.5 OR NOT a.vip = TRUE`
	s, err := Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(s.Fingerprint())
	if len(s.Tables) != 2 || len(s.Joins) != 1 || s.Where == nil {
		t.Fatalf("bad parse: %+v", s)
	}
	s2, err := Parse(`SELECT a.id, b.name FROM users a JOIN orders b ON a.id = b.uid WHERE b.total < 100.5 AND a.age >= 18 OR NOT a.vip = TRUE`)
	if err != nil {
		t.Fatal(err)
	}
	if s.Fingerprint() != s2.Fingerprint() {
		t.Fatalf("AND commutativity not canonical:\n%s\n%s", s.Fingerprint(), s2.Fingerprint())
	}
}
