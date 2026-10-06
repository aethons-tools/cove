package jam

import "testing"

// The roster view adds a discord profile holding only the user id when the
// person has a Discord account but no inbox in the project. That is no
// delivery target: DeliveryFor must not offer it (a DM to "" would follow).
func TestDeliveryForNeedsAnAddress(t *testing.T) {
	h := Human{Name: "alice", Delivery: []DeliveryProfile{{Service: "discord", UserID: "111"}}}
	if p, ok := h.DeliveryFor("discord"); ok {
		t.Fatalf("DeliveryFor = %+v, want none (no address)", p)
	}
	if !h.DiscordBound() {
		t.Fatal("the id binding still counts for attribution")
	}
}

func TestPersonalDeliveryProblemWithoutInbox(t *testing.T) {
	s := NewMemStore()
	mustCreateProject(t, s, "acme")
	if err := s.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHuman("acme", Human{Name: "alice", Login: "local", Delivery: []DeliveryProfile{{Service: "discord", UserID: "111"}}}); err != nil {
		t.Fatal(err)
	}
	owner, _ := HumanByLogin(s, "acme", "local")
	if msg := personalDeliveryProblem(s, "acme", owner); msg == "" {
		t.Fatal("an owner with a Discord account but no inbox in the project can't receive DMs")
	}
}
