package services

import (
	"testing"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/observer"
)

func TestSortEvents_StableOnTsThenSeq(t *testing.T) {
	events := []observer.ProgressEvent{
		{Ts: "2026-05-07T14:30:01.000Z", Seq: 3, Kind: "log"},
		{Ts: "2026-05-07T14:30:00.000Z", Seq: 2, Kind: "log"},
		{Ts: "2026-05-07T14:30:01.000Z", Seq: 1, Kind: "log"},
		{Ts: "2026-05-07T14:30:00.000Z", Seq: 1, Kind: "log"},
	}
	sortEvents(events)
	want := []int64{1, 2, 1, 3}
	for i, ev := range events {
		if ev.Seq != want[i] {
			t.Fatalf("position %d: expected seq=%d got %d (%v)", i, want[i], ev.Seq, events)
		}
	}
}

func TestDedupOnTsSeq_RemovesExactDuplicates(t *testing.T) {
	events := []observer.ProgressEvent{
		{Ts: "2026-05-07T14:30:00.000Z", Seq: 1, Kind: "phase", Phase: "a"},
		{Ts: "2026-05-07T14:30:00.000Z", Seq: 1, Kind: "phase", Phase: "a"},
		{Ts: "2026-05-07T14:30:01.000Z", Seq: 2, Kind: "phase", Phase: "b"},
	}
	out := dedupOnTsSeq(events)
	if len(out) != 2 {
		t.Fatalf("expected 2 unique events, got %d", len(out))
	}
}

func TestDedupOnTsSeq_KeepsZeroSeqDuplicates(t *testing.T) {
	// Lines without a stamped seq (seq=0) are passed through — we can't
	// distinguish two genuine events that happen to share a timestamp.
	events := []observer.ProgressEvent{
		{Ts: "2026-05-07T14:30:00.000Z", Seq: 0, Kind: "log"},
		{Ts: "2026-05-07T14:30:00.000Z", Seq: 0, Kind: "log"},
	}
	out := dedupOnTsSeq(events)
	if len(out) != 2 {
		t.Fatalf("expected zero-seq duplicates to pass through, got %d", len(out))
	}
}

func TestLastPhase_PicksMostRecent(t *testing.T) {
	events := []observer.ProgressEvent{
		{Kind: "phase", Phase: "first"},
		{Kind: "tool_use"},
		{Kind: "phase", Phase: "second"},
		{Kind: "log"},
	}
	if got := lastPhase(events); got != "second" {
		t.Fatalf("expected most-recent phase, got %q", got)
	}
}

func TestLastPhase_EmptyOnNoPhase(t *testing.T) {
	events := []observer.ProgressEvent{{Kind: "tool_use"}, {Kind: "log"}}
	if got := lastPhase(events); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestNextCursor_AdvancesPastLastEvent(t *testing.T) {
	events := []observer.ProgressEvent{
		{Ts: "2026-05-07T14:30:00.000Z", Seq: 1, Kind: "log"},
		{Ts: "2026-05-07T14:30:05.000Z", Seq: 2, Kind: "log"},
	}
	cursor := nextCursor(events, 0)
	want := time.Date(2026, 5, 7, 14, 30, 5, 0, time.UTC).UnixMilli() + 1
	if cursor != want {
		t.Fatalf("expected cursor=%d, got %d", want, cursor)
	}
}

func TestNextCursor_FallsBackWhenNoEvents(t *testing.T) {
	if got := nextCursor(nil, 4242); got != 4242 {
		t.Fatalf("expected fallback=4242, got %d", got)
	}
}

func TestResolveSinceTime_AnchorsToDispatchedAtMinusOneSecond(t *testing.T) {
	d := time.Date(2026, 5, 7, 14, 30, 10, 0, time.UTC)
	got := resolveSinceTime(0, &d)
	want := d.Add(-1 * time.Second)
	if !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestResolveSinceTime_PrefersExplicitMillis(t *testing.T) {
	d := time.Date(2026, 5, 7, 14, 30, 10, 0, time.UTC)
	got := resolveSinceTime(1700000000000, &d)
	if got.UnixMilli() != 1700000000000 {
		t.Fatalf("expected explicit cursor preserved, got %d", got.UnixMilli())
	}
}
