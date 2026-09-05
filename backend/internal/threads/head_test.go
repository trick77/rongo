package threads

import "testing"

// A row the reader typed is its own head, and the rows that continue it name
// that row rather than repeating it — which is the whole point: the question
// text is copied onto every attempt, so only this link says it was asked once.
func TestHeadLinkGroupsAttemptsUnderOneQuestion(t *testing.T) {
	s, ctx, threadID, _ := newThreadStore(t)

	asked, err := s.AddQuestion(ctx, threadID, "ba", "de", "wie unterscheidet sich das?", 0)
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	if asked.Head() != asked.ID {
		t.Errorf("typed question Head() = %d, want its own id %d", asked.Head(), asked.ID)
	}

	resumed, err := s.AddQuestion(ctx, threadID, "ba", "de", "wie unterscheidet sich das?", asked.ID)
	if err != nil {
		t.Fatalf("add resume: %v", err)
	}
	// The third attempt names the question, not the attempt before it: a head
	// is the root of the chain so nothing has to be walked at read time.
	reexplained, err := s.AddQuestion(ctx, threadID, "dev", "de", "wie unterscheidet sich das?", resumed.Head())
	if err != nil {
		t.Fatalf("add re-explain: %v", err)
	}
	if reexplained.Head() != asked.ID {
		t.Errorf("re-explain Head() = %d, want the question %d", reexplained.Head(), asked.ID)
	}

	list, err := s.Messages(ctx, testSubject, threadID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d messages, want 3 — the record keeps every attempt", len(list))
	}
	for _, m := range list {
		if m.Question != "wie unterscheidet sich das?" {
			t.Errorf("message %d question = %q, want the question kept on every row", m.ID, m.Question)
		}
	}
	if list[0].HeadMessageID != 0 {
		t.Errorf("first row head = %d, want 0", list[0].HeadMessageID)
	}
	if list[1].HeadMessageID != asked.ID || list[2].HeadMessageID != asked.ID {
		t.Errorf("continuation heads = %d, %d, want %d for both",
			list[1].HeadMessageID, list[2].HeadMessageID, asked.ID)
	}
}
