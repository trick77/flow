package timeline

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/sessions"
)

func (r *rig) do(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), method, r.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// Question and plan replies become the host's commands, one per request (the command id is the request's), and
// refuse what the agent could not use.
func TestReplies_questionAndPlan(t *testing.T) {
	r := newRig(t)
	base := "/s/" + r.id
	if c := r.post(t, base+"/questions/q1", `{"answers":[{"questionId":"q0","optionIds":["o1"]}]}`); c != http.StatusNoContent {
		t.Fatalf("question reply: %d", c)
	}
	c := r.fake.last()
	var qr event.QuestionReply
	_ = json.Unmarshal(c.Data, &qr)
	if c.Kind != event.CmdQuestionReply || c.ID != "reply-q1" || qr.RequestID != "q1" || len(qr.Answers) != 1 ||
		!slices.Equal(qr.Answers[0].OptionIds, []string{"o1"}) {
		t.Errorf("question command %s %s %+v", c.Kind, c.ID, qr)
	}
	if code := r.post(t, base+"/questions/q2", `{"text":"use the second"}`); code != http.StatusNoContent {
		t.Errorf("free-text answer: %d", code)
	}
	// optionIds is required in the contract: an answer with free text only still carries an empty list.
	if code := r.post(t, base+"/questions/q3", `{"answers":[{"questionId":"q0","text":"x"}]}`); code != http.StatusNoContent {
		t.Errorf("per-question text: %d", code)
	}
	if !strings.Contains(string(r.fake.last().Data), `"optionIds":[]`) {
		t.Errorf("optionIds not an empty list: %s", r.fake.last().Data)
	}
	for body, want := range map[string]int{
		`{}`: 400, `{"text":"  "}`: 400, `{"answers":[{"questionId":"q0","optionIds":[]}]}`: 400,
		`{"answers":[{"questionId":"","optionIds":["o0"]}]}`: 400, `nope`: 400,
	} {
		if code := r.post(t, base+"/questions/q9", body); code != want {
			t.Errorf("question reply %s: %d, want %d", body, code, want)
		}
	}
	if code := r.post(t, base+"/questions/gone", `{"text":"x"}`); code != http.StatusConflict {
		t.Errorf("question not pending: %d", code)
	}

	if code := r.post(t, base+"/plans/p1", `{"decision":"approved","mode":"accept_edits"}`); code != http.StatusNoContent {
		t.Fatalf("plan approval: %d", code)
	}
	c = r.fake.last()
	var pr event.PlanReply
	_ = json.Unmarshal(c.Data, &pr)
	if c.Kind != event.CmdPlanReply || c.ID != "reply-p1" || pr.Decision != event.PlanReplyDecisionApproved ||
		pr.Mode != event.PermissionModeAcceptEdits {
		t.Errorf("plan command %s %s %+v", c.Kind, c.ID, pr)
	}
	if code := r.post(t, base+"/plans/p2", `{"decision":"rejected","feedback":"smaller steps"}`); code != http.StatusNoContent {
		t.Errorf("plan rejection: %d", code)
	}
	for body, want := range map[string]int{
		`{"decision":"maybe"}`: 400, `{"decision":"rejected","mode":"auto"}`: 400,
		`{"decision":"approved","mode":"root"}`: 400,
	} {
		if code := r.post(t, base+"/plans/p9", body); code != want {
			t.Errorf("plan reply %s: %d, want %d", body, code, want)
		}
	}

	// A stopped sandbox holds no request: nothing is started for a reply.
	ensures, _ := r.sb.set(true)
	if code := r.post(t, base+"/questions/q5", `{"text":"x"}`); code != http.StatusConflict {
		t.Errorf("question reply in a stopped sandbox: %d", code)
	}
	if code := r.post(t, base+"/plans/p5", `{"decision":"approved"}`); code != http.StatusConflict {
		t.Errorf("plan reply in a stopped sandbox: %d", code)
	}
	if n, _ := r.sb.counts(); n != ensures {
		t.Errorf("%d bring-ups for replies", n-ensures)
	}
}

// A plan approved into a mode above the session's cap is refused, as a mode change is.
func TestReplies_planModeAboveCap(t *testing.T) {
	r := newRig(t)
	if err := r.api.Sessions.Store.ApplyMirror(t.Context(), r.id, sessions.Mirror{ModeCap: "accept_edits"}); err != nil {
		t.Fatal(err)
	}
	code, body := r.do(t, "POST", "/s/"+r.id+"/plans/p1", `{"decision":"approved","mode":"full_access"}`)
	if code != http.StatusBadRequest || !strings.Contains(string(body), "mode_above_cap") {
		t.Errorf("plan approval above the cap: %d %s", code, body)
	}
}

