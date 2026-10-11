package search

import "testing"

func TestNormalize(t *testing.T){
	got:=Normalize("  Dépo-\nsit\t  security   \n\n deposit ")
	want:="déposit security deposit"
	if got!=want{t.Fatalf("Normalize()=%q, want %q",got,want)}
}

func TestParseFilters(t *testing.T){
	query,filter,err:=Parse("lease type:pdf after:2026-01-02 tag:home")
	if err!=nil{t.Fatal(err)}
	if query!="lease"||filter.Type!="pdf"||filter.Tag!="home"||filter.After.IsZero(){t.Fatalf("unexpected parse: %q %#v",query,filter)}
}

func TestJoinTermsQuotesOperators(t *testing.T){
	if got:=joinTerms([]string{"security","deposit"}," AND ");got!=`"security" AND "deposit"`{t.Fatalf("query=%q",got)}
}
