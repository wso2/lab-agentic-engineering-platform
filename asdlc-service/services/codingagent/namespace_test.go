package codingagent

import "testing"

func TestRemoteWorkerNamespace_Deterministic(t *testing.T) {
	a := RemoteWorkerNamespace("d3adbeef-1234-4321-abcd-c0ffee123456")
	b := RemoteWorkerNamespace("d3adbeef-1234-4321-abcd-c0ffee123456")
	if a != b {
		t.Fatalf("non-deterministic: %s vs %s", a, b)
	}
}

func TestRemoteWorkerNamespace_Format(t *testing.T) {
	got := RemoteWorkerNamespace("d3adbeef-1234-4321-abcd-c0ffee123456")
	const want = "wc-d3adbeef-5f7c983f-remote-worker"
	if got != want {
		t.Fatalf("got %q; want %q", got, want)
	}
}

func TestRemoteWorkerNamespace_ShortUUID(t *testing.T) {
	got := RemoteWorkerNamespace("short")
	if got == "" || len(got) > 63 {
		t.Fatalf("unexpected NS shape: %q (len %d)", got, len(got))
	}
}
