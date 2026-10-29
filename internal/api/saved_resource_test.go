package api

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/persistence"
)

func TestSavedResourceInputRejectsOwnerSubstitutionAndAmbiguousJSON(t *testing.T) {
	valid := `{"kind":"workspace","resourceId":"wsp_00000000000000000001","saved":false}`
	if _, ok := decodeSavedResourceRequest([]byte(valid)); !ok {
		t.Fatal("valid removal rejected")
	}
	for _, body := range []string{
		`null`, `{}`, valid + `{}`, strings.Replace(valid, `false`, `null`, 1),
		strings.Replace(valid, `false`, `"false"`, 1),
		strings.Replace(valid, `"saved":false`, `"saved":false,"saved":true`, 1),
		strings.Replace(valid, `"saved":false`, `"Saved":false`, 1),
		strings.Replace(valid, `"saved":false`, `"owner":"usr_other"`, 1),
		strings.Replace(valid, `"saved":false`, `"saved":false,"owner":"usr_other"`, 1),
		strings.Replace(valid, `wsp_`, `svc_`, 1),
		strings.Replace(valid, `workspace`, `future`, 1),
		strings.Repeat(" ", 1025) + valid,
	} {
		if _, ok := decodeSavedResourceRequest([]byte(body)); ok {
			t.Fatalf("invalid input accepted: %s", body)
		}
	}
}

func TestSavedResourceCursorBindsPrincipalKindScopeAndPurpose(t *testing.T) {
	owner := persistence.SavedResourceOwner{Kind: authn.PrincipalHuman, ID: "usr_saved_test"}
	scope := "iso_00000000000000000001"
	c := savedResourceCursor{Kind: "saved-resources", Version: 1, IsolationDomainID: scope, Principal: savedPrincipalBinding(owner), SavedAt: time.Now().UTC(), ID: "wsp_00000000000000000001"}
	encode := func(value savedResourceCursor) string {
		raw, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	query := "limit=1&cursor=" + encode(c)
	if limit, cursor, err := parseSavedResourceList(query, scope, owner); err != nil || limit != 1 || *cursor != c {
		t.Fatal("cursor roundtrip failed", err)
	}
	for _, other := range []persistence.SavedResourceOwner{{Kind: owner.Kind, ID: "usr_other"}, {Kind: authn.PrincipalService, ID: owner.ID}} {
		if _, _, err := parseSavedResourceList(query, scope, other); err == nil {
			t.Fatal("foreign principal cursor accepted")
		}
	}
	if _, _, err := parseSavedResourceList(query, "iso_00000000000000000002", owner); err == nil {
		t.Fatal("foreign scope cursor accepted")
	}
	for _, change := range []func(*savedResourceCursor){func(v *savedResourceCursor) { v.Kind = "resource-search" }, func(v *savedResourceCursor) { v.Version++ }, func(v *savedResourceCursor) { v.SavedAt = time.Time{} }, func(v *savedResourceCursor) { v.ID = "bad" }} {
		v := c
		change(&v)
		if _, _, err := parseSavedResourceList("cursor="+encode(v), scope, owner); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=01", "limit=1&limit=2", "cursor=", "cursor=x", "cursor=" + strings.Repeat("a", 513), "owner=usr_other", "q=x", "limit=%ZZ"} {
		if _, _, err := parseSavedResourceList(query, scope, owner); err == nil {
			t.Fatal("invalid query accepted", query)
		}
	}
	raw, _ := json.Marshal(c)
	for _, noncanonical := range []string{" " + string(raw), strings.Replace(string(raw), "{", `{"version":1,`, 1), string(raw) + "{}"} {
		if _, _, err := parseSavedResourceList("cursor="+base64.RawURLEncoding.EncodeToString([]byte(noncanonical)), scope, owner); err == nil {
			t.Fatal("noncanonical cursor accepted")
		}
	}
}
