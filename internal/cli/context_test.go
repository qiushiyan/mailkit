package cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/qiushiyan/mailkit/internal/cluster"
	"github.com/qiushiyan/mailkit/internal/fixtures"
	"github.com/qiushiyan/mailkit/internal/mail"
)

// Rules: prefer labelled identifiers; discard an identifier matching more
// than 25 messages; weak probes must be time-bounded.

func relatedIDs(r result, t *testing.T) []string {
	t.Helper()
	var out []string
	for _, h := range r.jsonList(t, "related") {
		out = append(out, h["id"].(string))
	}
	return out
}

func TestContext_LabelledIdentifierWinsOverBareDigitRuns(t *testing.T) {
	// The IKEA seed carries "Order #1623209215", a PO box (530225) and a
	// onelink id (258595750). Only the labelled one may drive a probe.
	seed := fixtures.Message(t, fixtures.IKEASeed)
	h := newHarness(t, seed)
	r := mustOK(t, h.find("context", seed.ID))
	sig := r.json(t)["signals"].(map[string]any)
	ids := fmt.Sprint(sig["identifiers"])
	if !strings.Contains(ids, "1623209215") {
		t.Fatalf("order number missing from signals: %s", ids)
	}
	for _, bare := range []string{"530225", "258595750"} {
		if strings.Contains(ids, bare) {
			t.Errorf("bare digit run %s leaked into identifiers: %s", bare, ids)
		}
	}
}

func TestContext_TooCommonIdentifierIsDroppedAndNamed(t *testing.T) {
	seed := msg("seed", "c-seed", day(10), "shop@example.com", "Order 777001", para("Your order 777001 shipped. Thanks for shopping; the reference number is on every receipt."))
	build := func(n int) *harness {
		msgs := []mail.Message{seed}
		for i := range n {
			msgs = append(msgs, msg(fmt.Sprintf("x%d", i), fmt.Sprintf("cx%d", i), day(i%20), "other@elsewhere.org",
				"Receipt", para("Receipt reference 777001 appears in this unrelated message about something else entirely.")))
		}
		return newHarness(t, msgs...)
	}
	// 25 messages sharing the number, the seed included: distinctive enough.
	r := mustOK(t, build(cluster.TooCommon-1).find("context", "seed", "--limit", "10"))
	if d := r.json(t)["dropped_as_too_common"].([]any); len(d) != 0 {
		t.Fatalf("25 matches must not be dropped: %v", d)
	}
	// 26: a number that common is a date or a price, not a key.
	r = mustOK(t, build(cluster.TooCommon).find("context", "seed", "--limit", "10"))
	dropped := r.json(t)["dropped_as_too_common"].([]any)
	if len(dropped) != 1 {
		t.Fatalf("26 matches must drop the identifier even with --limit 10: %v", dropped)
	}
	d := dropped[0].(map[string]any)
	if d["identifier"] != "777001" || !strings.Contains(fmt.Sprint(d["hits"]), "more than") {
		t.Errorf("dropped entry must name the identifier and say 'more than': %v", d)
	}
	if len(relatedIDs(r, t)) != 0 {
		t.Errorf("nothing else links these; related should be empty, got %v", relatedIDs(r, t))
	}
	// --raw keeps it.
	r = mustOK(t, build(cluster.TooCommon).find("context", "seed", "--raw"))
	if len(relatedIDs(r, t)) == 0 {
		t.Errorf("--raw must keep the too-common hits")
	}
}

func TestContext_WeakProbesAreBoundedByTheWindow(t *testing.T) {
	// Same sender domain, and a subject-token overlap, placed one day
	// inside and one day outside the 30-day window on each side. Nothing
	// carries an identifier, so only the weak probes can link them.
	seed := msg("seed", "c-seed", day(30), "notify@taskrabbit.co.uk", "Assembly booked", para("Your tasker is on the way and will arrive in the agreed window tomorrow."))
	inside1 := msg("in1", "c1", day(30-29), "news@mail.taskrabbit.co.uk", "Assembly reminder", para("A reminder about the assembly that has been booked for you this month."))
	inside2 := msg("in2", "c2", day(30+29), "news@taskrabbit.co.uk", "Assembly done", para("The assembly has been completed by your tasker and the job is closed."))
	outside1 := msg("out1", "c3", day(30-31), "news@taskrabbit.co.uk", "Assembly offer", para("An older mail about assembly that predates the event by more than the window."))
	outside2 := msg("out2", "c4", day(30+31), "news@taskrabbit.co.uk", "Assembly survey", para("A later mail about assembly that follows the event by more than the window."))
	h := newHarness(t, seed, inside1, inside2, outside1, outside2)
	r := mustOK(t, h.find("context", "seed"))
	got := strings.Join(relatedIDs(r, t), ",")
	for _, want := range []string{"in1", "in2"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s is inside the window and shares domain+subject; missing from %q", want, got)
		}
	}
	for _, no := range []string{"out1", "out2"} {
		if strings.Contains(got, no) {
			t.Errorf("%s is outside the window; weak signals must not reach it: %q", no, got)
		}
	}
}

func TestContext_EveryHitCarriesItsReason(t *testing.T) {
	seed := msg("seed", "c-seed", day(10), "shop@example.com", "Order 888111222", para("Order 888111222 confirmed, thank you for your purchase from our store today."))
	other := msg("o1", "c-other", day(12), "courier@delivery.net", "Your parcel", para("Parcel for order 888111222 is out for delivery with the courier this afternoon."))
	h := newHarness(t, seed, other)
	hits := mustOK(t, h.find("context", "seed")).jsonList(t, "related")
	if len(hits) != 1 {
		t.Fatalf("want exactly the courier mail, got %d", len(hits))
	}
	why := fmt.Sprint(hits[0]["why"])
	if !strings.Contains(why, "888111222") {
		t.Errorf("the reason must name the shared identifier: %s", why)
	}
}