// Send now and remove reach the host under the message's id; a message no longer queued is not_found, and removing
// in a stopped sandbox starts nothing.
func TestQueue_sendNowAndRemove(t *testing.T) {
	r := newRig(t)
	base := "/s/" + r.id + "/queue/"
	if code, _ := r.do(t, "POST", base+"m2/send-now", ""); code != http.StatusNoContent {
		t.Errorf("send now: %d", code)
	}
	if c := r.fake.last(); c.Kind != event.CmdMessageSendNow || c.ID != "send-now-m2" {
		t.Errorf("send now sent %s %s", c.Kind, c.ID)
	}
	if code, _ := r.do(t, "DELETE", base+"m3", ""); code != http.StatusNoContent {
		t.Errorf("remove: %d", code)
	}
	if c := r.fake.last(); c.Kind != event.CmdMessageRemove || c.ID != "remove-m3" {
		t.Errorf("remove sent %s %s", c.Kind, c.ID)
	}
	if code, _ := r.do(t, "DELETE", base+"gone", ""); code != http.StatusNotFound {
		t.Errorf("remove of a message not queued: %d", code)
	}
	if code, _ := r.do(t, "POST", base+"gone/send-now", ""); code != http.StatusNotFound {
		t.Errorf("send now of a message not queued: %d", code)
	}
	ensures, _ := r.sb.set(true)
	if code, body := r.do(t, "DELETE", base+"m4", ""); code != http.StatusServiceUnavailable ||
		!strings.Contains(string(body), "sandbox_unavailable") {
		t.Errorf("remove in a stopped sandbox: %d %s", code, body)
	}
	if n, _ := r.sb.counts(); n != ensures {
		t.Error("remove started the sandbox")
	}
}

// The log reads in pages by seq, forwards and backwards, and answers sandbox_unavailable while the sandbox is
// stopped.
func TestReadSessionLog(t *testing.T) {
	r := newRig(t)
	if c := r.post(t, "/s/"+r.id+"/messages", `{"clientId":"m1","text":"go"}`); c != http.StatusAccepted {
		t.Fatal(c)
	}
	read := func(query string) apitypes.EventPage {
		t.Helper()
		code, body := r.do(t, "GET", "/s/"+r.id+"/log"+query, "")
		if code != http.StatusOK {
			t.Fatalf("log%s: %d %s", query, code, body)
		}
		var p apitypes.EventPage
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	seqs := func(p apitypes.EventPage) []int64 {
		var out []int64
		for _, raw := range p.Events {
			var e event.Event
			_ = json.Unmarshal(raw, &e)
			out = append(out, e.Seq)
		}
		return out
	}
	var all apitypes.EventPage
	for deadline := time.Now().Add(5 * time.Second); ; {
		all = read("")
		if all.LastSeq >= 6 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := int64(len(all.Events)); n != all.LastSeq || n < 6 || seqs(all)[0] != 1 {
		t.Fatalf("whole log: %v, lastSeq %d", seqs(all), all.LastSeq)
	}
	if got := seqs(read("?after=2&limit=3")); !slices.Equal(got, []int64{3, 4, 5}) {
		t.Errorf("forwards: %v", got)
	}
	if got := seqs(read("?before=5&limit=2")); !slices.Equal(got, []int64{3, 4}) {
		t.Errorf("backwards: %v", got)
	}
	if got := seqs(read("?before=3")); !slices.Equal(got, []int64{1, 2}) {
		t.Errorf("backwards to the start: %v", got)
	}
	if got := read("?after=" + "999"); len(got.Events) != 0 || got.LastSeq != all.LastSeq {
		t.Errorf("past the end: %+v", got)
	}
	// At the int64 maximum after+1 and after+limit overflow: still an empty page, answered at once.
	start := time.Now()
	if got := read("?after=9223372036854775807"); len(got.Events) != 0 || got.LastSeq != all.LastSeq ||
		time.Since(start) > 5*time.Second {
		t.Errorf("after at the int64 maximum: %+v in %s", got, time.Since(start))
	}
	for _, q := range []string{"?after=1&before=3", "?limit=0", "?limit=5000", "?after=-1", "?before=x"} {
		if code, _ := r.do(t, "GET", "/s/"+r.id+"/log"+q, ""); code != http.StatusBadRequest {
			t.Errorf("log%s: %d", q, code)
		}
	}
	if code, _ := r.do(t, "GET", "/s/nope/log", ""); code != http.StatusNotFound {
		t.Errorf("unknown session: %d", code)
	}
	r.sb.set(true)
	if code, body := r.do(t, "GET", "/s/"+r.id+"/log", ""); code != http.StatusServiceUnavailable ||
		!strings.Contains(string(body), "sandbox_unavailable") {
		t.Errorf("stopped sandbox: %d %s", code, body)
	}
}
