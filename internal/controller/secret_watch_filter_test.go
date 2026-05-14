package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func newFilter(entries ...string) *secretWatchFilter {
	f := &secretWatchFilter{watched: make(map[string]struct{})}
	for i := 0; i+1 < len(entries); i += 2 {
		f.add(entries[i], entries[i+1])
	}
	return f
}

func secretObj(name string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name}}
}

func TestSecretWatchFilter_Matches(t *testing.T) {
	f := newFilter("ns1", "sec1", "ns2", "sec2")

	cases := []struct {
		ns, name string
		want     bool
	}{
		{"ns1", "sec1", true},
		{"ns2", "sec2", true},
		{"ns1", "sec2", false},
		{"ns2", "sec1", false},
		{"ns3", "sec3", false},
	}
	for _, tc := range cases {
		if got := f.matches(tc.ns, tc.name); got != tc.want {
			t.Errorf("matches(%q, %q) = %v, want %v", tc.ns, tc.name, got, tc.want)
		}
	}
}

func TestSecretWatchFilter_AddRemove(t *testing.T) {
	f := newFilter()

	f.add("ns", "sec")
	if !f.matches("ns", "sec") {
		t.Error("expected match after add")
	}

	f.remove("ns", "sec")
	if f.matches("ns", "sec") {
		t.Error("expected no match after remove")
	}

	// remove on absent key is a no-op
	f.remove("ns", "sec")
}

func TestSecretWatchFilter_Create(t *testing.T) {
	f := newFilter("ns", "tracked")
	if !f.Create(event.CreateEvent{Object: secretObj("tracked")}) {
		t.Error("Create: expected true for tracked secret")
	}
	if f.Create(event.CreateEvent{Object: secretObj("other")}) {
		t.Error("Create: expected false for untracked secret")
	}
}

func TestSecretWatchFilter_Update(t *testing.T) {
	f := newFilter("ns", "tracked")
	if !f.Update(event.UpdateEvent{ObjectNew: secretObj("tracked")}) {
		t.Error("Update: expected true for tracked secret")
	}
	if f.Update(event.UpdateEvent{ObjectNew: secretObj("other")}) {
		t.Error("Update: expected false for untracked secret")
	}
}

func TestSecretWatchFilter_Delete(t *testing.T) {
	f := newFilter("ns", "tracked")
	if !f.Delete(event.DeleteEvent{Object: secretObj("tracked")}) {
		t.Error("Delete: expected true for tracked secret")
	}
	if f.Delete(event.DeleteEvent{Object: secretObj("other")}) {
		t.Error("Delete: expected false for untracked secret")
	}
}

func TestSecretWatchFilter_Generic(t *testing.T) {
	f := newFilter("ns", "tracked")
	if !f.Generic(event.GenericEvent{Object: secretObj("tracked")}) {
		t.Error("Generic: expected true for tracked secret")
	}
	if f.Generic(event.GenericEvent{Object: secretObj("other")}) {
		t.Error("Generic: expected false for untracked secret")
	}
}
