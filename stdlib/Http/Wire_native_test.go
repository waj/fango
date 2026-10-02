package native

import "testing"

func TestBatchCollectsUpToWindow(t *testing.T) {
	b := NewBatch(4)
	if got := BatchPush(b, []byte("ab")); len(got) != 0 {
		t.Fatalf("early output %q", got)
	}
	if got := string(BatchPush(b, []byte("cde"))); got != "abcde" {
		t.Fatalf("window output %q", got)
	}
	if got := string(BatchPush(b, []byte("wxyz"))); got != "wxyz" {
		t.Fatalf("direct output %q", got)
	}
	BatchPush(b, []byte("f"))
	if got := string(BatchTake(b)); got != "f" {
		t.Fatalf("taken %q", got)
	}
	if len(BatchTake(b)) != 0 || BatchTotal(b) != 10 {
		t.Fatalf("total %d", BatchTotal(b))
	}
}
