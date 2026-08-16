package main

import (
	"reflect"
	"testing"
)

// The frame loop ticks every live application, so it needs the full distinct set
// of app namespaces — deduped, non-app cells excluded, and in a stable order so
// the per-frame tick sequence never varies between runs.
func TestLiveAppNamespaces(t *testing.T) {
	got := liveAppNamespaces([]string{
		"urn:hdm:apps:zeta:renderer",
		"urn:hdm:sys:map", // substrate, not an app
		"urn:hdm:apps:alpha:physics",
		"urn:hdm:apps:zeta:physics", // same app as the first
		"urn:hdm:ui:life",           // edge cell, not an app
		"urn:hdm:apps:alpha:renderer",
	})
	want := []string{"urn:hdm:apps:alpha", "urn:hdm:apps:zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("liveAppNamespaces = %v, want %v", got, want)
	}
}

func TestLiveAppNamespacesEmpty(t *testing.T) {
	if got := liveAppNamespaces(nil); len(got) != 0 {
		t.Fatalf("expected none, got %v", got)
	}
	if got := liveAppNamespaces([]string{"urn:hdm:sys:map", "urn:hdm:demo:wasteful"}); len(got) != 0 {
		t.Fatalf("non-app cells must not yield a namespace, got %v", got)
	}
}
