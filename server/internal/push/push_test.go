package push

import (
	"encoding/json"
	"testing"
)

func TestAPNsPayload(t *testing.T) {
	b, _ := json.Marshal(APNsPayload(Notification{Title: "t", Body: "b", DeepLink: "zeus://report/1", Image: "https://x/y.png"}))
	want := `{"aps":{"alert":{"body":"b","title":"t"},"mutable-content":1,"sound":"default"},"deeplink":"zeus://report/1","image":"https://x/y.png"}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	b, _ = json.Marshal(APNsPayload(Notification{Title: "t", Body: "b"}))
	if string(b) != `{"aps":{"alert":{"body":"b","title":"t"},"sound":"default"}}` {
		t.Fatalf("got %s", b)
	}
}
